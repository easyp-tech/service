<!-- generated: 2026-05-24, template: development.md -->
# Tools

Reference of commands and project tools.

## 0. Dev Environment Setup

### Prerequisites

| Tool | Version | Install |
|------|---------|---------
| Go | 1.26+ | `brew install go` |
| Docker + Compose | latest | `brew install --cask docker` |
| Task | v3 | `brew install go-task` |
| golangci-lint | v2 | `brew install golangci-lint` |
| easyp | latest | `brew install easyp-tech/tap/easyp` |
| grpcurl | latest | `brew install grpcurl` |

### First Run

1. Clone: `git clone github.com/easyp-tech/service && cd service`
2. Build plugin binaries: `task build-plugins`
3. Start full stack: `task up`
4. Wait for healthy: `curl http://localhost:8082/health`
5. Register plugins: `task register-plugins`
6. Verify: `curl http://localhost:8081/metrics`

### Minimal Local Development

1. Start postgres: `task up-minimal` (port 5433)
2. Build plugins: `task build-plugins`
3. Run from source: `task run-local` (uses `deploy/config/config.local.yml`)
4. Register plugins: `./register-plugins.sh localhost:8080`
5. Generate code: `easyp --cfg easyp.local.yaml generate`

## 1. Overview

All commands are run via `task` (Taskfile v3). For Go testing and linting, use standard Go tools directly.

## 2. Quick Reference

| Action | Command |
|--------|---------|
| Start full stack | `task up` |
| Start minimal stack | `task up-minimal` |
| Stop all | `task down` |
| Run from source | `task run-local` |
| Full cycle (build→up→register→logs) | `task run` |
| Setup (build→up→register, no logs) | `task setup` |
| Build plugin binaries | `task build-plugins` |
| Register plugins via gRPC | `task register-plugins` |
| Run tests | `go test ./...` |
| Lint | `golangci-lint run ./...` |
| Generate protobuf (docker-compose) | `task generate` |
| Generate protobuf (local) | `task generate-local` |
| Test MCP server | `task test-mcp` |
| Smoke test MCP | `task smoke-mcp` |

## 3. Detailed Command Groups

### Infrastructure

```bash
# Full dev stack (postgres, grafana, loki, alloy, tempo, mimir, pyroscope, traefik, rustfs)
task up

# Minimal: postgres (port 5433) only
task up-minimal

# Stop everything, remove volumes
task down
```

### Running

```bash
# Full cycle: build-plugins → stop → start → register → tail logs
task run

# Setup only (no log tailing)
task setup

# Run from source against minimal stack (uses config.local.yml)
task run-local
# Equivalent to: go run ./cmd/main.go -cfg deploy/config/config.local.yml -log_level debug
```

### Plugin Management

```bash
# Build all plugin binaries from registry/ Dockerfiles
# Extracts static binaries to plugins/{group}/{name}/{version}/plugin
task build-plugins
# Uses: go run ./cmd/easyp-svc/ plugins build registry --output plugins
# Flags: --filter <glob>, --parallel <n>, --force, --dry-run, --non-interactive

# Register all built plugins via gRPC CreatePlugin API
# Requires a running service (no external tools needed)
task register-plugins
# Uses: go run ./cmd/easyp-svc/ plugins migrate plugins/ --addr localhost:8080
```

### Testing

```bash
# Unit tests
go test ./...

# With race detector
go test -race ./...

# Specific package
go test ./internal/core/...

# MCP server tests
task test-mcp
# Equivalent to: go test ./internal/mcpserver -run TestMCPServer -count=1

# MCP smoke test (requires running service)
task smoke-mcp
```

### Linting

```bash
# Run all linters
golangci-lint run ./...

# With auto-fix
golangci-lint run --fix ./...
```

## 4. Code Generation

### Protobuf

```bash
# Generate *.pb.go, *_grpc.pb.go, *.mcp.go (against docker-compose service)
task generate
# Equivalent to: easyp --cfg easyp.yaml generate

# Generate against locally running service
task generate-local
# Equivalent to: easyp --cfg easyp.local.yaml generate

# Lint proto files
easyp lint

# Validate config
easyp validate-config
```

Config files:
- `easyp.yaml` — main config (for docker-compose service)
- `easyp.local.yaml` — local development config

Generated files:
- `api/generator/v1/generator.pb.go` — protobuf types
- `api/generator/v1/generator_grpc.pb.go` — gRPC stubs
- `api/generator/v1/generator.mcp.go` — MCP tool bindings

## 5. CI/CD Cheatsheet

```bash
# Simulate CI locally:
golangci-lint run ./...          # Lint
go test -race ./...              # Tests with race detector
easyp lint                       # Proto lint
easyp --cfg easyp.yaml generate  # Ensure codegen is up to date
```

## 6. Tool Installation

| Tool | Install Command |
|------|----------------|
| Go | `brew install go` |
| Docker Desktop | `brew install --cask docker` |
| Task | `brew install go-task` |
| golangci-lint | `brew install golangci-lint` |
| easyp | `brew install easyp-tech/tap/easyp` |
| grpcurl | `brew install grpcurl` |
| GoReleaser | `brew install goreleaser` |
