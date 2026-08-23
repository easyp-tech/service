package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/easyp-tech/service/internal/core"
)

// Compile-time interface check.
var _ core.Service = (*TracingCore)(nil)

// TracingCore — декоратор core.Service, добавляющий span-ы трассировки.
// Проксирует все вызовы в реальный Core.
type TracingCore struct {
	inner  core.Service
	tracer trace.Tracer
}

// NewTracingCore creates a new TracingCore decorator wrapping the given CoreService.
func NewTracingCore(inner core.Service) *TracingCore {
	return &TracingCore{
		inner:  inner,
		tracer: otel.Tracer("core"),
	}
}

// Generate creates a span "core.Generate" with attribute "plugin.name",
// proxies the call to the inner service, and on error sets span status to Error with RecordError.
func (c *TracingCore) Generate(ctx context.Context, req core.GenerateCodeRequest) (*core.GenerateCodeResponse, error) {
	ctx, span := c.tracer.Start(ctx, "core.Generate",
		trace.WithAttributes(attribute.String("plugin.name", req.PluginName)))
	defer span.End()

	resp, err := c.inner.Generate(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return nil, err
	}

	return resp, nil
}

// ListPlugins creates a span "core.ListPlugins",
// proxies the call to the inner service, and on error sets span status to Error with RecordError.
func (c *TracingCore) ListPlugins(ctx context.Context, filter core.PluginFilter, page core.PluginPage) (core.PluginList, error) {
	ctx, span := c.tracer.Start(ctx, "core.ListPlugins")
	defer span.End()

	result, err := c.inner.ListPlugins(ctx, filter, page)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return core.PluginList{}, err
	}

	return result, nil
}

// CreatePlugin creates a span "core.CreatePlugin" with plugin attributes.
func (c *TracingCore) CreatePlugin(ctx context.Context, req core.CreatePluginRequest) (*core.PluginInfo, error) {
	ctx, span := c.tracer.Start(ctx, "core.CreatePlugin",
		trace.WithAttributes(
			attribute.String("plugin.group", req.Group),
			attribute.String("plugin.name", req.Name),
			attribute.String("plugin.version", req.Version),
		))
	defer span.End()

	result, err := c.inner.CreatePlugin(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return nil, err
	}

	return result, nil
}

// UpdatePlugin creates a span "core.UpdatePlugin" with plugin attributes.
func (c *TracingCore) UpdatePlugin(ctx context.Context, req core.UpdatePluginRequest) (*core.PluginInfo, error) {
	ctx, span := c.tracer.Start(ctx, "core.UpdatePlugin",
		trace.WithAttributes(
			attribute.String("plugin.group", req.Group),
			attribute.String("plugin.name", req.Name),
			attribute.String("plugin.version", req.Version),
		))
	defer span.End()

	result, err := c.inner.UpdatePlugin(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return nil, err
	}

	return result, nil
}

// DeletePlugin creates a span "core.DeletePlugin" with plugin attributes.
func (c *TracingCore) DeletePlugin(ctx context.Context, group, name, version string) error {
	ctx, span := c.tracer.Start(ctx, "core.DeletePlugin",
		trace.WithAttributes(
			attribute.String("plugin.group", group),
			attribute.String("plugin.name", name),
			attribute.String("plugin.version", version),
		))
	defer span.End()

	err := c.inner.DeletePlugin(ctx, group, name, version)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return err
	}

	return nil
}
