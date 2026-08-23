package config

import (
	"github.com/sethvargo/go-envconfig"
)

// envAliases maps a setting's canonical environment variable to the other names
// this service will read it under, most preferred first.
//
// There is exactly one, and it is not a deprecation: OTEL_EXPORTER_OTLP_ENDPOINT
// is the name the OpenTelemetry SDKs define, and an operator who has configured
// a collector before will reach for it. It was read by nothing here, so that
// operator got a service with no traces and no error — while the variable that
// did work, TELEMETRY_OTLP_ENDPOINT, is ours alone.
//
// Aliases are resolved by wrapping the lookuper rather than by adding fields to
// Config, so the merge and the origin reporting keep asking one question —
// "is this leaf's variable set?" — and neither has to learn about second names.
var envAliases = map[string][]string{ //nolint:gochecknoglobals // static naming table
	"TELEMETRY_OTLP_ENDPOINT": {"OTEL_EXPORTER_OTLP_ENDPOINT"},
}

func isAlias(name string) bool {
	for _, alternatives := range envAliases {
		for _, alternative := range alternatives {
			if alternative == name {
				return true
			}
		}
	}

	return false
}

// aliasLookuper answers for a canonical variable using an alternative name when
// the canonical one is unset, and records which name actually supplied the
// value.
//
// The record is not bookkeeping: `config print --origin` exists to name the
// exact variable a value came from, and reporting the canonical name for a value
// that arrived under an alias would be precisely the kind of lie the command was
// built to prevent.
type aliasLookuper struct {
	inner envconfig.Lookuper
	used  map[string]string
}

func newAliasLookuper(inner envconfig.Lookuper) *aliasLookuper {
	return &aliasLookuper{inner: inner, used: map[string]string{}}
}

func (l *aliasLookuper) Lookup(key string) (string, bool) {
	if value, found := l.inner.Lookup(key); found {
		return value, true
	}

	for _, alternative := range envAliases[key] {
		if value, found := l.inner.Lookup(alternative); found {
			l.used[key] = alternative

			return value, true
		}
	}

	return "", false
}
