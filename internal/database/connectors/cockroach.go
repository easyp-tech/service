package connectors

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/easyp-tech/service/internal/database"
)

var (
	_ yaml.Unmarshaler         = (*CockroachSSL)(nil)
	_ json.Unmarshaler         = (*CockroachSSL)(nil)
	_ encoding.TextUnmarshaler = (*CockroachSSL)(nil)
	_ database.Connector       = (*CockroachDB)(nil)
)

// CockroachSSL is a type for setting connection ssl mode to CockroachDB.
type CockroachSSL uint8

var ErrUnknownMode = errors.New("unknown mode")

// UnmarshalJSON implements json.Unmarshaler.
func (i *CockroachSSL) UnmarshalJSON(b []byte) error {
	str := ""
	err := json.Unmarshal(b, &str)
	if err != nil {
		return fmt.Errorf("json.Unmarshal: %w", err)
	}

	return i.UnmarshalText([]byte(str))
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (i *CockroachSSL) UnmarshalYAML(b *yaml.Node) error {
	return i.UnmarshalText([]byte(b.Value))
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (i *CockroachSSL) UnmarshalText(str []byte) error {
	switch string(str) {
	case CockroachSSLDisable.String():
		*i = CockroachSSLDisable
	case CockroachSSLAllow.String():
		*i = CockroachSSLAllow
	case CockroachSSLPrefer.String():
		*i = CockroachSSLPrefer
	case CockroachSSLRequire.String():
		*i = CockroachSSLRequire
	case CockroachSSLVerifyCa.String():
		*i = CockroachSSLVerifyCa
	case CockroachSSLVerifyFull.String():
		*i = CockroachSSLVerifyFull
	default:
		return fmt.Errorf("%w: %s", ErrUnknownMode, str)
	}

	return nil
}

// Enum.
const (
	_                      CockroachSSL = iota
	CockroachSSLDisable                 // disable
	CockroachSSLAllow                   // allow
	CockroachSSLPrefer                  // prefer
	CockroachSSLRequire                 // require
	CockroachSSLVerifyCa                // verify-ca
	CockroachSSLVerifyFull              // verify-full
)

type (
	// CockroachDBVariable sets variable for connections.
	CockroachDBVariable struct {
		Name  string `json:"name"  yaml:"name"`
		Value string `json:"value" yaml:"value"`
	}

	// CockroachDBOptions contains options for setting variables and cluster ID.
	CockroachDBOptions struct {
		Cluster  string              `json:"cluster"  yaml:"cluster"`
		Variable CockroachDBVariable `json:"variable" yaml:"variable"`
	}

	// CockroachDBParameters contains url parameters for connecting to database.
	CockroachDBParameters struct {
		ApplicationName string       `json:"application_name" yaml:"application_name"`
		Mode            CockroachSSL `hcl:"mode"              json:"mode"             yaml:"mode"`
		SSLRootCert     string       `json:"ssl_root_cert"    yaml:"ssl_root_cert"`
		SSLCert         string       `json:"ssl_cert"         yaml:"ssl_cert"`
		SSLKey          string       `json:"ssl_key"          yaml:"ssl_key"`

		// It isn't recommended, so it's disable. You must use CockroachDB.Password instead of it.
		// Password        string

		Options *CockroachDBOptions `json:"options" yaml:"options"`
	}

	// CockroachDB config for connecting to cockroachDB.
	CockroachDB struct {
		User       string                 `json:"user"       yaml:"user"`
		Password   string                 `json:"password"   yaml:"password"`
		Host       string                 `json:"host"       yaml:"host"`
		Port       int                    `json:"port"       yaml:"port"`
		Database   string                 `json:"database"   yaml:"database"`
		Parameters *CockroachDBParameters `json:"parameters" yaml:"parameters"`

		// We don't have support for UNIX domain socket.
		// DirectoryPath string `yaml:"directory_path" json:"directory_path"`
	}
)

// DSN convert struct to DSN and returns connection string.
func (c *CockroachDB) DSN() (string, error) {
	hostPort := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	str := fmt.Sprintf("postgres://%s:%s@%s/%s",
		c.User,
		c.Password,
		hostPort,
		c.Database,
	)

	uri, err := url.Parse(str)
	if err != nil {
		return "", fmt.Errorf("url.Parse: %w", err)
	}

	if c.Parameters != nil {
		uri.RawQuery = c.buildParameters().Encode()
	}

	return uri.String(), nil
}

func (c *CockroachDB) buildParameters() url.Values {
	parameters := url.Values{}
	if c.Parameters.ApplicationName != "" {
		parameters.Add("application_name", c.Parameters.ApplicationName)
	}

	if c.Parameters.Mode != 0 {
		parameters.Add("sslmode", c.Parameters.Mode.String())
	}

	if c.Parameters.SSLRootCert != "" {
		parameters.Add("sslrootcert", c.Parameters.SSLRootCert)
	}

	if c.Parameters.SSLCert != "" {
		parameters.Add("sslcert", c.Parameters.SSLCert)
	}

	if c.Parameters.SSLKey != "" {
		parameters.Add("sslkey", c.Parameters.SSLKey)
	}

	if c.Parameters.Options == nil {
		return parameters
	}

	var options []string
	if c.Parameters.Options.Cluster != "" {
		options = append(options, "--cluster="+c.Parameters.Options.Cluster)
	}

	if c.Parameters.Options.Variable.Name != "" {
		options = append(options, fmt.Sprintf("-c %s=%s", c.Parameters.Options.Variable.Name, c.Parameters.Options.Variable.Value))
	}

	parameters.Add("options", strings.Join(options, " "))

	return parameters
}
