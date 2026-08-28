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
	// ErrInvalidConfig covers a plugin configuration the caller got wrong: no
	// command, an unparseable document, an executable outside plugins_dir.
	//
	// It exists so those reach the client as InvalidArgument. The registry
	// package has always had its own errors for this, but they wrapped nothing
	// from core, so errors.Is missed them in ErrorToStatus and every malformed
	// config — the most common mistake a client can make against CreatePlugin —
	// was reported as codes.Internal, i.e. as the server's fault. The proto's
	// own error table promised InvalidArgument the whole time.
	ErrInvalidConfig = errors.New("invalid plugin configuration")
)

// Feature identifies a service capability for licensing and feature-gating.
type Feature int

const (
	FeatureCodeGeneration Feature = iota // Базовая генерация кода
	FeaturePluginListing                 // Листинг плагинов
	FeatureMCPServerTools                // MCP server tools
	FeatureRateLimiting                  // Rate limiting
	FeaturePluginCRUD                    // CRUD операции с плагинами
	FeatureAudit                         // Аудит (Enterprise)
)

// featureNames содержит строковые представления Feature для метрик и логирования.
var featureNames = [...]string{
	FeatureCodeGeneration: "code_generation",
	FeaturePluginListing:  "plugin_listing",
	FeatureMCPServerTools: "mcp_server_tools",
	FeatureRateLimiting:   "rate_limiting",
	FeaturePluginCRUD:     "plugin_crud",
	FeatureAudit:          "audit",
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
		// IncOperation counts one completed operation by type and outcome.
		//
		// This is what the service did, not what reached the audit log: it is
		// recorded for every tier, including community, where audit is switched
		// off. How often generation fails is not a licensed feature.
		//
		// Both arguments are constants from this package — the Operation* and
		// AuditStatus* values above — so the label set stays bounded. Passing
		// anything derived from a request would make it unbounded.
		IncOperation(ctx context.Context, operation, status string)
	}

	// Registry provides access to available plugins.
	Registry interface {
		// Get retrieves a plugin by its identifier.
		// The pluginName parameter specifies the plugin to retrieve (e.g., "protobuf/go:v1.36.9").
		// Returns an error if the plugin is not found or cannot be loaded.
		Get(ctx context.Context, pluginGroup, pluginName, pluginVersion string) (Plugin, error)
		// List retrieves plugins matching the filter, sorted by
		// (group, name, version), at most page.Size of them, starting after
		// page.After. Callers pass page.Size already normalised.
		List(ctx context.Context, filter PluginFilter, page PluginPage) ([]PluginInfo, error)
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

	// PluginKey identifies one plugin in the listing order. The listing sorts
	// by (group, name, version) — the registry's uniqueness key — so a key
	// names an exact position to resume from, and stays valid when rows are
	// inserted or deleted around it, which an offset would not.
	PluginKey struct {
		Group   string
		Name    string
		Version string
	}

	// PluginPage bounds one page of a plugin listing.
	PluginPage struct {
		// Size is the number of entries the caller wants. Zero and anything
		// above MaxPageSize are normalised by ListPlugins, not rejected: the
		// caller cannot know the server's ceiling, so guessing too high should
		// cost precision, not an error.
		Size int
		// After resumes the listing after this key; nil starts from the top.
		After *PluginKey
	}

	// PluginList is one page of a plugin listing.
	PluginList struct {
		Plugins []PluginInfo
		// Next resumes the listing where this page ended; nil means this page
		// was the last one.
		Next *PluginKey
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

		// UpdateConfig and UpdateTags say which fields the caller asked to
		// replace, decoded from the request's update_mask. A mask that named
		// nothing sets both, which is what this operation did unconditionally
		// before the mask existed.
		//
		// Booleans rather than the FieldMask itself: core does not import the
		// generated API types, and "which of two fields" does not need a path
		// language to express.
		//
		// The distinction is not cosmetic. Config was validated before anything
		// else and an absent config was an empty one, so a caller wanting to
		// change a tag had to resend the plugin's whole command line — and a
		// mistake there points a registry entry at a different binary.
		UpdateConfig bool
		UpdateTags   bool
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
		// SaveBatch сохраняет группу аудит-записей за одну операцию.
		SaveBatch(ctx context.Context, entries []AuditEntry) error
	}

	// AuditSink принимает аудит-события от Core и отвечает за их доставку
	// в хранилище, включая учёт потерь.
	AuditSink interface {
		// Send передаёт событие на запись. Блокируется, пока событие не принято
		// или не отменён контекст.
		Send(ctx context.Context, entry AuditEntry)
		// Skipped отмечает событие, не отправленное из-за отсутствия лицензии.
		Skipped()
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
		ListPlugins(ctx context.Context, filter PluginFilter, page PluginPage) (PluginList, error)
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
		// ExpiresAt is when the licence stops being valid, zero in community mode.
		// Carried out of verification so that it can be exported as a metric: a
		// licence lapsing unnoticed downgrades the whole installation, and that
		// is worth alerting on well before it happens.
		ExpiresAt time.Time
		// InGrace reports that the licence has expired and the service is running
		// on the grace period the token granted. It is a distinct state from both
		// "valid" and "expired", and the only one with a deadline attached.
		InGrace bool
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

	// LicenseUnlimited is what MaxWorkers and MaxPlugins hold when the licence
	// imposes no ceiling of its own.
	LicenseUnlimited = -1
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

	// ExpiresAt stays zero: a community installation has no licence to expire,
	// and exporting a zero timestamp is what tells the alert rule to stay quiet.
	return LicenseClaims{
		Tier:       LicenseTierCommunity,
		Features:   communityFeatures,
		MaxWorkers: communityMaxWorkers,
		MaxPlugins: communityMaxPlugins,
	}
}

// EnterpriseLicenseClaims returns the LicenseClaims a valid Enterprise token
// grants, for a token expiring at expiresAt. inGrace says whether that moment
// has already passed and the service is running on the grace period.
//
// The token names a tier and nothing else: which features that tier unlocks is
// decided here, in the release, so that changing the offering does not mean
// reissuing every licence in the field.
func EnterpriseLicenseClaims(expiresAt time.Time, inGrace bool) LicenseClaims {
	enterpriseFeatures := []Feature{
		FeatureCodeGeneration,
		FeaturePluginListing,
		FeatureMCPServerTools,
		FeatureRateLimiting,
		FeaturePluginCRUD,
		FeatureAudit,
	}

	return LicenseClaims{
		Tier:       LicenseTierEnterprise,
		Features:   enterpriseFeatures,
		MaxWorkers: LicenseUnlimited,
		MaxPlugins: LicenseUnlimited,
		ExpiresAt:  expiresAt,
		InGrace:    inGrace,
	}
}
