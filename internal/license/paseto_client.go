package license

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"aidanwoods.dev/go-paseto"

	"github.com/easyp-tech/service/internal/core"
)

// Claims every licence we issue carries. A token naming a different issuer or a
// different audience is not ours, however well it verifies.
//
// These are one half of a format agreed with the issuing tool in the licence
// registry. Kept as plain literals on both sides deliberately: sharing a package
// for six strings would couple a private repository to this module for less than
// it costs.
const (
	tokenIssuer   = "easyp.tech"
	tokenAudience = "easyp-service"
)

const (
	// clockSkew is the tolerance applied to both ends of the validity window, so
	// that a node whose clock drifts by well under a minute neither loses its
	// licence early nor gains one before it starts.
	clockSkew = time.Minute

	day = 24 * time.Hour

	// A v4.public token with a footer is "v4.public.<payload>.<footer>".
	tokenPartsWithFooter = 4
	footerPart           = 3
)

// PasetoLicenseClient verifies PASETO v4.public licence tokens offline.
//
// Nothing here reaches the network: the token states the tier, the key that
// signs it is configuration, and the release decides what the tier unlocks.
// Every failure resolves to community mode rather than to an error, because a
// licence problem must not be able to take the service down.
type PasetoLicenseClient struct {
	token       string
	publicKeys  map[string]paseto.V4AsymmetricPublicKey
	fallbackKey *paseto.V4AsymmetricPublicKey
	logger      *slog.Logger
	clock       func() time.Time

	// mu guards lastState, which exists only so that a licence that has not
	// changed is not re-announced on every refresh tick.
	mu        sync.Mutex
	lastState string
}

// option adjusts a client at construction. Unexported because the only
// legitimate caller is a test in this package.
type option func(*PasetoLicenseClient)

// withClock replaces the clock the validity window is measured against.
//
// Use it only for cases that cannot be expressed in the token itself. Anything
// about expiry or the grace period must be tested with a token whose dates are
// genuinely in the past: this clock is ours alone, and a test that moves it
// proves nothing about how a real expired token is treated.
func withClock(clock func() time.Time) option {
	return func(c *PasetoLicenseClient) {
		c.clock = clock
	}
}

// AnyKeyID is the reserved key id meaning "verify a token this map does not
// otherwise cover", including one whose footer carries no usable key id.
//
// It replaces a separate single-key setting. One trust anchor described by two
// fields is a way for the two to disagree, and they already had: the chart
// rendered only the map while compose passed both.
const AnyKeyID = "*"

// NewPasetoLicenseClient builds a client from a map of key id to hex-encoded
// Ed25519 public key. The entry under AnyKeyID, if present, covers every key id
// the rest of the map does not. It is no weaker than the others: the signature
// still has to verify against it.
//
// A key that does not decode is an error rather than a quiet fall back to
// community mode. Having no key at all is a valid community configuration;
// having a mistyped one is an operator error, and it should be loud.
func NewPasetoLicenseClient(
	token string,
	publicKeysHex map[string]string,
	logger *slog.Logger,
	opts ...option,
) (*PasetoLicenseClient, error) {
	keys := make(map[string]paseto.V4AsymmetricPublicKey, len(publicKeysHex))

	var fallback *paseto.V4AsymmetricPublicKey

	for kid, hexKey := range publicKeysHex {
		key, err := paseto.NewV4AsymmetricPublicKeyFromHex(strings.TrimSpace(hexKey))
		if err != nil {
			return nil, fmt.Errorf("licence public key for key id %q: %w", kid, err)
		}

		// Kept out of the map as well as recorded as the fallback, so that a
		// token whose footer literally says "*" cannot select it by name.
		if kid == AnyKeyID {
			fallback = &key

			continue
		}

		keys[kid] = key
	}

	client := &PasetoLicenseClient{
		token:       strings.TrimSpace(token),
		publicKeys:  keys,
		fallbackKey: fallback,
		logger:      logger,
		clock:       time.Now,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client, nil
}

// ValidateLicense verifies the configured token and reports what it grants.
func (c *PasetoLicenseClient) ValidateLicense(ctx context.Context) (core.LicenseClaims, error) {
	if c.token == "" {
		return core.CommunityLicenseClaims(), nil
	}

	key, ok := c.resolveKey(ctx)
	if !ok {
		return core.CommunityLicenseClaims(), nil
	}

	// Built without the library's own expiry rule on purpose. Everything to do
	// with time is decided in claimsFor, against c.clock; leaving NotExpired()
	// in place would reject an expired token here, before the grace period it
	// may still be entitled to could ever be considered.
	parser := paseto.NewParserWithoutExpiryCheck()
	parser.AddRule(paseto.IssuedBy(tokenIssuer))
	parser.AddRule(paseto.ForAudience(tokenAudience))

	token, err := parser.ParseV4Public(key, c.token, nil)
	if err != nil {
		// Which of the two it is decides where to go looking, and the two lead
		// opposite ways: a rule failure means the token is not one of ours and
		// the key is beside the point, while anything else means the token did
		// not come from the key it was checked against.
		if errors.Is(err, &paseto.RuleError{}) {
			c.report(ctx, slog.LevelWarn,
				"licence token is not one of ours: it must carry iss="+tokenIssuer+" and aud="+tokenAudience+"; "+
					"running in community mode",
				"error", err)

			return core.CommunityLicenseClaims(), nil
		}

		c.report(ctx, slog.LevelWarn,
			"licence token failed verification against the configured key; running in community mode",
			"error", err)

		return core.CommunityLicenseClaims(), nil
	}

	return c.claimsFor(ctx, token), nil
}

// resolveKey picks the key the token is to be verified against.
//
// The key id comes from the footer, which is read before anything is verified.
// That is safe because it selects a key and decides nothing else: a forged key
// id merely points at a key the signature will then fail against.
func (c *PasetoLicenseClient) resolveKey(ctx context.Context) (paseto.V4AsymmetricPublicKey, bool) {
	var none paseto.V4AsymmetricPublicKey

	if len(c.publicKeys) == 0 && c.fallbackKey == nil {
		c.report(ctx, slog.LevelWarn, "licence token supplied but no public key is configured to verify it against; "+
			"running in community mode. Set license.public_keys or LICENSE_PUBLIC_KEYS.")

		return none, false
	}

	kid, err := extractKID(c.token)
	if err != nil {
		if c.fallbackKey != nil {
			return *c.fallbackKey, true
		}

		c.report(ctx, slog.LevelWarn, "licence token carries no usable key id and no \"*\" public key is configured; "+
			"running in community mode", "error", err)

		return none, false
	}

	if key, ok := c.publicKeys[kid]; ok {
		return key, true
	}

	if c.fallbackKey != nil {
		return *c.fallbackKey, true
	}

	c.report(ctx, slog.LevelWarn, "licence token names a key id that is not configured; running in community mode",
		"kid", kid, "configured_kids", slices.Sorted(maps.Keys(c.publicKeys)))

	return none, false
}

// claimsFor decides what a token that has already passed signature, issuer and
// audience checks actually entitles the holder to.
func (c *PasetoLicenseClient) claimsFor(ctx context.Context, token *paseto.Token) core.LicenseClaims {
	notBefore, err := token.GetNotBefore()
	if err != nil {
		c.report(ctx, slog.LevelWarn, "licence token has no usable nbf claim; running in community mode", "error", err)

		return core.CommunityLicenseClaims()
	}

	expiry, err := token.GetExpiration()
	if err != nil {
		c.report(ctx, slog.LevelWarn, "licence token has no usable exp claim; running in community mode", "error", err)

		return core.CommunityLicenseClaims()
	}

	// A token that does not ask for a grace period does not get one.
	var graceDays int

	err = token.Get("grace_days", &graceDays)
	if err != nil {
		graceDays = 0
	}

	now := c.clock()

	if now.Before(notBefore.Add(-clockSkew)) {
		c.report(ctx, slog.LevelWarn, "licence token is not valid yet; running in community mode",
			"not_before", notBefore.Format(time.RFC3339), "now", now.Format(time.RFC3339))

		return core.CommunityLicenseClaims()
	}

	if now.After(expiry.Add(time.Duration(graceDays) * day).Add(clockSkew)) {
		c.report(ctx, slog.LevelWarn, "licence expired past its grace period; running in community mode",
			"expired_at", expiry.Format(time.RFC3339),
			"grace_days", graceDays,
			"now", now.Format(time.RFC3339))

		return core.CommunityLicenseClaims()
	}

	tier, err := token.GetString("tier")
	if err != nil || tier != core.LicenseTierEnterprise {
		c.report(ctx, slog.LevelWarn, "licence token names a tier this release does not grant; running in community mode",
			"tier", tier)

		return core.CommunityLicenseClaims()
	}

	customer, _ := token.GetString("customer_name")
	inGrace := now.After(expiry.Add(clockSkew))
	c.announce(ctx, customer, expiry, graceDays, inGrace)

	return core.EnterpriseLicenseClaims(expiry, inGrace)
}

// announce reports what a token was found to grant.
func (c *PasetoLicenseClient) announce(
	ctx context.Context,
	customer string,
	expiry time.Time,
	graceDays int,
	inGrace bool,
) {
	if inGrace {
		c.report(ctx, slog.LevelWarn, "licence has expired; running on its grace period",
			"customer", customer,
			"expired_at", expiry.Format(time.RFC3339),
			"grace_days", graceDays)

		return
	}

	c.report(ctx, slog.LevelInfo, "licence accepted",
		"customer", customer,
		"tier", core.LicenseTierEnterprise,
		"expires_at", expiry.Format(time.RFC3339))
}

// report logs a verdict, but only when it differs from the last one.
//
// ValidateLicense runs on every refresh tick — five minutes apart by default —
// for the lifetime of the process, so logging an unchanged verdict every time
// buries the transitions that are worth reading under hundreds of identical
// lines a day. This applies to the failures too: a licence that cannot be
// verified is a standing condition, and a standing condition belongs in the
// tier gauge, which is exported continuously. The log is for the moment it
// changes.
func (c *PasetoLicenseClient) report(ctx context.Context, level slog.Level, msg string, args ...any) {
	state := fmt.Sprint(append([]any{level, msg}, args...)...)

	c.mu.Lock()

	repeat := state == c.lastState
	c.lastState = state

	c.mu.Unlock()

	if repeat {
		return
	}

	c.logger.Log(ctx, level, msg, args...)
}

var (
	errNoFooter = errors.New("token carries no footer")
	errNoKeyID  = errors.New("footer names no key id")
)

// extractKID reads the key id from the token footer without verifying anything.
// See resolveKey for why that is safe.
func extractKID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < tokenPartsWithFooter {
		return "", errNoFooter
	}

	footer, err := base64.RawURLEncoding.DecodeString(parts[footerPart])
	if err != nil {
		return "", fmt.Errorf("decoding footer: %w", err)
	}

	var decoded struct {
		KID string `json:"kid"`
	}

	err = json.Unmarshal(footer, &decoded)
	if err != nil {
		return "", fmt.Errorf("parsing footer: %w", err)
	}

	if decoded.KID == "" {
		return "", errNoKeyID
	}

	return decoded.KID, nil
}
