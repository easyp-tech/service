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
var _ core.Registry = (*TracingRegistry)(nil)

// TracingRegistry — декоратор core.Registry, добавляющий span-ы трассировки.
// Проксирует все вызовы в реальную реализацию Registry.
type TracingRegistry struct {
	inner  core.Registry
	tracer trace.Tracer
}

// NewTracingRegistry creates a new TracingRegistry decorator wrapping the given Registry.
func NewTracingRegistry(inner core.Registry) *TracingRegistry {
	return &TracingRegistry{
		inner:  inner,
		tracer: otel.Tracer("registry"),
	}
}

// Get creates a span "registry.Get" with attributes db.system=postgresql, plugin.group,
// plugin.name, plugin.version. On error sets span status to Error with RecordError.
// On success wraps the returned Plugin in TracingPlugin.
func (r *TracingRegistry) Get(ctx context.Context, pluginGroup, pluginName, pluginVersion string) (core.Plugin, error) {
	ctx, span := r.tracer.Start(ctx, "registry.Get",
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("plugin.group", pluginGroup),
			attribute.String("plugin.name", pluginName),
			attribute.String("plugin.version", pluginVersion),
		))
	defer span.End()

	plugin, err := r.inner.Get(ctx, pluginGroup, pluginName, pluginVersion)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return nil, err
	}

	// Оборачиваем возвращённый Plugin в TracingPlugin
	return NewTracingPlugin(plugin, r.tracer), nil
}

// List creates a span "registry.List", proxies the call to the inner Registry,
// and on error sets span status to Error with RecordError.
func (r *TracingRegistry) List(ctx context.Context, filter core.PluginFilter, page core.PluginPage) ([]core.PluginInfo, error) {
	ctx, span := r.tracer.Start(ctx, "registry.List",
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
		))
	defer span.End()

	result, err := r.inner.List(ctx, filter, page)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return nil, err
	}

	return result, nil
}

// Create creates a span "registry.Create" with db attributes.
func (r *TracingRegistry) Create(ctx context.Context, req core.CreatePluginRequest) (*core.PluginInfo, error) {
	ctx, span := r.tracer.Start(ctx, "registry.Create",
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "INSERT"),
			attribute.String("plugin.group", req.Group),
			attribute.String("plugin.name", req.Name),
			attribute.String("plugin.version", req.Version),
		))
	defer span.End()

	result, err := r.inner.Create(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return nil, err
	}

	return result, nil
}

// Update creates a span "registry.Update" with db attributes.
func (r *TracingRegistry) Update(ctx context.Context, req core.UpdatePluginRequest) (*core.PluginInfo, error) {
	ctx, span := r.tracer.Start(ctx, "registry.Update",
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "UPDATE"),
			attribute.String("plugin.group", req.Group),
			attribute.String("plugin.name", req.Name),
			attribute.String("plugin.version", req.Version),
		))
	defer span.End()

	result, err := r.inner.Update(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return nil, err
	}

	return result, nil
}

// Delete creates a span "registry.Delete" with db attributes.
func (r *TracingRegistry) Delete(ctx context.Context, group, name, version string) error {
	ctx, span := r.tracer.Start(ctx, "registry.Delete",
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "DELETE"),
			attribute.String("plugin.group", group),
			attribute.String("plugin.name", name),
			attribute.String("plugin.version", version),
		))
	defer span.End()

	err := r.inner.Delete(ctx, group, name, version)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())

		return err
	}

	return nil
}
