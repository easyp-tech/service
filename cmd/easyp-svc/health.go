package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/easyp-tech/service/internal/config"
)

const (
	flagAddr           = "addr"
	healthProbeTimeout = 3 * time.Second
	healthLivePath     = "/live"
)

var errNotLive = errors.New("service is not live")

func getHealthCommand() *cli.Command {
	return &cli.Command{
		Name:  "health",
		Usage: "Exit 0 if the service answers its liveness endpoint, 1 otherwise",
		Description: "Meant for a container HEALTHCHECK, where no curl or wget is available. " +
			"It probes /live, not readiness: readiness checks the database, and restarting a " +
			"container every time the database blips turns a recoverable outage into a crash loop.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  flagAddr,
				Usage: "host:port of the health listener; derived from --cfg or the default port when empty",
				Value: "",
			},
			&cli.StringFlag{
				Name:  flagCfg,
				Usage: "path to config file, used only to learn the health port",
				Value: "",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			addr, err := resolveHealthAddr(ctx, cmd.String(flagAddr), cmd.String(flagCfg))
			if err != nil {
				return err
			}

			return probeLive(ctx, addr)
		},
	}
}

func resolveHealthAddr(ctx context.Context, addr, cfgPath string) (string, error) {
	if addr != "" {
		return addr, nil
	}

	var res config.Result
	var err error
	if cfgPath == "" {
		res, err = config.LoadFromEnv(ctx)
	} else {
		res, err = config.Load(ctx, cfgPath)
	}
	if res.Config == nil {
		return "", fmt.Errorf("resolving health port: %w", err)
	}

	port, err := config.ParsePort("server.port.health", res.Config.Server.Port.Health)
	if err != nil {
		return "", fmt.Errorf("resolving health port: %w", err)
	}

	return net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(port), 10)), nil
}

func probeLive(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+healthLivePath, nil)
	if err != nil {
		return fmt.Errorf("building probe request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", errNotLive, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s returned %d", errNotLive, healthLivePath, resp.StatusCode)
	}

	return nil
}
