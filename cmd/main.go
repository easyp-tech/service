package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hellofresh/health-go/v5"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sethvargo/go-envconfig"
	"github.com/sipki-tech/dev-platform/database/connectors"
	"github.com/sipki-tech/dev-platform/grpc_helper"
	"github.com/sipki-tech/dev-platform/logger"
	"github.com/sipki-tech/dev-platform/metrics"
	"github.com/sipki-tech/dev-platform/serve"
	"github.com/sipki-tech/dev-platform/version"
	"google.golang.org/grpc/grpclog"
	"gopkg.in/yaml.v3"

	adapter_metrics "github.com/easyp-tech/service/internal/adapters/metrics"
	"github.com/easyp-tech/service/internal/adapters/registry"
	"github.com/easyp-tech/service/internal/api"
	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/flags"
)

const (
	exitCode       = 2
	configFileSize = 1024 * 1024
)

type (
	config struct {
		Server server   `yaml:"server" env:", prefix=SERVER_"`
		DB     dbConfig `yaml:"db" env:", prefix=DB_"`
	}
	server struct {
		Host string `yaml:"host" env:"HOST, default=0.0.0.0"`
		Port ports  `yaml:"port" env:", prefix=PORT_"`
	}
	ports struct {
		GRPC   uint16 `yaml:"grpc" env:"GRPC, default=23410"`
		Metric uint16 `yaml:"metric" env:"METRIC, default=23411"`
		Health uint16 `yaml:"health" env:"HEALTH, default=23412"`
	}
	dbConfig struct {
		MigrateDir string `yaml:"migrate_dir" env:"MIGRATE_DIR, default=migrate"`
		Driver     string `yaml:"driver" env:"DRIVER, default=postgres"`
		Postgres   string `yaml:"postgres" env:"POSTGRES_DSN"`
	}
)

var (
	cfgFile  = &flags.File{DefaultPath: "", MaxSize: configFileSize}
	logLevel = &flags.Level{Level: slog.LevelDebug}
)

func main() {
	flag.Var(cfgFile, "cfg", "path to config file")
	flag.Var(logLevel, "log_level", "log level")
	flag.Parse()

	log := buildLogger(logLevel.Level)
	grpclog.SetLoggerV2(grpc_helper.NewLogger(log))

	appName := filepath.Base(os.Args[0])
	ctxParent := logger.NewContext(context.Background(), log.With(slog.String(logger.Version.String(), version.System())))
	ctx, cancel := signal.NotifyContext(ctxParent, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGTERM)
	defer cancel()
	go forceShutdown(ctx)

	err := start(ctx, cfgFile, appName)
	if err != nil {
		log.Error("shutdown",
			slog.String(logger.Error.String(), err.Error()),
		)
		os.Exit(exitCode)
	}
}

func start(ctx context.Context, cfgFile *flags.File, appName string) error {
	cfg := config{}

	if !cfgFile.IsNil() {
		err := yaml.NewDecoder(cfgFile).Decode(&cfg)
		if err != nil {
			return fmt.Errorf("yaml.NewDecoder.Decode: %w", err)
		}
	} else {
		err := envconfig.Process(ctx, &cfg)
		if err != nil {
			return fmt.Errorf("envconfig.Process: %w", err)
		}
	}

	reg := prometheus.NewPedanticRegistry()

	return run(ctx, cfg, reg, appName)
}

func run(ctx context.Context, cfg config, reg *prometheus.Registry, namespace string) error {
	log := logger.FromContext(ctx)
	m := metrics.New(reg, namespace)

	r, err := registry.New(ctx, reg, namespace, registry.Config{
		Postgres: connectors.Raw{
			Query: cfg.DB.Postgres,
		},
		MigrateDir: cfg.DB.MigrateDir,
		Driver:     cfg.DB.Driver,
	})
	if err != nil {
		return fmt.Errorf("repo.New: %w", err)
	}

	defer func() {
		err := r.Close()
		if err != nil {
			log.Error("close database connection", slog.String(logger.Error.String(), err.Error()))
		}
	}()

	module := core.New(adapter_metrics.NoMetrics{}, r)

	grpcAPI := api.New(ctx, m, module, reg, namespace)

	const healthTimeout = 1 * time.Second

	// add some checks on instance creation
	h, err := health.New(
		health.WithComponent(
			health.Component{
				Name:    namespace,
				Version: version.System(),
			},
		),
		health.WithChecks(
			health.Config{
				Name:    "postgres",
				Timeout: healthTimeout,
				Check:   r.Health,
			},
		),
	)
	if err != nil {
		return fmt.Errorf("health.New: %w", err)
	}

	return serve.Start(
		ctx,
		serve.Metrics(log.With(slog.String(logger.Module.String(), "metric")), cfg.Server.Host, cfg.Server.Port.Metric, reg),
		serve.GRPC(log.With(slog.String(logger.Module.String(), "gRPC")), cfg.Server.Host, cfg.Server.Port.GRPC, grpcAPI),
		serve.HTTP(log.With(slog.String(logger.Module.String(), "health")), cfg.Server.Host, cfg.Server.Port.Health, h.Handler()),
	)
}

func buildLogger(level slog.Level) *slog.Logger {
	return slog.New(
		slog.NewJSONHandler(
			os.Stdout,
			&slog.HandlerOptions{ //nolint:exhaustruct
				AddSource: true,
				Level:     level,
			},
		),
	)
}

func forceShutdown(ctx context.Context) {
	log := logger.FromContext(ctx)
	const shutdownDelay = 15 * time.Second

	<-ctx.Done()
	time.Sleep(shutdownDelay)

	log.Error("failed to graceful shutdown")
	os.Exit(exitCode)
}
