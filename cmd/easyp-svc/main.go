package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/easyp-tech/service/internal/adapters/storage"
	"github.com/easyp-tech/service/sdk"
)

// Flag and argument names shared by several plugin subcommands.
const (
	flagCfg            = "cfg"
	envCfg             = "EASYP_CONFIG"
	flagFilter         = "filter"
	flagParallel       = "parallel"
	flagForce          = "force"
	flagDryRun         = "dry-run"
	flagNonInteractive = "non-interactive"
	flagTLSCA          = "tls-ca"
	flagTLSCert        = "tls-cert"
	flagTLSKey         = "tls-key"
	flagInsecure       = "insecure"
	flagToken          = "token"

	usageNonInteractive = "disable interactive UI and dynamic progress bars"
)

// version is stamped at link time by the release build (-X main.version). It is
// a variable rather than a constant for exactly that reason, and the fallback
// is what a local `go build` gets.
//
// It reaches three places: `easyp-svc --version`, the version field on every log
// line, and the service.version resource attribute on every trace and profile.
// Before it existed all three said "dev" no matter what was running, so the only
// way to identify a deployed build was to read the image label from outside the
// container — which says nothing about the binary actually executing.
var version = "dev"

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

	app := &cli.Command{
		Name:     "easyp-svc",
		Usage:    "EasyP Service CLI",
		Version:  version,
		Commands: getCommands(),
	}

	err := app.Run(ctx, os.Args)
	if err != nil {
		slog.Error("Failed to run app", "error", err)
		os.Exit(1)
	}
}

func cfgFlag(usage string) *cli.StringFlag {
	return &cli.StringFlag{
		Name:    flagCfg,
		Usage:   usage,
		Value:   "",
		Sources: cli.EnvVars(envCfg),
	}
}

func getCommands() []*cli.Command {
	return []*cli.Command{
		getServiceCommand(),
		getPluginsCommand(),
		getAuthCommand(),
		getAPICommand(),
		getConfigCommand(),
		getHealthCommand(),
	}
}

func getConfigCommand() *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "Inspect and check the service configuration",
		Description: "Settings come from three layers — the environment, the config file, and the " +
			"defaults in the binary — and until now the only way to see what they resolved to was to " +
			"start the service. Both subcommands resolve exactly as `service start` does; with no " +
			"--cfg they read the environment alone, which is how a Helm deployment is configured.",
		Commands: []*cli.Command{
			{
				Name:  "validate",
				Usage: "Check a configuration without starting the service",
				Flags: []cli.Flag{
					cfgFlag("path to config file; omit to check the environment alone"),
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return runConfigValidate(ctx, cmd.String(flagCfg))
				},
			},
			{
				Name:  "print",
				Usage: "Print the configuration the service would run with",
				Flags: []cli.Flag{
					cfgFlag("path to config file; omit to resolve from the environment alone"),
					&cli.BoolFlag{
						Name:  "origin",
						Usage: "annotate each setting with the layer it came from",
						Value: false,
					},
					&cli.BoolFlag{
						Name: "changed",
						Usage: "print only the settings that differ from the built-in defaults, " +
							"which is what a config file needs to state and no more",
						Value: false,
					},
					&cli.BoolFlag{
						Name: "show-secrets",
						Usage: "print credentials instead of a placeholder; the output then belongs " +
							"nowhere but a terminal",
						Value: false,
					},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return runConfigPrint(ctx, printOptions{
						cfgPath:     cmd.String(flagCfg),
						origin:      cmd.Bool("origin"),
						changed:     cmd.Bool("changed"),
						showSecrets: cmd.Bool("show-secrets"),
					})
				},
			},
		},
	}
}

func getAPICommand() *cli.Command {
	return &cli.Command{
		Name:  "api",
		Usage: "Inspect the gRPC API contract",
		Commands: []*cli.Command{
			{
				Name: "descriptor",
				Usage: "Write a FileDescriptorSet for the API, for use with " +
					"`grpcurl -protoset` (the server does not serve reflection)",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "output",
						Aliases: []string{"o"},
						Usage:   "file to write to; \"-\" writes to stdout",
						Value:   "-",
					},
				},
				Action: func(_ context.Context, cmd *cli.Command) error {
					return runAPIDescriptor(cmd.String("output"))
				},
			},
		},
	}
}

func getAuthCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Manage credentials for the mutating API methods",
		Commands: []*cli.Command{
			{
				Name:  "new-token",
				Usage: "Generate a write token and print the config entry authorising it",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "name",
						Usage: "label identifying this token in logs and the audit trail",
						Value: "unnamed",
					},
				},
				Action: func(_ context.Context, cmd *cli.Command) error {
					return runAuthNewToken(cmd.String("name"))
				},
			},
		},
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
					cfgFlag("path to config file"),
					&cli.StringFlag{
						Name: "log-level",
						// Every other flag in this tool is kebab-case
						// (--dry-run, --tls-ca, --force-path-style); this one
						// was the exception. The old spelling is kept as an
						// alias rather than dropped: it appears in committed
						// compose files and in scripts, and a flag that stops
						// existing fails the start rather than the flag.
						Aliases: []string{"log_level"},
						Usage: "log level (debug, info, warn, error); " +
							"overrides log.level from the configuration",
						// No default, so that "not given" stays distinguishable
						// from "given". The level's default now lives with every
						// other default, in the struct tag on log.level, and is
						// info rather than the debug this flag used to assume.
						Value: "",
					},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfgPath := cmd.String(flagCfg)
					logLvl := cmd.String("log-level")

					// Printed before the logger exists, so it stays plain text.
					// It says only where the settings come from; what they
					// resolved to is the "configuration resolved" record the
					// service logs once it knows, which is the honest place for
					// it — an unset --log-level is not a level of "".
					if cfgPath == "" {
						_, _ = fmt.Fprintln(os.Stdout, "Starting easyp-svc from the environment")
					} else {
						_, _ = fmt.Fprintf(os.Stdout, "Starting easyp-svc with config: %q\n", cfgPath)
					}

					return runServiceStart(ctx, cfgPath, logLvl)
				},
			},
		},
	}
}

func getPluginsCommand() *cli.Command {
	return &cli.Command{
		Name:  "plugins",
		Usage: "Build and publish plugins",
		Commands: []*cli.Command{
			getPluginsBuildCommand(),
			getPluginsPublishCommand(),
		},
	}
}

func getPluginsPublishCommand() *cli.Command {
	return &cli.Command{
		Name:  "publish",
		Usage: "Build, verify, upload and register plugin versions with a running service",
		Description: "Takes each version through in one pass: build it from the registry (or take its " +
			"plugin.tgz from --archives), verify it, upload the archive to object storage with its sha256, " +
			"register it — creating it, or replacing the config of a version the service already has — and " +
			"delete it from the work directory. A version that is registered and already in storage is " +
			"skipped unless --force is set; an upload is skipped whenever storage already holds the same " +
			"archive. With --plugins-dir the archive is unpacked into that directory instead of uploaded, " +
			"for a service on the same host running without object storage. Needs a v1.2.0 or newer server.",
		ArgsUsage: "[registry-path]",
		Flags:     publishFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			opts := publishOptions{
				registryPath:   cmd.Args().First(),
				archivesDir:    cmd.String("archives"),
				pluginsDir:     cmd.String("plugins-dir"),
				workDir:        cmd.String("workdir"),
				keep:           cmd.Bool("keep"),
				filter:         cmd.String(flagFilter),
				force:          cmd.Bool(flagForce),
				dryRun:         cmd.Bool(flagDryRun),
				nonInteractive: cmd.Bool(flagNonInteractive),
				buildParallel:  cmd.Int("build-parallel"),
				parallel:       cmd.Int(flagParallel),
				verify: verifyOptions{
					runtimeImage: cmd.String("runtime-image"),
					smoke:        !cmd.Bool("no-smoke"),
				},
			}

			dest, err := publishDestination(ctx, cmd, opts)
			if err != nil {
				return err
			}

			connect := func() (pluginService, func(), error) {
				return connectService(cmd.String("addr"), resolveWriteToken(cmd.String(flagToken)), clientTLSOptions{
					caFile:   cmd.String(flagTLSCA),
					certFile: cmd.String(flagTLSCert),
					keyFile:  cmd.String(flagTLSKey),
					insecure: cmd.Bool(flagInsecure),
				})
			}

			return runPluginsPublish(ctx, opts, dest, connect)
		},
	}
}

// publishDestination resolves where publish puts archives: the plugins_dir
// given, or object storage configured by flags and --cfg.
//
//nolint:ireturn // destination is the seam between the two modes
func publishDestination(ctx context.Context, cmd *cli.Command, opts publishOptions) (destination, error) {
	if opts.pluginsDir != "" {
		return dirDestination{root: opts.pluginsDir}, nil
	}

	s3Opts, err := resolveS3Options(
		ctx,
		cmd.String(flagCfg),
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
		return nil, err
	}

	if opts.dryRun {
		return storageDestination{bucket: s3Opts.Bucket, prefix: s3Opts.Prefix}, nil
	}

	if s3Opts.Bucket == "" {
		return nil, ErrS3BucketRequired
	}

	store, err := storage.NewS3Storage(ctx, s3Opts)
	if err != nil {
		return nil, fmt.Errorf("storage.NewS3Storage: %w", err)
	}

	return storageDestination{store: store, bucket: s3Opts.Bucket, prefix: s3Opts.Prefix}, nil
}

// connectService opens a client to the service. Mutating calls need a token;
// an empty one is passed through deliberately, since the server's rejection
// names the problem better than a client-side guess would.
//
//nolint:ireturn // pluginService is the seam tests replace
func connectService(addr, token string, tls clientTLSOptions) (pluginService, func(), error) {
	tlsOpt, err := tls.sdkOption()
	if err != nil {
		return nil, nil, err
	}

	sdkOpts := []sdk.Option{tlsOpt}
	if token != "" {
		sdkOpts = append(sdkOpts, sdk.WithToken(token))
	}

	client, err := sdk.NewClient(addr, sdkOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to gRPC server at %s: %w", addr, err)
	}

	return client, func() { _ = client.Close() }, nil
}

func getPluginsBuildCommand() *cli.Command {
	return &cli.Command{
		Name:  "build",
		Usage: "Build plugin binaries from registry Dockerfiles, without uploading anything",
		Description: "Builds and verifies each version into --output: an unpacked bundle by default, or with " +
			"--archive only its verified {group}/{name}/{version}/plugin.tgz — the tree `plugins publish " +
			"--archives` takes, for building on one machine and publishing from another.",
		ArgsUsage: "<registry-path>",
		Flags:     buildFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if len(args) < 1 {
				return ErrMissingRegistryPath
			}

			return runPluginsBuild(ctx, buildOptions{
				registryPath:   args[0],
				outputDir:      cmd.String("output"),
				filter:         cmd.String(flagFilter),
				parallel:       cmd.Int(flagParallel),
				force:          cmd.Bool(flagForce),
				dryRun:         cmd.Bool(flagDryRun),
				nonInteractive: cmd.Bool(flagNonInteractive),
				keepGoing:      cmd.Bool("keep-going"),
				archive:        cmd.Bool("archive"),
				verify: verifyOptions{
					runtimeImage: cmd.String("runtime-image"),
					smoke:        !cmd.Bool("no-smoke"),
				},
			})
		},
	}
}

// publishFlags defines the flags of the corresponding subcommand.
func publishFlags() []cli.Flag {
	flags := []cli.Flag{
		&cli.StringFlag{
			Name:  "archives",
			Usage: "publish the {group}/{name}/{version}/plugin.tgz tree in this directory instead of building",
			Value: "",
		},
		&cli.StringFlag{
			Name: "plugins-dir",
			Usage: "unpack into this directory instead of uploading; it must be the plugins_dir of a service " +
				"running without object storage",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "workdir",
			Usage: "where versions are built; each is removed once published",
			Value: filepath.Join(os.TempDir(), "easyp-publish"),
		},
		&cli.BoolFlag{
			Name:  "keep",
			Usage: "leave built versions in --workdir after publishing them",
			Value: false,
		},
		&cli.StringFlag{
			Name:  flagFilter,
			Usage: "glob filter pattern for plugins (e.g. 'protocolbuffers/*' or 'grpc/go:v1.6.2')",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  flagForce,
			Usage: "publish versions the service already has, rebuilding them and replacing their config",
			Value: false,
		},
		&cli.IntFlag{
			Name:  "build-parallel",
			Usage: "number of concurrent docker builds",
			Value: defaultBuildParallel,
		},
		&cli.IntFlag{
			Name:    flagParallel,
			Aliases: []string{"p"},
			Usage: "versions uploading and registering at once; storage usually caps a single connection, " +
				"so more streams is more throughput, up to the server's rate_limit.max_concurrent_per_ip",
			Value: defaultPublishParallel,
		},
		&cli.BoolFlag{
			Name:  flagDryRun,
			Usage: "list what would be published without building or contacting anything",
			Value: false,
		},
		&cli.BoolFlag{
			Name:  flagNonInteractive,
			Usage: usageNonInteractive,
			Value: false,
		},
	}

	flags = append(flags, verifyFlags()...)
	flags = append(flags, s3Flags()...)

	return append(flags, connectionFlags()...)
}

// s3Flags defines where and with what credentials a command reaches object
// storage. Anything left empty falls back to registry.s3 from --cfg, and the
// credentials themselves to the default AWS chain, so they need not be typed
// on a command line at all.
func s3Flags() []cli.Flag {
	return []cli.Flag{
		cfgFlag("path to service config YAML; registry.s3 is used for settings not passed as flags"),
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
	}
}

// connectionFlags defines how a client command reaches and authenticates to the
// service: transport security first, then the credential.
func connectionFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name: "addr",
			// The service's own default. 8080 is what the Helm chart maps the
			// gRPC port to, which made this flag right for a cluster and wrong
			// for the binary anyone runs locally — and the two disagreed
			// silently, as a connection refused.
			Usage: "gRPC server address (the chart publishes gRPC on 8080)",
			Value: "localhost:23410",
		},
		&cli.StringFlag{
			Name:  flagTLSCA,
			Usage: "PEM bundle of the CA that signed the server certificate (default: system trust store)",
			Value: "",
		},
		&cli.StringFlag{
			Name:  flagTLSCert,
			Usage: "client certificate presented to the server; required when the server enforces mTLS",
			Value: "",
		},
		&cli.StringFlag{
			Name:  flagTLSKey,
			Usage: "private key for --tls-cert",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  flagInsecure,
			Usage: "connect over plaintext, disabling transport security entirely (local development only)",
			Value: false,
		},
		&cli.StringFlag{
			Name:  flagToken,
			Usage: "write token authorising plugin registration (falls back to " + writeTokenEnv + ")",
			Value: "",
		},
	}
}

// buildFlags defines the flags of the corresponding subcommand.
func buildFlags() []cli.Flag {
	return append([]cli.Flag{
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "output directory for built plugin binaries",
			Value:   "plugins",
		},
		&cli.StringFlag{
			Name:  flagFilter,
			Usage: "glob filter pattern for plugins (e.g. 'protocolbuffers/*' or 'grpc/go:v1.5.1')",
			Value: "",
		},
		&cli.IntFlag{
			Name:    flagParallel,
			Aliases: []string{"p"},
			Usage:   "number of concurrent docker builds",
			Value:   defaultBuildParallel,
		},
		&cli.BoolFlag{
			Name: flagForce,
			Usage: "rebuild even if the version is already built; a version built by an older easyp-svc " +
				"was not verified the way this one verifies, and is only rebuilt with this",
			Value: false,
		},
		&cli.BoolFlag{
			Name:  flagDryRun,
			Usage: "list what would be built without building",
			Value: false,
		},
		&cli.BoolFlag{
			Name:  flagNonInteractive,
			Usage: usageNonInteractive,
			Value: false,
		},
		&cli.BoolFlag{
			Name:  "keep-going",
			Usage: "continue building remaining plugins after a failure",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "archive",
			Usage: "keep only each version's verified plugin.tgz, the tree `plugins publish --archives` takes",
			Value: false,
		},
	}, verifyFlags()...)
}

// verifyFlags controls the check every built version goes through.
func verifyFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name: "runtime-image",
			Usage: "image each built plugin is smoke-tested in; must match the base of the " +
				"service image the plugins will run under",
			Value: defaultRuntimeImage,
		},
		&cli.BoolFlag{
			Name: "no-smoke",
			Usage: "skip running each built plugin in the runtime image; the archive is still " +
				"checked to unpack the way the service unpacks it",
			Value: false,
		},
	}
}
