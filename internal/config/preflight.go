package config

import (
	"fmt"
	"os"
)

// CheckFiles reports paths the configuration names that cannot be read.
//
// Separate from Validate, which stays a pure function of the struct: it touches
// no filesystem and no clock, and every test in this package is parallel on that
// basis. This is the part that has to look at the disk, so it is a different
// call with a different contract.
//
// The licence path is the one that matters. A mistyped LICENSE_FILE passed
// validation, and the service then started in community mode and served
// perfectly well — so a paid deployment silently became a free one, with the
// only evidence a log line among thousands. Everything about the two-tier design
// makes that failure quiet; this is where it is made loud.
func (c *Config) CheckFiles() Diagnostics {
	var out Diagnostics

	files := []struct {
		name string
		path string
	}{
		{"server.tls.cert_file", c.Server.TLS.CertFile},
		{"server.tls.key_file", c.Server.TLS.KeyFile},
		{"server.tls.client_ca_file", c.Server.TLS.ClientCAFile},
		{"license.file", c.License.File},
	}

	for _, file := range files {
		if file.path == "" {
			continue
		}

		err := readable(file.path)
		if err != nil {
			out = append(out, Diagnostic{
				Severity: SeverityError,
				Source:   SourceFile,
				Path:     file.name,
				Message:  fmt.Sprintf("%s cannot be read: %v", file.path, err),
			})
		}
	}

	return out
}

// readable opens the file rather than stating it: a path that exists but is not
// readable by this user fails at exactly the same moment as one that is missing,
// and a check that only stats would pass and let it happen anyway.
func readable(path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return err //nolint:wrapcheck // the os error already names the path and the reason
	}

	return handle.Close() //nolint:wrapcheck // same
}
