package telemetry

// Config contains telemetry settings.
//
// An empty endpoint means there is no collector, and Init then builds no
// exporter for it at all. Neither carries a default for that reason: the OTLP
// connection is lazy, so an address nobody answers on is not an error but an
// export retried forever, and a deployment that never asked for telemetry would
// pay for it in log noise. The names of the variables these come from are
// TELEMETRY_OTEL_EXPORTER_OTLP_ENDPOINT and TELEMETRY_PYROSCOPE_ENDPOINT — the
// section prefix is part of them, and the bare OTel SDK names are read by
// nothing here.
type Config struct {
	OTLPEndpoint      string
	ServiceName       string
	PyroscopeEndpoint string

	// ServiceVersion is stamped into the resource of every trace and profile.
	// It comes from the same link-time variable the logs use, so the three
	// answer the question "which build is this" the same way.
	ServiceVersion string

	// ServiceTier names the licence tier this deployment serves, and is empty
	// when the deployment does not run tiers side by side.
	//
	// It is declared by whoever deploys, not derived from the licence: resource
	// attributes are fixed when Init runs and the licence has not been fetched
	// by then. The metrics pipeline already works this way — Alloy labels each
	// scrape from a container label — so this keeps one answer rather than two
	// that can disagree.
	ServiceTier string
}
