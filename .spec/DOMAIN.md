<!-- generated: 2026-05-24, template: core.md -->
# Domain Model

## 1. Core Entities

### GenerateCodeRequest

```go
// GenerateCodeRequest represents an incoming request to generate code using a specific plugin.
type GenerateCodeRequest struct {
    PluginName string                          // Format: "{group}/{name}:{version}"
    Payload    *pluginpb.CodeGeneratorRequest  // Protobuf source files + parameters
}
```

### GenerateCodeResponse

```go
// GenerateCodeResponse wraps the response from a code generation operation.
type GenerateCodeResponse struct {
    Payload *pluginpb.CodeGeneratorResponse // Generated files
}
```

### PluginInfo

```go
// PluginInfo represents information about a plugin.
type PluginInfo struct {
    ID        uuid.UUID   // Unique identifier (UUIDv4)
    Group     string      // Plugin group (e.g., "protocolbuffers", "grpc")
    Name      string      // Plugin name (e.g., "go", "python")
    Version   string      // Semantic version (e.g., "v1.36.10") or "latest"
    Tags      []string    // Searchable tags
    CreatedAt time.Time   // Registration timestamp
}
```

### AuditEntry

```go
// AuditEntry represents a single audit log record.
type AuditEntry struct {
    ID            uuid.UUID       // Unique entry ID
    OperationType string          // GENERATE_CODE, LIST_PLUGINS, CREATE_PLUGIN, etc.
    PluginName    string          // Full plugin name (group/name:version)
    CallerAddress string          // Client IP address
    Status        string          // "success" or "error"
    ErrorCode     string          // Domain error code (NOT_FOUND, INTERNAL, etc.)
    ErrorMessage  string          // Human-readable error message
    DurationMs    int64           // Operation duration in milliseconds
    Metadata      map[string]any  // Additional context (file_count, plugin_count, etc.)
    CreatedAt     time.Time       // Entry creation timestamp
}
```

### LicenseClaims

```go
// LicenseClaims holds the data returned by the license server.
type LicenseClaims struct {
    Tier       string     // "community" or "enterprise"
    Features   []Feature  // List of permitted features
    MaxWorkers int        // Max concurrent workers; -1 = unlimited
    MaxPlugins int        // Max registered plugins; -1 = unlimited
}
```

### Filter Types

```go
// PluginFilter for listing plugins.
type PluginFilter struct {
    Group   string
    Name    string
    Version string
    Tags    []string
}

// CreatePluginRequest for registering a new plugin.
type CreatePluginRequest struct {
    Group   string
    Name    string
    Version string
    Config  json.RawMessage
    Tags    []string
}

// UpdatePluginRequest for modifying an existing plugin.
type UpdatePluginRequest struct {
    Group   string          // Immutable lookup key
    Name    string          // Immutable lookup key
    Version string          // Immutable lookup key
    Config  json.RawMessage
    Tags    []string
}
```

## 2. Enums / Value Objects

### Feature (iota enum)

```go
type Feature int

const (
    FeatureCodeGeneration  Feature = iota // Basic code generation
    FeaturePluginListing                  // Plugin listing
    FeatureMCPServerTools                 // MCP server tools
    FeatureRateLimiting                   // Rate limiting
    FeaturePluginCRUD                     // CRUD operations on plugins
    FeatureAudit                          // Audit logging (Enterprise)
)
```

**Community tier features:** CodeGeneration, PluginListing, MCPServerTools, RateLimiting, PluginCRUD

**Enterprise-only features:** Audit

Multi-tenancy and response caching used to be listed here as constants with
nothing behind them. They are in `.spec/ROADMAP.md` until they are code.

### License Tiers

```go
const (
    LicenseTierCommunity  = "community"
    LicenseTierEnterprise = "enterprise"
)
```

**Community defaults:** MaxWorkers=4, MaxPlugins=10

### Audit Operations

```go
const (
    OperationGenerateCode = "GENERATE_CODE"
    OperationListPlugins  = "LIST_PLUGINS"
    OperationCreatePlugin = "CREATE_PLUGIN"
    OperationUpdatePlugin = "UPDATE_PLUGIN"
    OperationDeletePlugin = "DELETE_PLUGIN"
)
```

### Audit Statuses

```go
const (
    AuditStatusSuccess = "success"
    AuditStatusError   = "error"
)
```

## 3. Business Errors

For the full business error catalog (codes, gRPC status mapping, retry policy) see `ERRORS.md`.

## 4. Key Interfaces

### Service (business logic contract)

```go
type Service interface {
    Generate(ctx context.Context, req GenerateCodeRequest) (*GenerateCodeResponse, error)
    ListPlugins(ctx context.Context, filter PluginFilter) ([]PluginInfo, error)
    CreatePlugin(ctx context.Context, req CreatePluginRequest) (*PluginInfo, error)
    UpdatePlugin(ctx context.Context, req UpdatePluginRequest) (*PluginInfo, error)
    DeletePlugin(ctx context.Context, group, name, version string) error
}
```

Implemented by: `core.Core`

### Registry (plugin storage + execution)

```go
type Registry interface {
    Get(ctx context.Context, pluginGroup, pluginName, pluginVersion string) (Plugin, error)
    List(ctx context.Context, filter PluginFilter) ([]PluginInfo, error)
    Create(ctx context.Context, req CreatePluginRequest) (*PluginInfo, error)
    Update(ctx context.Context, req UpdatePluginRequest) (*PluginInfo, error)
    Delete(ctx context.Context, group, name, version string) error
}
```

Implemented by: `adapters/registry`, `core.WorkerPool` (decorator), `telemetry.TracingRegistry` (decorator)

### Plugin (single plugin execution)

```go
type Plugin interface {
    Generate(ctx context.Context, req *pluginpb.CodeGeneratorRequest) (*pluginpb.CodeGeneratorResponse, error)
    Info(ctx context.Context) *PluginInfo
}
```

### FeatureGate (license-based feature access)

```go
type FeatureGate interface {
    Enabled(feature Feature) bool
    MaxWorkers() int
    MaxPlugins() int
}
```

Implemented by: `license.featureGate`

### Metrics (generation metrics)

```go
type Metrics interface {
    GenerateCode(ctx context.Context, info PluginInfo) error
    ObserveGenerationDuration(ctx context.Context, pluginName string, duration time.Duration)
    IncGenerationErrors(ctx context.Context, pluginName string, errorType string)
    IncGenerationRetries(ctx context.Context, pluginName string)
}
```

### AuditLog (audit persistence)

```go
type AuditLog interface {
    Save(ctx context.Context, entry AuditEntry) error
}
```

### LicenseClient (license server communication)

```go
type LicenseClient interface {
    ValidateLicense(ctx context.Context) (LicenseClaims, error)
}
```

## 5. Plugin Name Validation

Format: `{group}/{name}:{version}`

```
^[a-z][a-z0-9-]*/[a-z][a-z0-9-]*:(v\d+\.\d+\.\d+|latest)$
```

Examples: `protocolbuffers/go:v1.36.10`, `grpc/go:v1.5.1`, `grpc/go:latest`
