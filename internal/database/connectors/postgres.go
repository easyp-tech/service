package connectors

import (
	"encoding"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/easyp-tech/service/internal/database"
)

var (
	_ yaml.Unmarshaler         = (*PostgresSSL)(nil)
	_ json.Unmarshaler         = (*PostgresSSL)(nil)
	_ encoding.TextUnmarshaler = (*PostgresSSL)(nil)
	_ database.Connector       = (*PostgresDB)(nil)
)

// PostgresSSL is a type for setting connection ssl mode to PostgresDB.
type PostgresSSL uint8

// UnmarshalJSON implements json.Unmarshaler.
func (i *PostgresSSL) UnmarshalJSON(b []byte) error {
	str := ""
	err := json.Unmarshal(b, &str)
	if err != nil {
		return fmt.Errorf("json.Unmarshal: %w", err)
	}

	return i.UnmarshalText([]byte(str))
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (i *PostgresSSL) UnmarshalYAML(b *yaml.Node) error {
	return i.UnmarshalText([]byte(b.Value))
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (i *PostgresSSL) UnmarshalText(str []byte) error {
	switch string(str) {
	case PostgresSSLDisable.String():
		*i = PostgresSSLDisable
	case PostgresSSLAllow.String():
		*i = PostgresSSLAllow
	case PostgresSSLPrefer.String():
		*i = PostgresSSLPrefer
	case PostgresSSLRequire.String():
		*i = PostgresSSLRequire
	case PostgresSSLVerifyCa.String():
		*i = PostgresSSLVerifyCa
	case PostgresSSLVerifyFull.String():
		*i = PostgresSSLVerifyFull
	default:
		return fmt.Errorf("%w: %s", ErrUnknownMode, str)
	}

	return nil
}

// Enum.
const (
	_                     PostgresSSL = iota
	PostgresSSLDisable                // disable
	PostgresSSLAllow                  // allow
	PostgresSSLPrefer                 // prefer
	PostgresSSLRequire                // require
	PostgresSSLVerifyCa               // verify-ca
	PostgresSSLVerifyFull             // verify-full
)

type (
	// PostgresDBParameters contains url parameters for connecting to database.
	PostgresDBParameters struct {
		ApplicationName string      `json:"application_name" yaml:"application_name"`
		Mode            PostgresSSL `json:"mode"             yaml:"mode"`
		SSLRootCert     string      `json:"ssl_root_cert"    yaml:"ssl_root_cert"`
		SSLCert         string      `json:"ssl_cert"         yaml:"ssl_cert"`
		SSLKey          string      `json:"ssl_key"          yaml:"ssl_key"`
	}

	// PostgresDB config for connecting to postgresDB.
	PostgresDB struct {
		User       string                `json:"user"       yaml:"user"`
		Password   string                `json:"password"   yaml:"password"`
		Host       string                `json:"host"       yaml:"host"`
		Port       int                   `json:"port"       yaml:"port"`
		Database   string                `json:"database"   yaml:"database"`
		Parameters *PostgresDBParameters `json:"parameters" yaml:"parameters"`
	}
)

// DSN convert struct to DSN and returns connection string.
func (p *PostgresDB) DSN() (string, error) {
	hostPort := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	str := fmt.Sprintf("postgres://%s:%s@%s/%s",
		p.User,
		p.Password,
		hostPort,
		p.Database,
	)

	uri, err := url.Parse(str)
	if err != nil {
		return "", fmt.Errorf("url.Parse: %w", err)
	}

	if p.Parameters == nil {
		return uri.String(), nil
	}

	parameters := url.Values{}
	if p.Parameters.ApplicationName != "" {
		parameters.Add("application_name", p.Parameters.ApplicationName)
	}

	if p.Parameters.Mode != 0 {
		parameters.Add("sslmode", p.Parameters.Mode.String())
	}

	if p.Parameters.SSLRootCert != "" {
		parameters.Add("sslrootcert", p.Parameters.SSLRootCert)
	}

	if p.Parameters.SSLCert != "" {
		parameters.Add("sslcert", p.Parameters.SSLCert)
	}

	if p.Parameters.SSLKey != "" {
		parameters.Add("sslkey", p.Parameters.SSLKey)
	}

	uri.RawQuery = parameters.Encode()

	return uri.String(), nil
}
