package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientTLSOptions(t *testing.T) {
	t.Parallel()

	t.Run("insecure is an explicit opt-out", func(t *testing.T) {
		t.Parallel()

		opt, err := clientTLSOptions{insecure: true}.sdkOption() //nolint:exhaustruct
		require.NoError(t, err)
		require.NotNil(t, opt)
	})

	t.Run("zero value means TLS with the system trust store", func(t *testing.T) {
		t.Parallel()

		opt, err := clientTLSOptions{}.sdkOption() //nolint:exhaustruct
		require.NoError(t, err)
		require.NotNil(t, opt)
	})

	t.Run("half a key pair is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := clientTLSOptions{certFile: "client.crt"}.sdkOption() //nolint:exhaustruct
		require.ErrorIs(t, err, ErrClientCertPairRequired)

		_, err = clientTLSOptions{keyFile: "client.key"}.sdkOption() //nolint:exhaustruct
		require.ErrorIs(t, err, ErrClientCertPairRequired)
	})

	t.Run("unparsable CA is rejected", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "ca.pem")
		require.NoError(t, os.WriteFile(path, []byte("not a certificate\n"), 0o600))

		_, err := clientTLSOptions{caFile: path}.sdkOption() //nolint:exhaustruct
		require.ErrorIs(t, err, ErrCANotParsed)
	})

	t.Run("missing CA file is reported", func(t *testing.T) {
		t.Parallel()

		_, err := clientTLSOptions{caFile: filepath.Join(t.TempDir(), "absent.pem")}.sdkOption() //nolint:exhaustruct
		require.Error(t, err)
	})
}
