// Package registry provides a registry for EasyP plugin server.
package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/jmoiron/sqlx"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sipki-tech/dev-platform/database"
	"github.com/sipki-tech/dev-platform/database/connectors"
	"github.com/sipki-tech/dev-platform/database/migrations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/core"
)

var _ core.Registry = &Registry{}
var _ core.Plugin = &plugin{}

type (
	// Config provide connection info for database.
	Config struct {
		Postgres   connectors.Raw
		MigrateDir string
		Driver     string
	}
	// Registry is a registry for EasyP plugin server.
	Registry struct {
		sql    *database.SQL
		domain *url.URL
	}
	// plugin is a plugin in the registry.
	plugin struct {
		ID        uuid.UUID `db:"id"`
		Name      string    `db:"name"`
		CreatedAt time.Time `db:"created_at"`
		domain    *url.URL  `db:"-"`
	}
)

// New build and returns a new Registry.
func New(ctx context.Context, reg *prometheus.Registry, namespace string, cfg Config) (*Registry, error) {
	const subsystem = "repo"
	m := database.NewMetrics(reg, namespace, subsystem, new(core.Registry))

	returnErrs := []error{ // List of core.Err… returned by Repo methods.
		core.ErrNotFound,
		core.ErrInvalidPluginName,
	}

	migrates, err := migrations.Parse(cfg.MigrateDir)
	if err != nil {
		return nil, fmt.Errorf("migrations.Parse: %w", err)
	}

	err = migrations.Run(ctx, cfg.Driver, &cfg.Postgres, migrations.Up, migrates)
	if err != nil {
		return nil, fmt.Errorf("migrations.Run: %w", err)
	}

	conn, err := database.NewSQL(ctx, cfg.Driver, database.SQLConfig{
		Metrics:    m,
		ReturnErrs: returnErrs,
	}, &cfg.Postgres)
	if err != nil {
		return nil, fmt.Errorf("database.NewSQL: %w", err)
	}

	return &Registry{
		sql: conn,
	}, nil
}

// Get implements core.Registry.
func (r *Registry) Get(ctx context.Context, pluginName string) (core.Plugin, error) {
	err := r.sql.NoTx(func(d *sqlx.DB) error {
		p := plugin{}

		query := "select id, name, created_at from plugins where name=$1"

		strs := strings.Split(pluginName, ":")
		if len(strs) > 2 || len(strs) == 0 {
			return fmt.Errorf("%w: %s", core.ErrInvalidPluginName, pluginName)
		}
		if len(strs) == 1 {
			query = "select id, name, created_at from plugins where name = $1 order by created_at desc limit 1"
		}

		err := d.GetContext(ctx, &p, query, pluginName)
		if err != nil {
			return fmt.Errorf("d.GetContext: %w", err)
		}

		p.domain = r.domain

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sql.NoTx: %w", err)
	}

	return nil, nil
}

// Close database connection.
func (r *Registry) Close() error {
	return r.sql.Close()
}

// Health checks the health of the registry.
func (r *Registry) Health(ctx context.Context) error {
	return r.sql.NoTx(func(db *sqlx.DB) error { return db.PingContext(ctx) })
}

// Generate implements core.Plugin.
func (p *plugin) Generate(ctx context.Context, req *pluginpb.CodeGeneratorRequest) (*pluginpb.CodeGeneratorResponse, error) {
	requestData, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("proto.Marshal: %w", err)
	}

	imageName := p.domain.String() + "/" + p.Name

	cmd := exec.CommandContext(ctx,
		"docker",
		"run",
		"--rm",
		"-i",
		"--network=none",
		"--memory=128m",
		"--cpus=1.0",
		imageName,
	)

	cmd.Stdin = bytes.NewReader(requestData)

	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("plugin execution failed: %s, stderr: %s", err, string(exitErr.Stderr))
		}

		return nil, fmt.Errorf("cmd.Output: %w", err)
	}

	var response pluginpb.CodeGeneratorResponse
	if err := proto.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("proto.Unmarshal: %w", err)
	}

	return &response, nil
}
