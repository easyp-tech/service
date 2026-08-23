package config

import (
	"regexp"
	"strings"
)

// UnknownEnv reports variables that were aimed at this service and set nothing.
//
// environ is in os.Environ() form, so this is testable without mutating the
// process environment — which also keeps every test in this package parallel.
//
// A variable carrying a section's prefix but matching no setting is a mistake
// worth naming: the environment is the only layer where a typo produced no
// signal at all. The file layer has named its unknown keys since the strict
// walk; a mistyped SERVER_PORT_GRPCC simply did nothing, silently, and the
// environment is the layer Helm uses for every secret.
//
// These are warnings and never errors. Kubernetes injects link variables for
// every Service in the namespace — a Service named `db` produces DB_PORT,
// DB_SERVICE_HOST and DB_PORT_5432_TCP_ADDR — so refusing to start on an
// unrecognised prefixed variable would refuse to start in any namespace holding
// a Service called db, auth, audit, server or license.
func UnknownEnv(environ []string) (Diagnostics, error) {
	leaves, err := Leaves()
	if err != nil {
		return nil, err
	}

	prefixes, err := SectionPrefixes()
	if err != nil {
		return nil, err
	}

	known := make(map[string]bool, len(leaves))
	names := make([]string, 0, len(leaves))

	for _, leaf := range leaves {
		known[leaf.EnvKey] = true

		names = append(names, leaf.EnvKey)
	}

	var out Diagnostics

	for _, entry := range environ {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}

		if !reportableEnv(name, value, known, prefixes) {
			continue
		}

		diag := Diagnostic{
			Severity: SeverityWarning,
			Source:   SourceEnv,
			Path:     name,
			Message:  "environment variable matches no setting and was ignored",
		}

		if match, ok := bestMatch(name, names); ok {
			diag.Hint = "did you mean " + match + "?"
		}

		out = append(out, diag)
	}

	return out, nil
}

func reportableEnv(name, value string, known map[string]bool, prefixes []string) bool {
	// An empty variable is unset as far as this service is concerned — see
	// environmentLookuper — and compose defines every optional secret as
	// `"${VAR:-}"`, so reporting these would bury the real findings.
	if value == "" {
		return false
	}

	if known[name] || isAlias(name) {
		return false
	}

	if isKubernetesLinkVar(name, value) {
		return false
	}

	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

// The observability stack's own variables used to need an allowlist here: Mimir,
// Loki and Tempo read TELEMETRY_S3_* through -config.expand-env=true, and the
// names collided with this service's TELEMETRY_ section prefix. They were
// renamed to OBS_* in v0.13.0, so a name starting TELEMETRY_ is once again
// unambiguously a setting of this service.

// kubernetesLinkVar matches the shapes kubelet injects for every Service in the
// namespace. DB_PORT is the dangerous one: it collides with this service's DB_
// prefix on a cluster that happens to have a Service named db.
var kubernetesLinkVar = regexp.MustCompile(`_SERVICE_(HOST|PORT)(_|$)|_PORT_\d+_TCP(_|$)`)

func isKubernetesLinkVar(name, value string) bool {
	if kubernetesLinkVar.MatchString(name) {
		return true
	}

	// The bare <NAME>_PORT form carries a URL rather than a port number, which
	// is what tells it apart from a setting someone meant to write.
	return strings.HasSuffix(name, "_PORT") && strings.HasPrefix(value, "tcp://")
}
