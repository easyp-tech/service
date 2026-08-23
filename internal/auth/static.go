package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"google.golang.org/grpc/metadata"

	"github.com/easyp-tech/service/internal/config"
)

const (
	// authorizationKey is the metadata key credentials travel in. gRPC lowercases
	// header names, so the lookup must be lowercase too.
	authorizationKey = "authorization"
	// bearerPrefix is the scheme the token is presented under.
	bearerPrefix = "bearer "
)

var _ Authenticator = &StaticTokenAuthenticator{}

// StaticTokenAuthenticator matches a presented token against the digests listed
// in the configuration.
type StaticTokenAuthenticator struct {
	tokens []hashedToken
}

// hashedToken is one configured credential with its digest already decoded, so
// the comparison never has to parse hex on the request path.
type hashedToken struct {
	name   string
	digest []byte
}

// NewStaticTokenAuthenticator builds an authenticator over the configured
// tokens. An empty list is accepted and denies everything: a missing
// configuration must fail closed.
//
// Entries whose digest is not valid hex are skipped; config.AuthConfig.Validate
// rejects those at startup, so reaching this branch means validation was
// bypassed.
func NewStaticTokenAuthenticator(tokens config.TokenList) *StaticTokenAuthenticator {
	hashed := make([]hashedToken, 0, len(tokens))

	for _, token := range tokens {
		digest, err := hex.DecodeString(token.SHA256)
		if err != nil {
			continue
		}

		hashed = append(hashed, hashedToken{name: token.Name, digest: digest})
	}

	return &StaticTokenAuthenticator{tokens: hashed}
}

// Empty reports whether any credential is configured at all.
func (a *StaticTokenAuthenticator) Empty() bool {
	return len(a.tokens) == 0
}

// Authenticate resolves the bearer token in md to the actor it belongs to.
func (a *StaticTokenAuthenticator) Authenticate(_ context.Context, md metadata.MD) (Actor, error) {
	presented, ok := bearerToken(md)
	if !ok {
		return Actor{}, ErrNoCredentials
	}

	sum := sha256.Sum256([]byte(presented))

	// Every candidate is compared, and comparison is constant-time: neither the
	// position of a match nor the number of matching leading bytes is observable
	// through timing.
	var matched string

	for _, token := range a.tokens {
		if subtle.ConstantTimeCompare(sum[:], token.digest) == 1 {
			matched = token.name
		}
	}

	if matched == "" {
		return Actor{}, ErrUnknownToken
	}

	return Actor{Name: matched, Kind: KindToken}, nil
}

// bearerToken extracts the token from the authorization metadata.
func bearerToken(md metadata.MD) (string, bool) {
	values := md.Get(authorizationKey)
	if len(values) == 0 {
		return "", false
	}

	// The scheme is case-insensitive per RFC 7235; the token itself is not.
	raw := values[0]
	if len(raw) <= len(bearerPrefix) || !strings.EqualFold(raw[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}

	token := strings.TrimSpace(raw[len(bearerPrefix):])
	if token == "" {
		return "", false
	}

	return token, true
}
