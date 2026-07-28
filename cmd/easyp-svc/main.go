package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/easyp-tech/service/internal/adapters/storage"
)

func main() {
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGABRT,
		syscall.SIGTERM,
	)
	defer cancel()

	go forceShutdown(ctx)

	app := &cli.Command{
		Name:     "easyp-svc",
		Usage:    "EasyP Service CLI",
		Commands: getCommands(),
	}

	err := app.Run(ctx, os.Args)
	if err != nil {
		slog.Error("Failed to run app", "error", err)
		os.Exit(1)
	}
}

func getCommands() []*cli.Command {
	return []*cli.Command{
		getServiceCommand(),
		getPluginsCommand(),
	}
}

func getServiceCommand() *cli.Command {
	return &cli.Command{
		Name:  "service",
		Usage: "Manage the easyp service",
		Commands: []*cli.Command{
			{
				Name:  "start",
				Usage: "Start the gRPC/MCP service",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "cfg",
						Usage: "path to config file",
						Value: "",
					},
					&cli.StringFlag{
						Name:  "log_level",
						Usage: "log level (debug, info, warn, error)",
						Value: "debug",
					},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfgPath := cmd.String("cfg")
					logLvl := cmd.String("log_level")

					_, _ = fmt.Fprintf(os.Stdout, "Starting easyp-svc with config: %q, log level: %q\n", cfgPath, logLvl)

					return runServiceStart(ctx, cfgPath, logLvl)
				},
			},
		},
	}
}

func getPluginsCommand() *cli.Command {
	return &cli.Command{
		Name:  "plugins",
		Usage: "Manage plugins",
		Commands: []*cli.Command{
			getPluginsBuildCommand(),
			getPluginsPackCommand(),
			getPluginsPushCommand(),
			getPluginsRegisterCommand(),
		},
	}
}

func getPluginsPackCommand() *cli.Command {
	return &cli.Command{
		Name:  "pack",
		Usage: "Pack built plugin version directories into tar.gz archives on disk",
		Description: "Writes each plugin version directory to {out}/{group}/{name}/{version}/plugin.tgz. " +
			"The layout mirrors the S3 object keys used by `plugins push`, so a packed tree can be " +
			"uploaded as-is later. Existing archives are skipped unless --force is set.",
		ArgsUsage: "[path]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "out",
				Usage:    "directory to write archives to",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "filter",
				Usage: "glob filter pattern for plugins (e.g. 'protocolbuffers/*' or 'grpc/go:v1.6.2')",
				Value: "",
			},
			&cli.BoolFlag{
				Name:  "force",
				Usage: "re-pack even if the archive already exists",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "print the pack plan without writing archives",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "non-interactive",
				Usage: "disable interactive UI and dynamic progress bars",
				Value: false,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			scanPath := defaultPluginsScanPath
			if args := cmd.Args().Slice(); len(args) >= 1 {
				scanPath = args[0]
			}

			return runPluginsPack(ctx, packOptions{
				scanPath:       scanPath,
				outDir:         cmd.String("out"),
				filter:         cmd.String("filter"),
				force:          cmd.Bool("force"),
				dryRun:         cmd.Bool("dry-run"),
				nonInteractive: cmd.Bool("non-interactive"),
			})
		},
	}
}

func getPluginsPushCommand() *cli.Command {
	return &cli.Command{
		Name:  "push",
		Usage: "Upload built plugin archives to S3 binary storage",
		Description: "Packs each plugin version directory into a tar.gz and uploads it to " +
			"{group}/{name}/{version}/plugin.tgz. Run before `plugins register`: the service " +
			"records the archive checksum at registration time. Re-pushing an already " +
			"registered plugin with --force invalidates its recorded checksum — re-register it.",
		ArgsUsage: "[path]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "cfg",
				Usage: "path to service config YAML; registry.s3 is used for settings not passed as flags",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "bucket",
				Usage: "S3 bucket name (overrides registry.s3.bucket from --cfg)",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "endpoint",
				Usage: "S3 endpoint URL, e.g. http://localhost:9000 for MinIO/RustFS",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "region",
				Usage: "S3 region",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "prefix",
				Usage: "S3 key prefix",
				Value: "",
			},
			&cli.BoolFlag{
				Name:  "force-path-style",
				Usage: "use path-style addressing (required by MinIO/RustFS)",
				Value: false,
			},
			&cli.StringFlag{
				Name:  "filter",
				Usage: "glob filter pattern for plugins (e.g. 'protocolbuffers/*' or 'grpc/go:v1.6.2')",
				Value: "",
			},
			&cli.BoolFlag{
				Name:  "force",
				Usage: "re-upload even if the archive already exists in storage",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "print the upload plan without contacting storage",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "non-interactive",
				Usage: "disable interactive UI and dynamic progress bars",
				Value: false,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			scanPath := defaultPluginsScanPath
			if args := cmd.Args().Slice(); len(args) >= 1 {
				scanPath = args[0]
			}

			s3Opts, err := resolveS3Options(
				cmd.String("cfg"),
				storage.S3Options{
					Endpoint:       cmd.String("endpoint"),
					Bucket:         cmd.String("bucket"),
					Region:         cmd.String("region"),
					Prefix:         cmd.String("prefix"),
					ForcePathStyle: cmd.Bool("force-path-style"),
				},
				cmd.IsSet("force-path-style"),
			)
			if err != nil {
				return err
			}

			return runPluginsPush(ctx, pushOptions{
				scanPath:       scanPath,
				filter:         cmd.String("filter"),
				s3:             s3Opts,
				force:          cmd.Bool("force"),
				dryRun:         cmd.Bool("dry-run"),
				nonInteractive: cmd.Bool("non-interactive"),
			})
		},
	}
}

func getPluginsRegisterCommand() *cli.Command {
	return &cli.Command{
		Name:      "register",
		Usage:     "Register built plugin binaries with a running service via CreatePlugin",
		ArgsUsage: "[path]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "addr",
				Usage: "gRPC server address",
				Value: "localhost:8080",
			},
			&cli.StringFlag{
				Name:  "cfg",
				Usage: "path to service config YAML; registry.plugins_dir is used as --plugins-prefix when that flag is not set",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "filter",
				Usage: "glob filter pattern for plugins (e.g. 'connectrpc/*')",
				Value: "",
			},
			&cli.BoolFlag{
				Name:  "non-interactive",
				Usage: "disable interactive UI and dynamic progress bars",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "scan and print planned CreatePlugin commands without contacting the server",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "fail-on-error",
				Usage: "exit with error if any plugin failed registration",
				Value: true,
			},
			&cli.StringFlag{
				Name:  "plugins-prefix",
				Usage: "prefix directory for plugins on the server (overrides registry.plugins_dir from --cfg)",
				Value: defaultPluginsPrefix,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			scanPath := defaultPluginsScanPath
			if args := cmd.Args().Slice(); len(args) >= 1 {
				scanPath = args[0]
			}

			pluginsPrefix, err := resolvePluginsPrefix(
				cmd.String("cfg"),
				cmd.String("plugins-prefix"),
				cmd.IsSet("plugins-prefix"),
			)
			if err != nil {
				return err
			}

			return runPluginsRegister(
				ctx,
				scanPath,
				cmd.String("addr"),
				cmd.String("filter"),
				pluginsPrefix,
				cmd.Bool("non-interactive"),
				cmd.Bool("dry-run"),
				cmd.Bool("fail-on-error"),
			)
		},
	}
}

func getPluginsBuildCommand() *cli.Command {
	return &cli.Command{
		Name:      "build",
		Usage:     "Build plugin binaries from registry Dockerfiles",
		ArgsUsage: "<registry-path>",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Usage:   "output directory for built plugin binaries",
				Value:   "plugins",
			},
			&cli.StringFlag{
				Name:  "filter",
				Usage: "glob filter pattern for plugins (e.g. 'protocolbuffers/*' or 'grpc/go:v1.5.1')",
				Value: "",
			},
			&cli.IntFlag{
				Name:    "parallel",
				Aliases: []string{"p"},
				Usage:   "number of concurrent docker builds",
				Value:   defaultBuildParallel,
			},
			&cli.BoolFlag{
				Name:  "force",
				Usage: "rebuild even if the binary already exists",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "list what would be built without building",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "non-interactive",
				Usage: "disable interactive UI and dynamic progress bars",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "keep-going",
				Usage: "continue building remaining plugins after a failure",
				Value: true,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if len(args) < 1 {
				return ErrMissingRegistryPath
			}

			return runPluginsBuild(
				ctx,
				args[0],
				cmd.String("output"),
				cmd.String("filter"),
				cmd.Int("parallel"),
				cmd.Bool("force"),
				cmd.Bool("dry-run"),
				cmd.Bool("non-interactive"),
				cmd.Bool("keep-going"),
			)
		},
	}
}
