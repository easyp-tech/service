<!-- generated: 2026-05-24, template: feature-flags.md -->
# Feature Flags

Feature gating system for EasyP Service.

## Overview

EasyP uses a **FeatureGate** pattern for two-tier licensing. Features are enabled/disabled based on the current license tier (community vs enterprise) without hard coupling between components.

## Features

```go
type Feature int

const (
    FeatureCodeGeneration  Feature = iota // Basic code generation
    FeaturePluginListing                  // Plugin listing
    FeatureMCPServerTools                 // MCP server tools
    FeatureRateLimiting                   // Rate limiting
    FeaturePluginCRUD                     // CRUD operations on plugins
    FeatureAudit                          // Audit logging (Enterprise only)
)
```

## Tier Matrix

| Feature | Community | Enterprise |
|---------|:---------:|:----------:|
| CodeGeneration | ✅ | ✅ |
| PluginListing | ✅ | ✅ |
| MCPServerTools | ✅ | ✅ |
| RateLimiting | ✅ | ✅ |
| PluginCRUD | ✅ | ✅ |
| Audit | ❌ | ✅ |

Audit is the only feature the tier actually gates. What separates the two tiers
today is that plus the resource limits below — worth knowing before quoting
anyone a price.

`LicenseInterceptor` maps gRPC methods to features and its map is empty, because
no method is Enterprise-only. It stays wired so that gating a method later is a
map entry rather than a rewrite.

## Resource Limits

| Resource | Community | Enterprise |
|----------|-----------|-----------|
| MaxWorkers | 4 | Configurable |
| MaxPlugins | 10 | Unlimited (-1) |

## FeatureGate Interface

```go
type FeatureGate interface {
    Enabled(feature Feature) bool
    MaxWorkers() int
    MaxPlugins() int  // -1 means unlimited
}
```

## Usage Points

| Component | Check | Purpose |
|-----------|-------|---------|
| `Core.checkFeature()` | `Enabled(FeaturePluginCRUD)` | Before Create/Update/Delete operations |
| `Core.CreatePlugin()` | `MaxPlugins()` | Enforce plugin count limit |
| `WorkerPool` | `MaxWorkers()` | Override worker count from license |
| `RateLimiter` | `FeatureGate` integration | Tier-based rate limits |
| `LicenseInterceptor` | gRPC middleware | Feature check before request processing |
