package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// sha256HexLength is the length of a sha256 digest in hex characters.
const sha256HexLength = 64

var (
	// ErrTokenNameRequired is returned when a token entry carries no label.
	ErrTokenNameRequired = errors.New("auth.write_tokens: name is required")
	// ErrTokenHashRequired is returned when a token entry carries no digest.
	ErrTokenHashRequired = errors.New("auth.write_tokens: sha256 is required")
	// ErrTokenHashMalformed is returned when a digest is not 64 hex characters.
	// Caught here rather than at request time because a mistyped digest matches
	// nothing and would otherwise only surface as an authentication failure in
	// production.
	ErrTokenHashMalformed = errors.New("auth.write_tokens: sha256 must be 64 hex characters")
	// ErrTokenNameDuplicate is returned when two entries share a name, which
	// would make the audit trail ambiguous.
	ErrTokenNameDuplicate = errors.New("auth.write_tokens: duplicate name")
	// ErrTokenMalformedEnv is returned when the environment encoding of the
	// token list cannot be parsed.
	ErrTokenMalformedEnv = errors.New("auth.write_tokens: expected name=sha256 pairs separated by commas")
)

// AuthConfig configures authentication of the mutating RPCs. Reads are
// anonymous; writes require one of the tokens below.
//
// Only digests are stored, never the tokens themselves, so this section is not
// a secret and can live in version control or a ConfigMap. An empty list denies
// every write — a forgotten configuration must break registration rather than
// leave the registry open.
type AuthConfig struct {
	WriteTokens TokenList `env:"WRITE_TOKENS" yaml:"write_tokens"`
}

// WriteToken is one credential permitted to call the mutating RPCs. Name is a
// label, not a secret: it identifies the caller in the audit log.
type WriteToken struct {
	Name   string `env:"NAME"   yaml:"name"`
	SHA256 string `env:"SHA256" yaml:"sha256"`
}

// TokenList is the set of credentials permitted to write.
type TokenList []WriteToken

// EnvDecode parses the environment encoding "name=sha256,name=sha256".
// It exists for parity with the env-only startup path; the YAML form is the
// primary one, since digests are not secret and belong next to the rest of the
// configuration.
func (t *TokenList) EnvDecode(val string) error {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		*t = nil

		return nil
	}

	parsed := make(TokenList, 0, strings.Count(trimmed, ",")+1)

	for _, pair := range strings.Split(trimmed, ",") {
		name, digest, found := strings.Cut(strings.TrimSpace(pair), "=")
		if !found {
			return fmt.Errorf("%w: got %q", ErrTokenMalformedEnv, pair)
		}

		parsed = append(parsed, WriteToken{
			Name:   strings.TrimSpace(name),
			SHA256: strings.TrimSpace(digest),
		})
	}

	*t = parsed

	return nil
}

// Validate checks every token entry.
func (c AuthConfig) Validate() error {
	seen := make(map[string]struct{}, len(c.WriteTokens))

	for idx, token := range c.WriteTokens {
		if token.Name == "" {
			return fmt.Errorf("%w (entry %d)", ErrTokenNameRequired, idx)
		}

		if token.SHA256 == "" {
			return fmt.Errorf("%w (%s)", ErrTokenHashRequired, token.Name)
		}

		if len(token.SHA256) != sha256HexLength {
			return fmt.Errorf("%w, got %d (%s)", ErrTokenHashMalformed, len(token.SHA256), token.Name)
		}

		if _, err := hex.DecodeString(token.SHA256); err != nil {
			return fmt.Errorf("%w (%s): %w", ErrTokenHashMalformed, token.Name, err)
		}

		if _, duplicate := seen[token.Name]; duplicate {
			return fmt.Errorf("%w: %s", ErrTokenNameDuplicate, token.Name)
		}

		seen[token.Name] = struct{}{}
	}

	return nil
}
