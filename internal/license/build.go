package license

// publicKeyHex is the Ed25519 public key that licence tokens are verified
// against. It is baked in at link time:
//
//	go build -ldflags "-X github.com/easyp-tech/service/internal/license.publicKeyHex=<hex>"
//
// Keeping it in the binary rather than in configuration is the point: a running
// deployment cannot be pointed at a different signing authority without being
// rebuilt. An empty value means the build has no verification key at all, and
// no token can be honoured.
var publicKeyHex string //nolint:gochecknoglobals // set by the linker; there is nowhere else for it to live

// PublicKey returns the embedded verification key, empty when the build has none.
func PublicKey() string {
	return publicKeyHex
}
