package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// tokenBytes is the length of a generated token before hex encoding. 32 bytes
// of entropy is far beyond guessing, which is why the stored digest can be a
// plain sha256 with no work factor.
const tokenBytes = 32

// runAuthNewToken generates a write token and prints it once, together with the
// configuration snippet that authorises it.
//
// The token itself is never stored by the service: only its digest goes into
// the configuration, so this output is the only time it exists in readable form.
func runAuthNewToken(name string) error {
	raw := make([]byte, tokenBytes)

	_, err := rand.Read(raw)
	if err != nil {
		return fmt.Errorf("rand.Read: %w", err)
	}

	token := hex.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))

	_, _ = fmt.Fprintf(os.Stdout, `Token (shown once — store it in your secret manager):

  %s

Add this to the service configuration; the digest is not a secret and can be
committed:

auth:
  write_tokens:
    - name: %q
      sha256: "%s"

Clients pass the token with --token, or in EASYP_TOKEN.
`, token, name, hex.EncodeToString(digest[:]))

	return nil
}
