package telemetry

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/grafana/pyroscope-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/core"
)

// Compile-time interface check.
var _ core.Plugin = (*TracingPlugin)(nil)

// TracingPlugin — декоратор core.Plugin, добавляющий span и метрику длительности.
type TracingPlugin struct {
	inner    core.Plugin
	tracer   trace.Tracer
	duration metric.Float64Histogram // plugin.execution.duration
}

// NewTracingPlugin creates a new TracingPlugin decorator wrapping the given Plugin.
//
// The tier this instrument's samples belong to is not set here. It rides on the
// resource, and Alloy turns resource attributes into labels on the way to Mimir
// — see resource_to_telemetry_conversion in config.alloy. Threading the tier
// through the registry and into every decorator would be a second way of saying
// what the resource already says.
func NewTracingPlugin(inner core.Plugin, tracer trace.Tracer) *TracingPlugin {
	meter := otel.Meter("registry")

	// The error is dropped rather than returned: on failure the SDK still hands
	// back a working no-op instrument, so the cost is this one histogram going
	// quiet. Failing construction here would take plugin execution down with it,
	// which is a steep price for a measurement.
	hist, _ := meter.Float64Histogram("plugin.execution.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of plugin code generation"))

	return &TracingPlugin{inner: inner, tracer: tracer, duration: hist}
}

// Generate creates a span "plugin.Generate" with attribute "plugin.image",
// records histogram "plugin.execution.duration" with attribute "plugin.name",
// and on error sets span status to Error with RecordError.
func (p *TracingPlugin) Generate(ctx context.Context, req *pluginpb.CodeGeneratorRequest) (*pluginpb.CodeGeneratorResponse, error) {
	info := p.inner.Info(ctx)
	imageName := info.Group + "/" + info.Name + ":" + info.Version

	ctx, span := p.tracer.Start(ctx, "plugin.Generate",
		trace.WithAttributes(attribute.String("plugin.image", imageName)))
	defer span.End()

	// Create child span for process execution
	ctx, processSpan := p.tracer.Start(ctx, "process.exec",
		trace.WithAttributes(
			attribute.String("process.command", imageName),
		))

	start := time.Now()

	// Wrap process execution with Pyroscope labels
	var resp *pluginpb.CodeGeneratorResponse
	var err error
	pyroscope.TagWrapper(ctx, pyroscope.Labels("plugin", imageName), func(ctx context.Context) {
		resp, err = p.inner.Generate(ctx, req)
	})

	elapsed := time.Since(start).Seconds()

	// End process span with error info if needed
	if err != nil {
		processSpan.RecordError(err)
		processSpan.SetStatus(codes.Error, err.Error())

		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			processSpan.SetAttributes(attribute.Int("process.exit_code", exitErr.ExitCode()))
		}
	}
	processSpan.End()

	p.duration.Record(ctx, elapsed,
		metric.WithAttributes(attribute.String("plugin.name", info.Name)))

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return nil, err
	}

	return resp, nil
}

// Info proxies the call to the inner Plugin without creating a span.
func (p *TracingPlugin) Info(ctx context.Context) *core.PluginInfo {
	return p.inner.Info(ctx)
}
