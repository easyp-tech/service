package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
)

// Core defines the interface for interacting with the plugin server.
type Core struct {
	metrics     Metrics
	registry    Registry
	featureGate FeatureGate
	auditCh     chan<- AuditEntry
	logger      *slog.Logger
}

// New creates a new Core instance.
// If featureGate is nil, all features are considered available (backward compatibility).
func New(metrics Metrics, registry Registry, featureGate FeatureGate, auditCh chan<- AuditEntry, logger *slog.Logger) *Core {
	return &Core{
		metrics:     metrics,
		registry:    registry,
		featureGate: featureGate,
		auditCh:     auditCh,
		logger:      logger,
	}
}

// sendAudit sends an audit entry to the audit channel.
// Blocks until the entry is sent or the context is cancelled.
func (c *Core) sendAudit(ctx context.Context, entry AuditEntry) { //nolint:funcorder,lll // sendAudit is a helper used by public methods above it
	select {
	case c.auditCh <- entry:
	case <-ctx.Done():
		c.logger.Warn("audit send cancelled", "entry_id", entry.ID, "operation", entry.OperationType)
	}
}

// Generate generates code by plugin.
func (c *Core) Generate(ctx context.Context, req GenerateCodeRequest) (*GenerateCodeResponse, error) {
	start := time.Now()

	group, err := getGroup(req.PluginName)
	if err != nil {
		c.auditError(ctx, OperationGenerateCode, req.PluginName, start, err)

		return nil, fmt.Errorf("getGroup: %w", err)
	}

	name, version, err := getNameAndVersion(req.PluginName)
	if err != nil {
		c.auditError(ctx, OperationGenerateCode, req.PluginName, start, err)

		return nil, fmt.Errorf("getNameAndVersion: %w", err)
	}

	plugin, err := c.registry.Get(ctx, group, name, version)
	if err != nil {
		c.auditError(ctx, OperationGenerateCode, req.PluginName, start, err)

		return nil, fmt.Errorf("c.registry.Get: %w", err)
	}

	generatedCode, err := plugin.Generate(ctx, req.Payload)
	if err != nil {
		c.auditError(ctx, OperationGenerateCode, req.PluginName, start, err)

		return nil, fmt.Errorf("plugin.Generate: %w", err)
	}

	err = c.metrics.GenerateCode(ctx, *plugin.Info(ctx))
	if err != nil {
		c.auditError(ctx, OperationGenerateCode, req.PluginName, start, err)

		return nil, fmt.Errorf("c.metrics.GenerateCode: %w", err)
	}

	metadata := map[string]any{
		"file_count": len(generatedCode.GetFile()),
	}
	c.auditSuccess(ctx, OperationGenerateCode, req.PluginName, start, metadata)

	return &GenerateCodeResponse{
		Payload: generatedCode,
	}, nil
}

// ListPlugins retrieves a list of plugins matching the filter.
func (c *Core) ListPlugins(ctx context.Context, filter PluginFilter) ([]PluginInfo, error) {
	start := time.Now()

	plugins, err := c.registry.List(ctx, filter)
	if err != nil {
		c.auditError(ctx, OperationListPlugins, "", start, err)

		return nil, fmt.Errorf("c.registry.List: %w", err)
	}

	metadata := map[string]any{
		"plugin_count": len(plugins),
	}
	c.auditSuccess(ctx, OperationListPlugins, "", start, metadata)

	return plugins, nil
}

func getGroup(pluginName string) (string, error) {
	splitArray := strings.Split(pluginName, "/")
	if len(splitArray) != pluginNameParts {
		return "", fmt.Errorf("%w: %s", ErrInvalidPluginName, pluginName)
	}

	return splitArray[0], nil
}

func getNameAndVersion(pluginName string) (string, string, error) {
	splitArray := strings.Split(pluginName, "/")
	if len(splitArray) != pluginNameParts {
		return "", "", fmt.Errorf("%w: %s", ErrInvalidPluginName, pluginName)
	}

	nameVersion := strings.Split(splitArray[1], ":")
	if len(nameVersion) != pluginNameParts {
		return "", "", fmt.Errorf("%w: %s", ErrInvalidPluginName, pluginName)
	}

	return nameVersion[0], nameVersion[1], nil
}

var (
	nameRegexp    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	versionRegexp = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
)

const pluginNameParts = 2 // "{group}/{name}:{version}" splits into exactly 2 parts by "/" and ":"

func validateGroupName(group, name string) error {
	if !nameRegexp.MatchString(group) {
		return fmt.Errorf("%w: invalid group %q", ErrInvalidPluginName, group)
	}
	if !nameRegexp.MatchString(name) {
		return fmt.Errorf("%w: invalid name %q", ErrInvalidPluginName, name)
	}

	return nil
}

func validateVersion(version string) error {
	if version != "latest" && !versionRegexp.MatchString(version) {
		return fmt.Errorf("%w: invalid version %q", ErrInvalidPluginName, version)
	}

	return nil
}

// checkFeature returns ErrFeatureDenied if the given feature is not enabled.
// If featureGate is nil, all features are considered available.
func (c *Core) checkFeature(feature Feature) error { //nolint:funcorder // checkFeature is a helper used by public methods above it
	if c.featureGate == nil {
		return nil
	}
	if c.featureGate.Enabled(feature) {
		return nil
	}

	return fmt.Errorf("%w: feature %s", ErrFeatureDenied, feature)
}

// CreatePlugin registers a new plugin in the registry.
func (c *Core) CreatePlugin(ctx context.Context, req CreatePluginRequest) (*PluginInfo, error) {
	start := time.Now()
	pluginName := req.Group + "/" + req.Name + ":" + req.Version

	err := c.checkFeature(FeaturePluginCRUD)
	if err != nil {
		c.auditError(ctx, OperationCreatePlugin, pluginName, start, err)

		return nil, err
	}
	err = validateGroupName(req.Group, req.Name)
	if err != nil {
		c.auditError(ctx, OperationCreatePlugin, pluginName, start, err)

		return nil, err
	}
	err = validateVersion(req.Version)
	if err != nil {
		c.auditError(ctx, OperationCreatePlugin, pluginName, start, err)

		return nil, err
	}

	// Check MaxPlugins limit.
	if limit := c.featureGate.MaxPlugins(); limit >= 0 {
		plugins, err := c.registry.List(ctx, PluginFilter{})
		if err != nil {
			c.auditError(ctx, OperationCreatePlugin, pluginName, start, err)

			return nil, fmt.Errorf("c.registry.List: %w", err)
		}
		if len(plugins) >= limit {
			err = fmt.Errorf("%w: current %d, limit %d", ErrMaxPluginsExceeded, len(plugins), limit)
			c.auditError(ctx, OperationCreatePlugin, pluginName, start, err)

			return nil, err
		}
	}

	info, err := c.registry.Create(ctx, req)
	if err != nil {
		c.auditError(ctx, OperationCreatePlugin, pluginName, start, err)

		return nil, fmt.Errorf("c.registry.Create: %w", err)
	}

	c.auditSuccess(ctx, OperationCreatePlugin, pluginName, start, nil)

	return info, nil
}

// UpdatePlugin modifies config and tags of an existing plugin.
// Group, Name, and Version are immutable identifiers used as lookup keys.
func (c *Core) UpdatePlugin(ctx context.Context, req UpdatePluginRequest) (*PluginInfo, error) {
	start := time.Now()
	pluginName := req.Group + "/" + req.Name + ":" + req.Version

	err := c.checkFeature(FeaturePluginCRUD)
	if err != nil {
		c.auditError(ctx, OperationUpdatePlugin, pluginName, start, err)

		return nil, err
	}
	err = validateGroupName(req.Group, req.Name)
	if err != nil {
		c.auditError(ctx, OperationUpdatePlugin, pluginName, start, err)

		return nil, err
	}
	err = validateVersion(req.Version)
	if err != nil {
		c.auditError(ctx, OperationUpdatePlugin, pluginName, start, err)

		return nil, err
	}

	info, err := c.registry.Update(ctx, req)
	if err != nil {
		c.auditError(ctx, OperationUpdatePlugin, pluginName, start, err)

		return nil, fmt.Errorf("c.registry.Update: %w", err)
	}

	c.auditSuccess(ctx, OperationUpdatePlugin, pluginName, start, nil)

	return info, nil
}

// DeletePlugin removes a plugin from the registry.
// In-flight GenerateCode executions that already obtained a Plugin reference
// will complete fully; only subsequent requests for this plugin will fail.
func (c *Core) DeletePlugin(ctx context.Context, group, name, version string) error {
	start := time.Now()
	pluginName := group + "/" + name + ":" + version

	err := c.checkFeature(FeaturePluginCRUD)
	if err != nil {
		c.auditError(ctx, OperationDeletePlugin, pluginName, start, err)

		return err
	}

	err = c.registry.Delete(ctx, group, name, version)
	if err != nil {
		c.auditError(ctx, OperationDeletePlugin, pluginName, start, err)

		return fmt.Errorf("c.registry.Delete: %w", err)
	}

	c.auditSuccess(ctx, OperationDeletePlugin, pluginName, start, nil)

	return nil
}

// errorCode classifies an error into a domain error code string.
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrInvalidPluginName):
		return "INVALID_PLUGIN_NAME"
	case errors.Is(err, ErrGenerationFailed):
		return "GENERATION_FAILED"
	case errors.Is(err, ErrServerOverloaded):
		return "SERVER_OVERLOADED"
	case errors.Is(err, ErrShuttingDown):
		return "SHUTTING_DOWN"
	case errors.Is(err, ErrAlreadyExists):
		return "ALREADY_EXISTS"
	case errors.Is(err, ErrMaxPluginsExceeded):
		return "MAX_PLUGINS_EXCEEDED"
	case errors.Is(err, ErrFeatureDenied):
		return "FEATURE_DENIED"
	case errors.Is(err, ErrStorageUnavailable):
		return "STORAGE_UNAVAILABLE"
	case errors.Is(err, ErrBinaryNotUploaded):
		return "BINARY_NOT_UPLOADED"
	default:
		return "INTERNAL"
	}
}

// auditSuccess creates and sends a success audit entry.
func (c *Core) auditSuccess(ctx context.Context, opType, pluginName string, start time.Time, metadata map[string]any) {
	if metadata == nil {
		metadata = make(map[string]any)
	}
	id, _ := uuid.NewV4()
	c.sendAudit(ctx, AuditEntry{
		ID:            id,
		OperationType: opType,
		PluginName:    pluginName,
		CallerAddress: CallerIPFromContext(ctx),
		Status:        AuditStatusSuccess,
		DurationMs:    time.Since(start).Milliseconds(),
		Metadata:      metadata,
		CreatedAt:     time.Now(),
	})
}

// auditError creates and sends an error audit entry.
func (c *Core) auditError(ctx context.Context, opType, pluginName string, start time.Time, err error) {
	metadata := make(map[string]any)
	id, _ := uuid.NewV4()
	c.sendAudit(ctx, AuditEntry{
		ID:            id,
		OperationType: opType,
		PluginName:    pluginName,
		CallerAddress: CallerIPFromContext(ctx),
		Status:        AuditStatusError,
		ErrorCode:     errorCode(err),
		ErrorMessage:  err.Error(),
		DurationMs:    time.Since(start).Milliseconds(),
		Metadata:      metadata,
		CreatedAt:     time.Now(),
	})
}
