package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"

	"github.com/easyp-tech/service/sdk"
)

var (
	// ErrClientCertPairRequired is returned when only one half of the client
	// key pair was supplied.
	ErrClientCertPairRequired = errors.New("--tls-cert and --tls-key must be passed together")
	// ErrCANotParsed is returned when a CA bundle holds no usable certificate.
	ErrCANotParsed = errors.New("no certificates found in CA file")
)

// clientTLSOptions holds the transport security flags shared by the client
// commands. The zero value means "TLS with the system trust store", so
// plaintext is never reached by omission — only by passing --insecure.
type clientTLSOptions struct {
	caFile   string
	certFile string
	keyFile  string
	insecure bool
}

// sdkOption turns the transport flags into the SDK option that configures
// the connection's credentials.
//
//nolint:ireturn // sdk.Option is the SDK's own interface; NewClient takes nothing else.
func (o clientTLSOptions) sdkOption() (sdk.Option, error) {
	if o.insecure {
		return sdk.WithInsecure(), nil
	}

	if (o.certFile != "") != (o.keyFile != "") {
		return nil, ErrClientCertPairRequired
	}

	tlsCfg := &tls.Config{ //nolint:exhaustruct // the remaining fields keep their secure defaults
		MinVersion: tls.VersionTLS12,
	}

	if o.certFile != "" {
		cert, err := tls.LoadX509KeyPair(o.certFile, o.keyFile)
		if err != nil {
			return nil, fmt.Errorf("tls.LoadX509KeyPair: %w", err)
		}

		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	if o.caFile != "" {
		pool, err := loadCAPool(o.caFile)
		if err != nil {
			return nil, err
		}

		tlsCfg.RootCAs = pool
	}

	return sdk.WithTransportCredentials(credentials.NewTLS(tlsCfg)), nil
}

// loadCAPool reads a PEM bundle into a certificate pool.
func loadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("os.ReadFile %s: %w", path, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%w: %s", ErrCANotParsed, path)
	}

	return pool, nil
}
