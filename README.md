# EasyP API Service

A microservice for executing protobuf/gRPC code generation plugins running as Docker containers.

**Module:** `github.com/easyp-tech/service`

## Why EasyP API Service?

### The Problem: Plugin Management Chaos

Managing protobuf/gRPC code generation across development teams becomes increasingly complex as organizations scale:

**Version Inconsistencies**
- Developers use different plugin versions locally, causing build failures and inconsistent generated code
- "Works on my machine" syndrome when generated code differs between environments
- Manual coordination required to keep entire teams synchronized on plugin versions

**Operational Overhead**
- DevOps teams spend significant time managing plugin installations across developer machines
- Each new team member requires manual setup of correct plugin versions
- Plugin updates require coordinating with every developer individually
- No centralized control over which plugin versions are approved for use

**Security & Compliance Risks**
- Developers install plugins from various sources without security validation
- No audit trail of which plugins were used for which builds
- Difficult to enforce security policies on code generation tools

### The Solution: Centralized Plugin Execution

EasyP API Service eliminates these operational headaches by centralizing plugin management:

**🎯 Instant Version Control**
- Deploy new plugin versions to entire team instantly via stable tags (e.g., `grpc/go:stable`)
- Operations team controls plugin rollouts without touching developer machines
- Zero developer coordination required for plugin updates

**🔒 Security & Consistency**
- All plugins run in isolated Docker containers with security constraints
- Centralized approval process for new plugins
- Consistent execution environment regardless of developer's local setup

**⚡ Developer Experience**
- No local plugin installation or maintenance required
- Works identically across all environments (local, CI/CD, production)
- New team members productive immediately without plugin setup

## Overview

EasyP API Service provides centralized management and execution of protobuf/gRPC plugins as isolated Docker containers. The service accepts `google.protobuf.compiler.CodeGeneratorRequest` via gRPC API and returns generated code by executing plugins in a secure, isolated environment.

### Key Features

- 🐳 **Plugin isolation** in Docker containers
- 📦 **Self-hosted registry** for plugin Docker images  
- 🔄 **Plugin versioning** with "latest" support
- 📊 **Monitoring** with Prometheus and Grafana
- 🗄️ **Persistence** with PostgreSQL
- 🌐 **gRPC + HTTP** API
- 📈 **Health checks** and metrics

## Architecture

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   gRPC Client   │───▶│   API Service   │───▶│ Docker Registry │
└─────────────────┘    └─────────────────┘    └─────────────────┘
                               │
                               ▼
                       ┌─────────────────┐
                       │   PostgreSQL    │
                       └─────────────────┘
```

The service runs plugins as Docker containers, passing protobuf data through stdin/stdout.

## Project Structure

```
.
├── api/                                 # API contracts (protobuf)
│   ├── generator/v1/                   # Main code generation API
│   │   ├── generator.proto
│   │   ├── generator.pb.go
│   │   └── generator_grpc.pb.go
│   └── web/v1/                         # Web API for management
│       ├── web.proto
│       ├── web.pb.go
│       ├── web.pb.gw.go
│       └── web_grpc.pb.go
├── cmd/
│   └── main.go                         # Server entry point
├── internal/                           # Internal logic
│   ├── adapters/                       # External system adapters
│   │   ├── metrics/                    # Prometheus metrics collection
│   │   └── registry/                   # DB and Docker operations
│   ├── api/                           # Transport layer (gRPC)
│   ├── core/                          # Business logic
│   └── flags/                         # CLI flag processing
├── migrate/                           # SQL migrations
│   └── 1.init.sql
├── registry/                          # Plugin Dockerfiles
│   ├── protobuf/go/v1.36.10/
│   ├── grpc/go/v1.5.1/
│   ├── grpc-ecosystem/gateway/v2.27.3/
│   ├── grpc-ecosystem/openapiv2/v2.27.3/
│   └── community/pseudomuto-doc/v1.5.1/
├── docker/
│   └── Dockerfile                     # Service Dockerfile
├── infrastructure/                    # Monitoring configurations
│   ├── grafana/
│   ├── loki/
│   ├── prometheus/
│   └── promtail/
├── config.yml                        # Service configuration
├── docker-compose.yml               # Development infrastructure
├── easyp.yaml                       # easyp configuration
├── Taskfile.yml                     # Task automation
└── push.sh                          # Plugin build and push script
```

## Quick Start

### Prerequisites

- Docker and docker-compose
- [Task](https://taskfile.dev/) (optional)
- Go 1.24+ (for development)

### Running Infrastructure

```bash
# Start all services
task up

# Build and push plugins to local registry
task local-push-registry

# Full run with logs
task run
```

Or without Task:

```bash
# Start infrastructure
docker compose up -d

# Build plugins
./push.sh localhost:5005 --push

# View service logs
docker compose logs -f service
```

### Health Check

```bash
# Health check
curl http://localhost:8082/health

# Metrics
curl http://localhost:8081/metrics

# Grafana (admin/admin)
open http://localhost:3000
```

## API

### Generator API (Primary)

**Endpoint:** `localhost:8080` (gRPC)

```protobuf
service ServiceAPI {
  rpc GenerateCode(GenerateCodeRequest) returns (GenerateCodeResponse);
}

message GenerateCodeRequest {
  google.protobuf.compiler.CodeGeneratorRequest code_generator_request = 1;
  string plugin_name = 2;  // Format: "group/name:version"
}

message GenerateCodeResponse {
  google.protobuf.compiler.CodeGeneratorResponse code_generator_response = 1;
}
```

### Web API (Planned)

**Endpoint:** `localhost:8080` (gRPC) + HTTP Gateway

```protobuf
service ServiceAPI {
  rpc Plugins(PluginsRequest) returns (PluginsResponse) {
    option (google.api.http) = { get: "/v1/plugins" };
  };
}
```

## Plugin Naming Format

Plugins are identified in the format: `{group}/{name}:{version}`

### Examples:
- `protobuf/go:v1.36.10` - Go protobuf plugin
- `grpc/go:v1.5.1` - Go gRPC plugin  
- `grpc-ecosystem/gateway:v2.27.3` - gRPC Gateway
- `community/pseudomuto-doc:v1.5.1` - Documentation plugin
- `protobuf/go:latest` - Latest version of Go plugin

### Plugin Groups:
- `protobuf` - Core protobuf plugins
- `grpc` - gRPC plugins 
- `grpc-ecosystem` - gRPC ecosystem plugins
- `community` - Community plugins

## Configuration

### Environment Variables

```yaml
# Server
SERVER_HOST=0.0.0.0
SERVER_PORT_GRPC=8080
SERVER_PORT_METRIC=8081  
SERVER_PORT_HEALTH=8082

# Database
DB_POSTGRES_DSN="postgres://user:pass@localhost/db"
DB_MIGRATE_DIR="migrate"

# Docker Registry
REGISTRY_DOMAIN="localhost:5005"
```

### Configuration File

```yaml
server:
  host: "0.0.0.0"
  port:
    grpc: 8080
    metric: 8081
    health: 8082
db:
  migrate_dir: "migrate"
  driver: "postgres"
  postgres: "postgres://easyp_svc:easyp_pass@postgres:5432/easyp_db?sslmode=disable"
registry:
  domain: "localhost:5005"
```

## Contributing Plugins

We welcome contributions of new plugins! Here's how to add your plugin to the registry:

### 1. Fork and Create Plugin Structure

```bash
# Fork the repository
git fork https://github.com/easyp-tech/easyp-api-service

# Clone your fork
git clone https://github.com/YOUR_USERNAME/easyp-api-service
cd easyp-api-service

# Create plugin directory structure
mkdir -p registry/{group}/{plugin-name}/{version}
cd registry/{group}/{plugin-name}/{version}
```

### 2. Create Dockerfile

Your plugin must be packaged as a Docker image that:
- Reads protobuf `CodeGeneratorRequest` from stdin
- Writes protobuf `CodeGeneratorResponse` to stdout
- Runs as a non-root user for security
- Is optimized for size (use multi-stage builds)

#### Example: Go-based Plugin

```dockerfile
FROM --platform=$BUILDPLATFORM golang:1.25-alpine3.22 AS build

ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

# Install upx for binary compression (optional but recommended)
RUN apk add upx=5.0.2-r0 --no-cache

# Install your protoc plugin
RUN --mount=type=cache,target=/go/pkg/mod \
    go install -ldflags "-s -w" -trimpath example.com/protoc-gen-yourplugin@v1.0.0 \
 && mv /go/bin/${GOOS}_${GOARCH}/protoc-gen-yourplugin /go/bin/protoc-gen-yourplugin || true \
 && upx --best --lzma /go/bin/protoc-gen-yourplugin

FROM scratch

# Copy essential files for non-root user
COPY --from=build --link /etc/passwd /etc/passwd
COPY --from=build --link --chown=root:root /go/bin/protoc-gen-yourplugin /protoc-gen-yourplugin

# Run as non-root user
USER nobody

ENTRYPOINT ["/protoc-gen-yourplugin"]
```

#### Example: Python-based Plugin

```dockerfile
FROM python:3.11-alpine AS build

# Install your plugin
RUN pip install --no-cache-dir yourplugin==1.0.0

FROM python:3.11-alpine

# Copy installed packages
COPY --from=build /usr/local/lib/python3.11/site-packages /usr/local/lib/python3.11/site-packages
COPY --from=build /usr/local/bin/protoc-gen-yourplugin /usr/local/bin/

# Create non-root user
RUN adduser -D -s /bin/sh plugin
USER plugin

ENTRYPOINT ["/usr/local/bin/protoc-gen-yourplugin"]
```

#### Example: Node.js-based Plugin

```dockerfile
FROM node:18-alpine AS build

WORKDIR /app
RUN npm install -g yourplugin@1.0.0

FROM node:18-alpine

# Copy global node modules
COPY --from=build /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=build /usr/local/bin /usr/local/bin

# Create non-root user
RUN adduser -D -s /bin/sh plugin
USER plugin

ENTRYPOINT ["protoc-gen-yourplugin"]
```

### 3. Test Your Plugin Locally

```bash
# Build the plugin image
docker build -t localhost:5005/{group}/{plugin-name}:{version} .

# Test with sample protobuf request
echo "your_protobuf_request_binary_data" | \
  docker run --rm -i localhost:5005/{group}/{plugin-name}:{version}
```

### 4. Add Database Entry

Create a migration file or add to the existing migration:

```sql
-- Add your plugin to the database
INSERT INTO plugins (group_name, name, version, created_at)
VALUES ('{group}', '{plugin-name}', '{version}', now());
```

### 5. Update Documentation

Add your plugin to this README:

```markdown
### Available Plugins

- `{group}/{plugin-name}:{version}` - Description of your plugin
```

### 6. Submit Pull Request

```bash
# Commit your changes
git add registry/{group}/{plugin-name}/
git commit -m "Add {group}/{plugin-name}:{version} plugin"

# Push to your fork
git push origin main

# Create pull request
# Include description of what your plugin does and how to use it
```

### Plugin Requirements

**Security:**
- ✅ Must run as non-root user
- ✅ No network access required (use `--network=none`)
- ✅ Limited memory (128MB max)
- ✅ Limited CPU (1 core max)
- ✅ Stateless execution

**Performance:**
- ✅ Fast startup (< 5 seconds)
- ✅ Small image size (< 100MB preferred)
- ✅ Efficient memory usage

**Compatibility:**
- ✅ Supports standard protoc plugin protocol
- ✅ Reads from stdin, writes to stdout
- ✅ Returns proper exit codes
- ✅ Works with linux/amd64 architecture

### Plugin Groups Guidelines

**`protobuf`** - Core Protocol Buffers plugins
- Official protoc plugins (protoc-gen-go, protoc-gen-cpp, etc.)
- Language-specific protobuf generators

**`grpc`** - gRPC framework plugins  
- Official gRPC plugins (protoc-gen-go-grpc, etc.)
- gRPC service generators

**`grpc-ecosystem`** - gRPC ecosystem tools
- grpc-gateway, grpc-web, openapi generators
- Authentication, validation tools

**`community`** - Community-maintained plugins
- Documentation generators
- Custom validation tools
- Framework-specific generators

### Plugin Testing

We provide testing tools to validate your plugin:

```bash
# Test plugin compatibility
./scripts/test-plugin.sh {group}/{plugin-name}:{version}

# Validate plugin security
./scripts/security-scan.sh {group}/{plugin-name}:{version}

# Performance benchmarks
./scripts/benchmark-plugin.sh {group}/{plugin-name}:{version}
```

## Development

### Adding New Plugin (Development)

For local development without PR:

```bash
# Create plugin structure
mkdir -p registry/{group}/{name}/{version}

# Create Dockerfile
# ... (see examples above)

# Add to database
docker exec -it easyp-postgres psql -U easyp_svc -d easyp_db \
  -c "INSERT INTO plugins (group_name, name, version) VALUES ('{group}', '{name}', '{version}');"

# Build and push
./push.sh localhost:5005 --push
```

### Generating Protobuf Code

```bash
# Generate from proto files
easyp generate
```

### Building Service

```bash
# Local build
go build -o bin/server ./cmd/main.go

# Docker build
docker build -f docker/Dockerfile -t easyp-api-service .

# Run
./bin/server -cfg config.yml -log_level debug
```

## Monitoring

### Available Services

| Service | URL | Description |
|---------|-----|-------------|
| Grafana | http://localhost:3000 | Dashboards (admin/admin) |
| Prometheus | http://localhost:9090 | Metrics |
| Health | http://localhost:8082 | Health checks |
| Metrics | http://localhost:8081 | Prometheus metrics |

### Key Metrics

- `grpc_server_handled_total` - gRPC request count
- `plugin_generation_total` - Plugin generation count by plugin
- `plugin_generation_duration_seconds` - Plugin execution time
- `postgres_queries_total` - Database query count

## Client Usage

### Go Client

```go
import (
    "github.com/easyp-tech/service/api/generator/v1"
    "google.golang.org/grpc"
)

// Connect
conn, err := grpc.Dial("localhost:8080", grpc.WithInsecure())
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

client := generator.NewServiceAPIClient(conn)

// Generate code
response, err := client.GenerateCode(ctx, &generator.GenerateCodeRequest{
    CodeGeneratorRequest: codeGenRequest,
    PluginName:          "protobuf/go:v1.36.10",
})
```

### CLI Usage with easyp

```yaml
# easyp.yaml
generate:
  plugins:
    - remote: "localhost:8080/protobuf/go:latest"
      out: .
      opts:
        paths: source_relative
    - remote: "localhost:8080/grpc/go:v1.5.1"  
      out: .
      opts:
        paths: source_relative
```

## Management Commands

```bash
# Start infrastructure
task up

# Stop with cleanup
task down

# Full development cycle  
task run

# Build plugins
task local-push-registry

# Manual plugin build
./push.sh localhost:5005 --push

# View images
docker images | grep localhost:5005
```

## Troubleshooting

### Docker Issues

```bash
# Check Docker network
docker network ls

# Check running containers  
docker ps

# Service logs
docker compose logs service

# Restart with rebuild
task down && task up
```

### Plugin Issues

```bash
# Check available plugins in registry
curl -s http://localhost:5005/v2/_catalog

# Check plugin versions
curl -s http://localhost:5005/v2/protobuf/go/tags/list

# Manual plugin execution
docker run --rm -i localhost:5005/protobuf/go:v1.36.10 < request.bin
```

### Database Issues

```bash
# Connect to PostgreSQL
docker exec -it easyp-postgres psql -U easyp_svc -d easyp_db

# Check plugins in database
SELECT * FROM plugins;

# Check schema
\d plugins
```

## Roadmap

### Planned Features

- [ ] Implementation of Web API for plugin management
- [ ] Web interface for plugin management
- [ ] Result caching  
- [ ] Automatic plugin updates
- [ ] Private registry support
- [ ] Rate limiting and quotas
- [ ] Audit logging

### Architectural Improvements

- [ ] Migration to Clean Architecture
- [ ] Integration tests
- [ ] CI/CD pipeline setup
- [ ] Kubernetes manifests
- [ ] Helm charts

## Available Plugins

### Core Plugins
- `protobuf/go:v1.36.10` - Go Protocol Buffers compiler
- `grpc/go:v1.5.1` - Go gRPC compiler

### Ecosystem Plugins  
- `grpc-ecosystem/gateway:v2.27.3` - gRPC-Gateway HTTP transcoding
- `grpc-ecosystem/openapiv2:v2.27.3` - OpenAPI v2 documentation generator

### Community Plugins
- `community/pseudomuto-doc:v1.5.1` - Protocol documentation generator

## License

This project is developed by the EasyP Tech team.

## Support

For questions and suggestions, please create Issues in the repository.