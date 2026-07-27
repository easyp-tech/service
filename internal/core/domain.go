// Package core contains the domain types and interfaces for the plugin server.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/gofrs/uuid/v5"
	"google.golang.org/protobuf/types/pluginpb"
)

// Errors.
var (
	ErrNotFound           = errors.New("not found")
	ErrInvalidPluginName  = errors.New("invalid plugin name")
	ErrGenerationFailed   = errors.New("code generation failed")
	ErrServerOverloaded   = errors.New("server overloaded")
	ErrShuttingDown       = errors.New("server shutting down")
	ErrAlreadyExists      = errors.New("already exists")
	ErrMaxPluginsExceeded = errors.New("max plugins exceeded")
	ErrFeatureDenied      = errors.New("feature denied")
	ErrStorageUnavailable = errors.New("binary storage unavailable")
	ErrBinaryNotUploaded  = errors.New("plugin archive not found in binary storage")
)

// Feature identifies a service capability for licensing and feature-gating.
type Feature int

const (
	FeatureCodeGeneration  Feature = iota // Базовая генерация кода
	FeaturePluginListing                  // Листинг плагинов
	FeatureMCPServerTools                 // MCP server tools
	FeatureRateLimiting                   // Rate limiting
	FeaturePluginCRUD                     // CRUD операции с плагинами
	FeatureMultiTenancy                   // Мультитенантность (Enterprise)
	FeatureResponseCaching                // Кэширование ответов (Enterprise)
	FeatureAudit                          // Аудит (Enterprise)
)

// featureNames содержит строковые представления Feature для метрик и логирования.
var featureNames = [...]string{
	FeatureCodeGeneration:  "code_generation",
	FeaturePluginListing:   "plugin_listing",
	FeatureMCPServerTools:  "mcp_server_tools",
	FeatureRateLimiting:    "rate_limiting",
	FeaturePluginCRUD:      "plugin_crud",
	FeatureMultiTenancy:    "multi_tenancy",
	FeatureResponseCaching: "response_caching",
	FeatureAudit:           "audit",
}

// String возвращает строковое представление Feature для метрик и логирования.
func (f Feature) String() string {
	if int(f) < 0 || int(f) >= len(featureNames) {
		return unknownValue
	}

	return featureNames[f]
}

// Audit operation types.
const (
	OperationGenerateCode = "GENERATE_CODE"
	OperationListPlugins  = "LIST_PLUGINS"
	OperationCreatePlugin = "CREATE_PLUGIN"
	OperationUpdatePlugin = "UPDATE_PLUGIN"
	OperationDeletePlugin = "DELETE_PLUGIN"
)

// Audit statuses.
const (
	AuditStatusSuccess = "success"
	AuditStatusError   = "error"
)

type (
	// Metrics defines the interface for collecting metrics about core operations.
	Metrics interface {
		// GenerateCode records metrics for a code generation request.
		// The pluginName parameter identifies which plugin was used (e.g., "grpc/go:v1.36.9").
		GenerateCode(ctx context.Context, info PluginInfo) error
		// ObserveGenerationDuration records the duration of a code generation attempt.
		ObserveGenerationDuration(ctx context.Context, pluginName string, duration time.Duration)
		// IncGenerationErrors increments the generation error counter with the given error type.
		IncGenerationErrors(ctx context.Context, pluginName string, errorType string)
		// IncGenerationRetries increments the generation retry counter.
		IncGenerationRetries(ctx context.Context, pluginName string)
	}

	// Registry provides access to available plugins.
	Registry interface {
		// Get retrieves a plugin by its identifier.
		// The pluginName parameter specifies the plugin to retrieve (e.g., "protobuf/go:v1.36.9").
		// Returns an error if the plugin is not found or cannot be loaded.
		Get(ctx context.Context, pluginGroup, pluginName, pluginVersion string) (Plugin, error)
		// List retrieves a list of plugins matching the filter.
		List(ctx context.Context, filter PluginFilter) ([]PluginInfo, error)
		// Create registers a new plugin in the registry.
		Create(ctx context.Context, req CreatePluginRequest) (*PluginInfo, error)
		// Update modifies config and tags of an existing plugin.
		Update(ctx context.Context, req UpdatePluginRequest) (*PluginInfo, error)
		// Delete removes a plugin from the registry.
		Delete(ctx context.Context, group, name, version string) error
	}

	// Plugin represents a code generator plugin that processes protobuf definitions.
	Plugin interface {
		// Generate processes a code generation request and produces generated code.
		// Takes a protobuf CodeGeneratorRequest and returns a CodeGeneratorResponse
		// containing the generated files or an error if generation fails.
		Generate(ctx context.Context, req *pluginpb.CodeGeneratorRequest) (*pluginpb.CodeGeneratorResponse, error)
		// Info retrieves information about a plugin by its identifier.
		Info(ctx context.Context) *PluginInfo
	}

	// GenerateCodeRequest represents an incoming request to generate code using a specific plugin.
	GenerateCodeRequest struct {
		// PluginName identifies the plugin to use for code generation.
		// Format: "<language>:v<version>" (e.g., "go:v1.36.9", "python:v3.20.0").
		PluginName string
		// Payload contains the protobuf code generation request with source files and parameters.
		Payload *pluginpb.CodeGeneratorRequest
	}

	// GenerateCodeResponse wraps the response from a code generation operation.
	GenerateCodeResponse struct {
		// Payload contains the protobuf code generation response with generated files.
		Payload *pluginpb.CodeGeneratorResponse
	}

	// PluginInfo represents information about a plugin.
	PluginInfo struct {
		ID        uuid.UUID
		Group     string
		Name      string
		Version   string
		Tags      []string
		CreatedAt time.Time
	}

	// PluginFilter represents a filter for listing plugins.
	PluginFilter struct {
		Group   string
		Name    string
		Version string
		Tags    []string
	}

	// CreatePluginRequest represents a request to register a new plugin.
	CreatePluginRequest struct {
		Group   string
		Name    string
		Version string
		Config  json.RawMessage
		Tags    []string
	}

	// UpdatePluginRequest represents a request to update an existing plugin.
	// Group, Name, and Version are immutable lookup keys.
	UpdatePluginRequest struct {
		Group   string
		Name    string
		Version string
		Config  json.RawMessage
		Tags    []string
	}

	// AuditEntry представляет одну запись аудит-журнала.
	AuditEntry struct {
		ID            uuid.UUID
		OperationType string
		PluginName    string
		CallerAddress string
		Status        string
		ErrorCode     string
		ErrorMessage  string
		DurationMs    int64
		Metadata      map[string]any
		CreatedAt     time.Time
	}

	// AuditLog определяет интерфейс для записи аудит-событий.
	AuditLog interface {
		// Save сохраняет аудит-запись в хранилище.
		Save(ctx context.Context, entry AuditEntry) error
	}

	// FeatureGate определяет интерфейс проверки доступности функций.
	FeatureGate interface {
		// Enabled возвращает true, если функция разрешена текущей лицензией.
		Enabled(feature Feature) bool
		// MaxWorkers возвращает лимит воркеров из текущей лицензии.
		MaxWorkers() int
		// MaxPlugins возвращает лимит плагинов из текущей лицензии.
		// -1 означает отсутствие ограничения.
		MaxPlugins() int
	}

	// Service defines the business logic interface used by the API layer.
	Service interface {
		Generate(ctx context.Context, req GenerateCodeRequest) (*GenerateCodeResponse, error)
		ListPlugins(ctx context.Context, filter PluginFilter) ([]PluginInfo, error)
		CreatePlugin(ctx context.Context, req CreatePluginRequest) (*PluginInfo, error)
		UpdatePlugin(ctx context.Context, req UpdatePluginRequest) (*PluginInfo, error)
		DeletePlugin(ctx context.Context, group, name, version string) error
	}

	// LicenseClaims holds the data returned by the license server.
	LicenseClaims struct {
		// Tier is the license level: "community" or "enterprise".
		Tier string
		// Features is the list of permitted features.
		Features []Feature
		// MaxWorkers is the maximum number of concurrent workers; -1 means unlimited.
		MaxWorkers int
		// MaxPlugins is the maximum number of registered plugins; -1 means unlimited.
		MaxPlugins int
	}

	// LicenseClient is the interface for communicating with the license server.
	LicenseClient interface {
		// ValidateLicense fetches and returns the current LicenseClaims.
		ValidateLicense(ctx context.Context) (LicenseClaims, error)
	}

	// BinaryStorage provides read access to plugin archives in remote object
	// storage. Archives are uploaded out-of-band (easyp-svc plugins push);
	// the service only downloads, inspects, and deletes them.
	BinaryStorage interface {
		// Download retrieves a plugin archive from remote storage and
		// atomically writes it to localPath.
		Download(ctx context.Context, key string, localPath string) error
		// Open returns a stream of the object under key and its size.
		// The caller must close the returned reader.
		Open(ctx context.Context, key string) (io.ReadCloser, int64, error)
		// Exists reports whether an object exists in remote storage.
		Exists(ctx context.Context, key string) (bool, error)
		// Delete removes an object from remote storage.
		Delete(ctx context.Context, key string) error
	}
)

const (
	LicenseTierCommunity  = "community"
	LicenseTierEnterprise = "enterprise"

	communityMaxWorkers = 4
	communityMaxPlugins = 10
)

// CommunityLicenseClaims returns the default LicenseClaims used in community mode
// (no license server configured, or when the server is unreachable).
func CommunityLicenseClaims() LicenseClaims {
	communityFeatures := []Feature{
		FeatureCodeGeneration,
		FeaturePluginListing,
		FeatureMCPServerTools,
		FeatureRateLimiting,
		FeaturePluginCRUD,
	}

	return LicenseClaims{
		Tier:       LicenseTierCommunity,
		Features:   communityFeatures,
		MaxWorkers: communityMaxWorkers,
		MaxPlugins: communityMaxPlugins,
	}
}
