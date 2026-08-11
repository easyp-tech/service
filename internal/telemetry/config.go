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
}
