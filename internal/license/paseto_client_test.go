package license

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"aidanwoods.dev/go-paseto"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
)

// Every token below is built relative to time.Now(). Absolute dates are what let
// the previous version of these tests pass while the grace period did not work:
// a token whose expiry sits in the real future is never expired as far as the
// PASETO library is concerned, so moving the client's own clock exercised
// nothing. A token that is expired is expired for everyone.
const (
	unset = "\x00"

	testKID = "2026-08"
)

type tokenSpec struct {
	// kid goes into the footer. Empty means no footer at all.
	kid string
	// rawFooter, when set, replaces the generated footer. For malformed footers.
	rawFooter []byte

	// issuer, audience and tier default to the values a real licence carries.
	// unset means the claim is omitted entirely.
	issuer   string
	audience string
	tier     string

	customer string

	// notBefore and expiration are offsets from now; negative is in the past.
	notBefore  time.Duration
	expiration time.Duration

	graceDays int
	omitGrace bool

	omitNotBefore  bool
	omitExpiration bool
}

func mint(t *testing.T, key paseto.V4AsymmetricSecretKey, spec tokenSpec) string {
	t.Helper()

	now := time.Now()
	token := paseto.NewToken()

	token.SetIssuedAt(now)

	if !spec.omitNotBefore {
		token.SetNotBefore(now.Add(spec.notBefore))
	}

	if !spec.omitExpiration {
		token.SetExpiration(now.Add(spec.expiration))
	}

	setOptionalClaim(&token, "iss", spec.issuer, tokenIssuer)
	setOptionalClaim(&token, "aud", spec.audience, tokenAudience)
	setOptionalClaim(&token, "tier", spec.tier, core.LicenseTierEnterprise)
	setOptionalClaim(&token, "customer_name", spec.customer, "acme")

	if !spec.omitGrace {
		require.NoError(t, token.Set("grace_days", spec.graceDays))
	}

	token.SetFooter(footerFor(t, spec))

	return token.V4Sign(key, nil)
}

// setOptionalClaim writes a string claim. The zero value means "whatever a real
// licence would carry"; unset omits the claim, so its absence can be tested.
func setOptionalClaim(token *paseto.Token, name, value, fallback string) {
	switch value {
	case unset:
	case "":
		token.SetString(name, fallback)
	default:
		token.SetString(name, value)
	}
}

func footerFor(t *testing.T, spec tokenSpec) []byte {
	t.Helper()

	if spec.rawFooter != nil {
		return spec.rawFooter
	}

	if spec.kid == "" {
		return nil
	}

	footer, err := json.Marshal(map[string]string{"kid": spec.kid})
	require.NoError(t, err)

	return footer
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// validSpec is a licence that should be accepted: current, enterprise, with the
// key id the tests configure.
func validSpec() tokenSpec {
	return tokenSpec{
		kid:        testKID,
		notBefore:  -24 * time.Hour,
		expiration: 30 * 24 * time.Hour,
		graceDays:  14,
	}
}

func tierOf(t *testing.T, client *PasetoLicenseClient) string {
	t.Helper()

	claims, err := client.ValidateLicense(context.Background())
	require.NoError(t, err)

	return claims.Tier
}

// TestValidityWindow covers expiry, the grace period and the not-yet-valid case
// using tokens whose dates are genuinely in the past or the future. It does not
// touch the client's clock: the whole point is that these tokens are expired for
// the PASETO library too, which is the condition the previous implementation
// could not survive.
func TestValidityWindow(t *testing.T) {
	t.Parallel()

	key := paseto.NewV4AsymmetricSecretKey()
	keys := map[string]string{testKID: key.Public().ExportHex()}

	tests := map[string]struct {
		spec tokenSpec
		want string
	}{
		"current licence": {
			spec: validSpec(),
			want: core.LicenseTierEnterprise,
		},
		"expired ten days ago, fourteen days of grace": {
			spec: tokenSpec{kid: testKID, notBefore: -365 * 24 * time.Hour, expiration: -10 * 24 * time.Hour, graceDays: 14},
			want: core.LicenseTierEnterprise,
		},
		"expired just inside the grace period": {
			spec: tokenSpec{
				kid:        testKID,
				notBefore:  -365 * 24 * time.Hour,
				expiration: -14*24*time.Hour - 30*time.Second,
				graceDays:  14,
			},
			want: core.LicenseTierEnterprise,
		},
		"expired past the grace period": {
			spec: tokenSpec{kid: testKID, notBefore: -365 * 24 * time.Hour, expiration: -20 * 24 * time.Hour, graceDays: 14},
			want: core.LicenseTierCommunity,
		},
		"expired with no grace period": {
			spec: tokenSpec{kid: testKID, notBefore: -365 * 24 * time.Hour, expiration: -time.Hour, graceDays: 0},
			want: core.LicenseTierCommunity,
		},
		"expired with no grace_days claim": {
			spec: tokenSpec{kid: testKID, notBefore: -365 * 24 * time.Hour, expiration: -time.Hour, omitGrace: true},
			want: core.LicenseTierCommunity,
		},
		"no expiry claim": {
			spec: tokenSpec{kid: testKID, notBefore: -24 * time.Hour, omitExpiration: true},
			want: core.LicenseTierCommunity,
		},
		"no nbf claim": {
			spec: tokenSpec{kid: testKID, expiration: 30 * 24 * time.Hour, omitNotBefore: true},
			want: core.LicenseTierCommunity,
		},
		"not valid for another two days": {
			spec: tokenSpec{kid: testKID, notBefore: 2 * 24 * time.Hour, expiration: 30 * 24 * time.Hour},
			want: core.LicenseTierCommunity,
		},
		"starts within the clock skew tolerance": {
			spec: tokenSpec{kid: testKID, notBefore: 30 * time.Second, expiration: 30 * 24 * time.Hour},
			want: core.LicenseTierEnterprise,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, err := NewPasetoLicenseClient(mint(t, key, test.spec), keys, "", discardLogger())
			require.NoError(t, err)
			require.Equal(t, test.want, tierOf(t, client))
		})
	}
}

// TestKeySelection covers how the key id in the footer picks a verification key.
// The footer is read before anything is verified, so the important case is the
// last one: a token that names a key id it was not signed with.
func TestKeySelection(t *testing.T) {
	t.Parallel()

	keyA := paseto.NewV4AsymmetricSecretKey()
	keyB := paseto.NewV4AsymmetricSecretKey()

	hexA := keyA.Public().ExportHex()
	hexB := keyB.Public().ExportHex()

	tests := map[string]struct {
		signWith paseto.V4AsymmetricSecretKey
		spec     tokenSpec
		keys     map[string]string
		fallback string
		want     string
	}{
		"key id selects the matching key": {
			signWith: keyA,
			spec:     validSpec(),
			keys:     map[string]string{testKID: hexA, "2026-09": hexB},
			want:     core.LicenseTierEnterprise,
		},
		"key id names nothing configured": {
			signWith: keyA,
			spec:     tokenSpec{kid: "2027-01", notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			keys:     map[string]string{testKID: hexA},
			want:     core.LicenseTierCommunity,
		},
		"key id points at a key the token was not signed with": {
			signWith: keyB,
			spec:     validSpec(),
			keys:     map[string]string{testKID: hexA},
			want:     core.LicenseTierCommunity,
		},
		"no footer and no single key": {
			signWith: keyA,
			spec:     tokenSpec{notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			keys:     map[string]string{testKID: hexA},
			want:     core.LicenseTierCommunity,
		},
		"footer is not json": {
			signWith: keyA,
			spec:     tokenSpec{rawFooter: []byte("not-json"), notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			keys:     map[string]string{testKID: hexA},
			want:     core.LicenseTierCommunity,
		},
		"footer names an empty key id": {
			signWith: keyA,
			spec:     tokenSpec{rawFooter: []byte(`{"kid":""}`), notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			keys:     map[string]string{testKID: hexA},
			want:     core.LicenseTierCommunity,
		},
		"single key accepts any key id": {
			signWith: keyA,
			spec:     validSpec(),
			fallback: hexA,
			want:     core.LicenseTierEnterprise,
		},
		"single key accepts a token with no footer": {
			signWith: keyA,
			spec:     tokenSpec{notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			fallback: hexA,
			want:     core.LicenseTierEnterprise,
		},
		"single key still has to be the signer": {
			signWith: keyB,
			spec:     validSpec(),
			fallback: hexA,
			want:     core.LicenseTierCommunity,
		},
		"single key covers a key id missing from the map": {
			signWith: keyB,
			spec:     tokenSpec{kid: "2027-01", notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			keys:     map[string]string{testKID: hexA},
			fallback: hexB,
			want:     core.LicenseTierEnterprise,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			token := mint(t, test.signWith, test.spec)

			client, err := NewPasetoLicenseClient(token, test.keys, test.fallback, discardLogger())
			require.NoError(t, err)
			require.Equal(t, test.want, tierOf(t, client))
		})
	}
}

// TestTokenContents covers the claims that decide whether a correctly signed
// token is one of ours and what it grants.
func TestTokenContents(t *testing.T) {
	t.Parallel()

	key := paseto.NewV4AsymmetricSecretKey()
	keys := map[string]string{testKID: key.Public().ExportHex()}

	tests := map[string]struct {
		spec tokenSpec
		want string
	}{
		"issued by someone else": {
			spec: tokenSpec{kid: testKID, issuer: "evil.tech", notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			want: core.LicenseTierCommunity,
		},
		"no issuer": {
			spec: tokenSpec{kid: testKID, issuer: unset, notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			want: core.LicenseTierCommunity,
		},
		"meant for another audience": {
			spec: tokenSpec{kid: testKID, audience: "buf", notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			want: core.LicenseTierCommunity,
		},
		"no audience": {
			spec: tokenSpec{kid: testKID, audience: unset, notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			want: core.LicenseTierCommunity,
		},
		"community tier": {
			spec: tokenSpec{
				kid:        testKID,
				tier:       core.LicenseTierCommunity,
				notBefore:  -time.Hour,
				expiration: 30 * 24 * time.Hour,
			},
			want: core.LicenseTierCommunity,
		},
		"no tier claim": {
			spec: tokenSpec{kid: testKID, tier: unset, notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			want: core.LicenseTierCommunity,
		},
		"a tier this release does not know": {
			spec: tokenSpec{kid: testKID, tier: "enterprise-plus", notBefore: -time.Hour, expiration: 30 * 24 * time.Hour},
			want: core.LicenseTierCommunity,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, err := NewPasetoLicenseClient(mint(t, key, test.spec), keys, "", discardLogger())
			require.NoError(t, err)
			require.Equal(t, test.want, tierOf(t, client))
		})
	}
}

func TestTamperedToken(t *testing.T) {
	t.Parallel()

	key := paseto.NewV4AsymmetricSecretKey()
	keys := map[string]string{testKID: key.Public().ExportHex()}
	token := mint(t, key, validSpec())

	tests := map[string]string{
		"payload bytes changed": token[:len(token)-40] + "XXXXX" + token[len(token)-35:],
		"truncated":             token[:len(token)/2],
		"not a token at all":    "hunter2",
		"empty parts":           "v4.public..",
	}

	for name, tampered := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, err := NewPasetoLicenseClient(tampered, keys, "", discardLogger())
			require.NoError(t, err)
			require.Equal(t, core.LicenseTierCommunity, tierOf(t, client))
		})
	}
}

// TestEnterpriseGrant pins what a valid Enterprise licence actually unlocks. The
// token names a tier and nothing more; this is the release's side of that deal.
func TestEnterpriseGrant(t *testing.T) {
	t.Parallel()

	key := paseto.NewV4AsymmetricSecretKey()
	keys := map[string]string{testKID: key.Public().ExportHex()}

	client, err := NewPasetoLicenseClient(mint(t, key, validSpec()), keys, "", discardLogger())
	require.NoError(t, err)

	claims, err := client.ValidateLicense(context.Background())
	require.NoError(t, err)

	require.Equal(t, core.LicenseTierEnterprise, claims.Tier)
	require.Equal(t, core.LicenseUnlimited, claims.MaxWorkers)
	require.Equal(t, core.LicenseUnlimited, claims.MaxPlugins)
	require.ElementsMatch(t, []core.Feature{
		core.FeatureCodeGeneration,
		core.FeaturePluginListing,
		core.FeatureMCPServerTools,
		core.FeatureRateLimiting,
		core.FeaturePluginCRUD,
		core.FeatureAudit,
	}, claims.Features)
}

func TestNewPasetoLicenseClient(t *testing.T) {
	t.Parallel()

	key := paseto.NewV4AsymmetricSecretKey()
	valid := key.Public().ExportHex()

	t.Run("a key that is not hex is an error", func(t *testing.T) {
		t.Parallel()

		_, err := NewPasetoLicenseClient("token", map[string]string{testKID: strings.Repeat("z", 64)}, "", discardLogger())
		require.Error(t, err)
		require.Contains(t, err.Error(), testKID)
	})

	t.Run("a key of the wrong length is an error", func(t *testing.T) {
		t.Parallel()

		_, err := NewPasetoLicenseClient("token", map[string]string{testKID: valid[:32]}, "", discardLogger())
		require.Error(t, err)
	})

	t.Run("a bad single key is an error", func(t *testing.T) {
		t.Parallel()

		_, err := NewPasetoLicenseClient("token", nil, "nonsense", discardLogger())
		require.Error(t, err)
	})

	t.Run("surrounding whitespace is tolerated", func(t *testing.T) {
		t.Parallel()

		client, err := NewPasetoLicenseClient(
			"\n"+mint(t, key, validSpec())+"\n",
			map[string]string{testKID: "  " + valid + "\n"},
			"",
			discardLogger(),
		)
		require.NoError(t, err)
		require.Equal(t, core.LicenseTierEnterprise, tierOf(t, client))
	})

	t.Run("no token is community mode", func(t *testing.T) {
		t.Parallel()

		client, err := NewPasetoLicenseClient("", map[string]string{testKID: valid}, "", discardLogger())
		require.NoError(t, err)
		require.Equal(t, core.LicenseTierCommunity, tierOf(t, client))
	})

	t.Run("a token with no key to check it against is community mode", func(t *testing.T) {
		t.Parallel()

		client, err := NewPasetoLicenseClient(mint(t, key, validSpec()), nil, "", discardLogger())
		require.NoError(t, err)
		require.Equal(t, core.LicenseTierCommunity, tierOf(t, client))
	})
}

// TestAnnounceOnlyOnChange guards the log volume: ValidateLicense runs on every
// refresh tick, five minutes apart by default, and a line per tick buries the
// transitions worth reading.
func TestAnnounceOnlyOnChange(t *testing.T) {
	t.Parallel()

	key := paseto.NewV4AsymmetricSecretKey()
	keys := map[string]string{testKID: key.Public().ExportHex()}

	var buf strings.Builder

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	client, err := NewPasetoLicenseClient(mint(t, key, validSpec()), keys, "", logger)
	require.NoError(t, err)

	for range 5 {
		require.Equal(t, core.LicenseTierEnterprise, tierOf(t, client))
	}

	require.Equal(t, 1, strings.Count(buf.String(), "licence accepted"),
		"the licence has not changed, so it should be announced once")
}

// TestRejectionIsReportedOnlyOnce covers the same log volume concern on the
// failure path. A licence that cannot be verified is a standing condition, and a
// standing condition belongs in the tier gauge, which is exported continuously.
func TestRejectionIsReportedOnlyOnce(t *testing.T) {
	t.Parallel()

	key := paseto.NewV4AsymmetricSecretKey()
	stranger := paseto.NewV4AsymmetricSecretKey()

	var buf strings.Builder

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	client, err := NewPasetoLicenseClient(
		mint(t, stranger, validSpec()),
		map[string]string{testKID: key.Public().ExportHex()},
		"",
		logger,
	)
	require.NoError(t, err)

	for range 5 {
		require.Equal(t, core.LicenseTierCommunity, tierOf(t, client))
	}

	require.Equal(t, 1, strings.Count(buf.String(), "level=WARN"),
		"the same rejection, five times over, is worth saying once")
}

// TestGraceIsAnnouncedOnTransition uses the client's clock, which is legitimate
// here: the subject is the logging, and the token's own dates already decide the
// tier. Nothing about expiry is being asserted.
func TestGraceIsAnnouncedOnTransition(t *testing.T) {
	t.Parallel()

	key := paseto.NewV4AsymmetricSecretKey()
	keys := map[string]string{testKID: key.Public().ExportHex()}

	var buf strings.Builder

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Expires in an hour, with a fortnight of grace behind it.
	spec := validSpec()
	spec.expiration = time.Hour

	now := time.Now()
	client, err := NewPasetoLicenseClient(mint(t, key, spec), keys, "", logger,
		withClock(func() time.Time { return now }))
	require.NoError(t, err)

	require.Equal(t, core.LicenseTierEnterprise, tierOf(t, client))
	require.Contains(t, buf.String(), "licence accepted")

	// Two hours later the licence is inside its grace period.
	now = now.Add(2 * time.Hour)

	require.Equal(t, core.LicenseTierEnterprise, tierOf(t, client))
	require.Contains(t, buf.String(), "running on its grace period")
}

func TestExtractKID(t *testing.T) {
	t.Parallel()

	t.Run("reads the key id from the footer", func(t *testing.T) {
		t.Parallel()

		key := paseto.NewV4AsymmetricSecretKey()

		kid, err := extractKID(mint(t, key, validSpec()))
		require.NoError(t, err)
		require.Equal(t, testKID, kid)
	})

	t.Run("a token with no footer has no key id", func(t *testing.T) {
		t.Parallel()

		key := paseto.NewV4AsymmetricSecretKey()
		spec := validSpec()
		spec.kid = ""

		_, err := extractKID(mint(t, key, spec))
		require.ErrorIs(t, err, errNoFooter)
	})
}
