package grpchelper

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/easyp-tech/service/internal/config"
)

// ErrClientCANotParsed is returned when the client CA file contains no usable
// certificate — an empty or PEM-less file would otherwise disable client
// verification without any visible failure.
var ErrClientCANotParsed = errors.New("no certificates found in client CA file")

// BuildServerCreds builds the transport credentials for the gRPC listener.
//
// With a client CA configured the server requires and verifies a client
// certificate, i.e. mutual TLS. With TLS disabled it returns insecure
// credentials and logs a warning: plaintext must never be entered silently.
//
//nolint:ireturn // TransportCredentials is grpc's own interface; grpc.Creds takes nothing else.
func BuildServerCreds(cfg config.TLSConfig, log *slog.Logger) (credentials.TransportCredentials, error) {
	if !cfg.Enabled() {
		log.Warn("gRPC server is running WITHOUT TLS: traffic is unencrypted and unauthenticated")

		return insecure.NewCredentials(), nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("tls.LoadX509KeyPair: %w", err)
	}

	tlsCfg := &tls.Config{ //nolint:exhaustruct // the remaining fields keep their secure defaults
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	if cfg.ClientCAFile != "" {
		pool, err := loadCertPool(cfg.ClientCAFile)
		if err != nil {
			return nil, err
		}

		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		log.Warn("gRPC server TLS has no client CA: any client may connect",
			"hint", "set server.tls.client_ca_file to require client certificates")
	}

	return credentials.NewTLS(tlsCfg), nil
}

// loadCertPool reads a PEM bundle into a certificate pool.
func loadCertPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("os.ReadFile %s: %w", path, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%w: %s", ErrClientCANotParsed, path)
	}

	return pool, nil
}
