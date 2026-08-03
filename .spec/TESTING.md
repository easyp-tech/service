<!-- generated: 2026-05-24, template: development.md -->
# Testing

Project testing conventions for EasyP Service.

## 1. Test Package Naming

Tests use the **same package** (internal tests) — not `_test` suffix packages. This allows access to unexported symbols.

```go
// internal/core/crud_test.go
package core  // same package as the code being tested
```

## 2. Test File Structure

Typical test from the project (`internal/core/crud_test.go`):

```go
package core

import (
    "context"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestCreatePlugin(t *testing.T) {
    tests := []struct {
        name    string
        req     CreatePluginRequest
        setup   func(*mockRegistry, *mockFeatureGate)
        wantErr error
    }{
        {
            name: "success",
            req: CreatePluginRequest{
                Group:   "grpc",
                Name:    "go",
                Version: "v1.5.1",
            },
            setup: func(r *mockRegistry, fg *mockFeatureGate) {
                fg.enabled = true
                fg.maxPlugins = -1
                r.createResult = &PluginInfo{...}
            },
        },
        {
            name:    "feature denied",
            req:     CreatePluginRequest{Group: "grpc", Name: "go", Version: "v1.0.0"},
            setup:   func(r *mockRegistry, fg *mockFeatureGate) { fg.enabled = false },
            wantErr: ErrFeatureDenied,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // setup mocks
            reg := &mockRegistry{}
            fg := &mockFeatureGate{}
            tt.setup(reg, fg)

            c := New(nil, reg, fg, make(chan<- AuditEntry, 100), slog.Default())
            _, err := c.CreatePlugin(context.Background(), tt.req)

            if tt.wantErr != nil {
                require.ErrorIs(t, err, tt.wantErr)
            } else {
                require.NoError(t, err)
            }
        })
    }
}
```

## 3. Key Patterns

### Table-Driven Tests (slice of structs)

All tests use slice-of-structs pattern with `t.Run`:

```go
tests := []struct {
    name    string
    // inputs
    wantErr error
}{...}

for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        // test body
    })
}
```

### Parallel Tests

All table-driven tests MUST use `t.Parallel()` at two levels:

```go
func TestXxx(t *testing.T) {
    t.Parallel() // top-level: run alongside other Test* functions

    tests := []struct {
        name string
        // ...
    }{...}

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel() // sub-test: run cases concurrently

            // Create fresh dependencies per sub-test (no shared mutable state)
            // ...
        })
    }
}
```

**Rules:**
- Every table-driven test has `t.Parallel()` at top-level and inside each `t.Run`
- Each sub-test creates its own mocks/dependencies (no shared mutable state)
- If a test cannot be parallel, it indicates shared state — refactor the code or test
- Enforced by `paralleltest` and `tparallel` linters

### Inline Mocks

Mocks are defined as simple structs in test files — no code generation:

```go
type mockRegistry struct {
    getResult    Plugin
    getErr       error
    listResult   []PluginInfo
    createResult *PluginInfo
    // ...
}

func (m *mockRegistry) Get(ctx context.Context, group, name, version string) (Plugin, error) {
    return m.getResult, m.getErr
}
```

### Assertion Library

Uses `testify` (`assert` and `require`):

```go
require.NoError(t, err)
require.ErrorIs(t, err, ErrNotFound)
assert.Equal(t, expected, actual)
assert.Len(t, plugins, 3)
```

## 4. Mock Generation

**No generated mocks.** All mocks are hand-written structs in test files.

Pattern: mock struct with fields for return values, methods return those fields.

Location: same `_test.go` file that uses them.

## 5. Integration Tests

No separate integration test infrastructure. The project uses:
- `docker-compose` for full stack testing
- `task smoke-mcp` for MCP end-to-end smoke tests
- Standard `go test` for unit tests

## 6. Relaxed Linter Rules in Tests

Tests have relaxed linter rules (`.golangci.yml`):
- Disabled: `dupl`, `errcheck`, `funlen`, `gocyclo`, `goconst`, `gosec`, `lll`, `varnamelen`, `testpackage`, `paralleltest`, `err113`
- This allows more verbose test code without linter noise

## 7. Commands

```bash
# Unit tests
go test ./...

# With race detector
go test -race ./...

# Specific package
go test ./internal/core/...

# With verbose output
go test -v ./...

# With coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# MCP server tests
task test-mcp

# MCP smoke test (requires running service)
task smoke-mcp
```

## Integration tests

`test/integration/` runs against a real PostgreSQL and a real plugin process.
It is behind the `integration` build tag, so `go test ./...` stays hermetic.

```bash
docker run -d --name easyp-it -e POSTGRES_PASSWORD=pg -e POSTGRES_DB=easyp \
  -p 5439:5432 postgres:16-alpine

EASYP_TEST_DSN='postgres://postgres:pg@localhost:5439/easyp?sslmode=disable' \
  go test -tags integration ./test/...
```

Without `EASYP_TEST_DSN` the tests skip rather than fail. CI supplies one from a
service container.

These exist because the defects that got through were not the kind unit tests
catch. Writing this suite immediately surfaced three more: `Core.CreatePlugin`
dereferenced a nil feature gate that its own sibling `checkFeature` documented as
supported, `sendAudit` panicked on a nil audit sink, and a `WorkerPool` whose
`Start` is never called blocks forever instead of failing. None of those are
reachable through the production wiring today — which is exactly why nothing
else found them.

`msgsize_test.go` is the one case that needs the whole stack rather than the
harness: it stands the production gRPC server up over TCP and talks to it with
the production SDK client, because the limits it checks live in
`grpchelper.NewServer` and in the SDK's dial options. A hand-rolled client or a
bare `grpc.NewServer` would exercise neither, and the defect it covers — 4 MiB
transport caps under a 64 MiB configured output limit — only shows up where both
ends are the real ones.

The stub plugin (`test/integration/stubplugin/`) is a compiled program, not a
script echoing fixed bytes: the point is the process boundary — marshal, hand
over stdin, read back from stdout, unmarshal — and a script exercises none of it.
It carries `//go:build ignore` so it stays out of `go build ./...`, and the test
builds it by file path, which ignores the tag while still resolving imports
against this module. Building it outside the module tree makes the compiler go
looking for dependencies on the network, and the test hangs rather than fails.
