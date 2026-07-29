package grpchelper_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/grpchelper"
)

// writeSelfSigned writes a throwaway certificate and key into dir and returns
// their paths.
func writeSelfSigned(t *testing.T, dir, name string) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{ //nolint:exhaustruct // only the fields under test matter
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name}, //nolint:exhaustruct // CN is enough here
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")

	require.NoError(t, os.WriteFile(certPath,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)) //nolint:exhaustruct
	require.NoError(t, os.WriteFile(keyPath,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)) //nolint:exhaustruct

	return certPath, keyPath
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestBuildServerCreds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "server")

	t.Run("disabled falls back to insecure", func(t *testing.T) {
		t.Parallel()

		creds, err := grpchelper.BuildServerCreds(config.TLSConfig{}, discardLogger()) //nolint:exhaustruct
		require.NoError(t, err)
		require.Equal(t, "insecure", creds.Info().SecurityProtocol)
	})

	t.Run("server-side TLS without a client CA", func(t *testing.T) {
		t.Parallel()

		creds, err := grpchelper.BuildServerCreds(config.TLSConfig{
			CertFile: certPath,
			KeyFile:  keyPath,
		}, discardLogger()) //nolint:exhaustruct
		require.NoError(t, err)
		require.Equal(t, "tls", creds.Info().SecurityProtocol)
	})

	t.Run("client CA is accepted", func(t *testing.T) {
		t.Parallel()

		creds, err := grpchelper.BuildServerCreds(config.TLSConfig{
			CertFile:     certPath,
			KeyFile:      keyPath,
			ClientCAFile: certPath,
		}, discardLogger())
		require.NoError(t, err)
		require.Equal(t, "tls", creds.Info().SecurityProtocol)
	})

	t.Run("missing key pair is an error", func(t *testing.T) {
		t.Parallel()

		_, err := grpchelper.BuildServerCreds(config.TLSConfig{
			CertFile: filepath.Join(dir, "absent.crt"),
			KeyFile:  filepath.Join(dir, "absent.key"),
		}, discardLogger()) //nolint:exhaustruct
		require.Error(t, err)
	})

	t.Run("unparsable client CA is an error", func(t *testing.T) {
		t.Parallel()

		empty := filepath.Join(dir, "empty-ca.pem")
		require.NoError(t, os.WriteFile(empty, []byte("not a certificate\n"), 0o600))

		_, err := grpchelper.BuildServerCreds(config.TLSConfig{
			CertFile:     certPath,
			KeyFile:      keyPath,
			ClientCAFile: empty,
		}, discardLogger())
		require.ErrorIs(t, err, grpchelper.ErrClientCANotParsed)
	})
}

// TestServerCredsRequireClientCert exercises the property that matters: a
// listener built with a client CA rejects a peer that presents none.
func TestServerCredsRequireClientCert(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "server")

	creds, err := grpchelper.BuildServerCreds(config.TLSConfig{
		CertFile:     certPath,
		KeyFile:      keyPath,
		ClientCAFile: certPath,
	}, discardLogger())
	require.NoError(t, err)

	ctx := t.Context()

	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	handshakeErr := make(chan error, 1)

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			handshakeErr <- acceptErr

			return
		}
		defer func() { _ = conn.Close() }()

		_, _, hsErr := creds.ServerHandshake(conn)
		handshakeErr <- hsErr
	}()

	var dialer net.Dialer

	clientConn, err := dialer.DialContext(ctx, "tcp", listener.Addr().String())
	require.NoError(t, err)
	defer func() { _ = clientConn.Close() }()

	client := tls.Client(clientConn, &tls.Config{ //nolint:exhaustruct // the server identity is not what this test checks
		// The server identity is irrelevant: this test only asserts that the
		// server refuses a client which presents no certificate.
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	})
	_ = client.HandshakeContext(ctx)

	select {
	case err := <-handshakeErr:
		require.Error(t, err, "server accepted a client that presented no certificate")
	case <-time.After(5 * time.Second):
		t.Fatal("server handshake did not finish")
	}
}
