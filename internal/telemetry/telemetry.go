package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/grafana/pyroscope-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// tierAttribute names the licence tier a signal came from. The same spelling is
// used for the trace resource attribute and the Pyroscope tag, and it matches
// the label Alloy puts on scraped metrics, so one word selects a tier in all
// four signals.
const tierAttribute = "tier"

// newResource describes the process every trace and profile is attributed to.
//
// Both tiers keep the same service.name, so a trace search still finds all of
// the service and its service graph stays one node. The tier is a separate
// attribute, filterable as {resource.tier="..."}, which is how the metrics
// pipeline already spells the same distinction.
func newResource(cfg Config) *resource.Resource {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
	}

	if cfg.ServiceTier != "" {
		attrs = append(attrs, attribute.String(tierAttribute, cfg.ServiceTier))
	}

	return resource.NewWithAttributes(semconv.SchemaURL, attrs...)
}

// endpointIsURL distinguishes the two forms an OTLP endpoint arrives in. The
// spec-standard OTEL_EXPORTER_OTLP_ENDPOINT (read through the env alias) holds
// a URL like http://collector:4317, which WithEndpoint would treat as a bogus
// hostname; WithEndpointURL parses it and derives TLS from the scheme. A bare
// host:port keeps the explicit WithEndpoint+WithInsecure pair.
func endpointIsURL(endpoint string) bool {
	return strings.Contains(endpoint, "://")
}

// exporterOptions builds the dial options for both OTLP exporters from
// whichever endpoint form was configured.
func exporterOptions(endpoint string) ([]otlptracegrpc.Option, []otlpmetricgrpc.Option) {
	if endpointIsURL(endpoint) {
		return []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)},
			[]otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpointURL(endpoint)}
	}

	traceOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	}
	metricOpts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	}

	return traceOpts, metricOpts
}

// Init initialises TracerProvider, MeterProvider and returns a composite
// shutdown function together with a configured *slog.Logger.
//
// If OTLP exporter creation fails the error is logged as a warning and the
// service continues to operate without telemetry export.
func Init(ctx context.Context, cfg Config, baseHandler slog.Handler) (
	func(context.Context) error, *slog.Logger, error,
) {
	var shutdownFn func(context.Context) error
	var logger *slog.Logger

	res := newResource(cfg)

	// --- OTLP exporters ----------------------------------------------------
	//
	// An empty endpoint means no collector, and no collector means no exporter.
	// Building one anyway is not harmless: the gRPC connection is lazy, so
	// creation succeeds against an address nothing answers on and the failure
	// only appears later, as an export retried forever. A stack brought up
	// without an observability overlay would run correctly while filling its log
	// with connection errors — the state this check exists to avoid.
	var (
		traceExp  *otlptrace.Exporter
		metricExp metric.Exporter
	)

	if cfg.OTLPEndpoint == "" {
		slog.New(baseHandler).Info("no OTLP endpoint configured, traces and metrics will not be exported")
	} else {
		traceOpts, metricOpts := exporterOptions(cfg.OTLPEndpoint)

		var traceErr error

		traceExp, traceErr = otlptracegrpc.New(ctx, traceOpts...)
		if traceErr != nil {
			slog.New(baseHandler).Warn("failed to create OTLP trace exporter, continuing without trace export",
				"error", traceErr)

			traceExp = nil
		}

		var metricErr error

		metricExp, metricErr = otlpmetricgrpc.New(ctx, metricOpts...)
		if metricErr != nil {
			slog.New(baseHandler).Warn("failed to create OTLP metric exporter, continuing without metric export",
				"error", metricErr)

			metricExp = nil
		}
	}

	// --- TracerProvider ---------------------------------------------------
	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
	}
	if traceExp != nil {
		tpOpts = append(tpOpts, sdktrace.WithBatcher(traceExp))
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)

	// --- MeterProvider ----------------------------------------------------
	mpOpts := []metric.Option{
		metric.WithResource(res),
	}
	if metricExp != nil {
		mpOpts = append(mpOpts, metric.WithReader(
			metric.NewPeriodicReader(metricExp, metric.WithInterval(15*time.Second)), //nolint:mnd // 15s metric export interval
		))
	}
	mp := metric.NewMeterProvider(mpOpts...)
	otel.SetMeterProvider(mp)

	// --- W3C TraceContext propagation -------------------------------------
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// --- Logger -----------------------------------------------------------
	logger = slog.New(NewTraceHandler(baseHandler))

	// --- Pyroscope profiling -----------------------------------------------
	// Same reasoning as the exporters above: with nowhere to send profiles, the
	// profiler only produces upload failures on a timer.
	var profiler *pyroscope.Profiler

	if cfg.PyroscopeEndpoint == "" {
		slog.New(baseHandler).Info("no Pyroscope endpoint configured, profiles will not be uploaded")
	} else {
		var pyroErr error

		// Nil rather than an empty map when no tier is declared: the SDK sends
		// the tag set as-is, and an empty one would create a second, differently
		// tagged series for the same application.
		var pyroTags map[string]string
		if cfg.ServiceTier != "" {
			pyroTags = map[string]string{tierAttribute: cfg.ServiceTier}
		}

		profiler, pyroErr = pyroscope.Start(pyroscope.Config{
			ApplicationName: cfg.ServiceName,
			ServerAddress:   cfg.PyroscopeEndpoint,
			Tags:            pyroTags,
			ProfileTypes: []pyroscope.ProfileType{
				pyroscope.ProfileCPU,
				pyroscope.ProfileAllocObjects,
				pyroscope.ProfileAllocSpace,
				pyroscope.ProfileInuseObjects,
				pyroscope.ProfileInuseSpace,
				pyroscope.ProfileGoroutines,
			},
		})
		if pyroErr != nil {
			slog.New(baseHandler).Warn("failed to start Pyroscope profiler, continuing without profiling", "error", pyroErr)

			profiler = nil
		}
	}

	// --- Composite shutdown -----------------------------------------------
	shutdownFn = func(ctx context.Context) error {
		var pyroStopErr error
		if profiler != nil {
			pyroStopErr = profiler.Stop()
		}

		return errors.Join(
			tp.Shutdown(ctx),
			mp.Shutdown(ctx),
			pyroStopErr,
		)
	}

	return shutdownFn, logger, nil
}
