package sdk

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/pluginpb"

	generator "github.com/easyp-tech/service/api/generator/v1"
)

// Client is a client for the EasyP API Service.
type Client struct {
	conn      *grpc.ClientConn
	genClient generator.ServiceAPIClient
	cfg       *config
	health    *healthMonitor // nil if health check is disabled
}

// NewClient creates a new Client connected to the specified address.
func NewClient(addr string, opts ...Option) (*Client, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt.apply(cfg)
	}

	// Build interceptor chain: retry first, then user interceptors.
	interceptors := make([]grpc.UnaryClientInterceptor, 0, 1+len(cfg.unaryInterceptors))
	interceptors = append(interceptors, retryUnaryInterceptor(cfg.maxRetries, cfg.retryBaseDelay, cfg.retryMaxDelay))
	interceptors = append(interceptors, cfg.unaryInterceptors...)

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(cfg.transportCreds),
		grpc.WithChainUnaryInterceptor(interceptors...),
		// Without this the client caps responses at gRPC's 4 MiB default, which
		// is below what the service is allowed to generate — a large request
		// would be served in full and then rejected on arrival.
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(cfg.maxRecvMsgSize)),
	}

	if cfg.keepaliveParams != nil {
		dialOpts = append(dialOpts, grpc.WithKeepaliveParams(*cfg.keepaliveParams))
	}

	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("grpc.NewClient %s: %w", addr, err)
	}

	client := &Client{
		conn:      conn,
		genClient: generator.NewServiceAPIClient(conn),
		cfg:       cfg,
	}

	if cfg.enableHealthCheck {
		client.health = &healthMonitor{
			conn:     conn,
			interval: cfg.healthCheckInterval,
			stopCh:   make(chan struct{}),
		}
		go client.health.start()
	}

	return client, nil
}

// Close closes the underlying gRPC connection.
// If a health monitor is running, it is stopped first.
func (c *Client) Close() error {
	if c.health != nil {
		c.health.stop()
	}

	err := c.conn.Close()
	if err != nil {
		return fmt.Errorf("conn.Close: %w", err)
	}

	return nil
}

// withTimeout returns a context with the earlier of the user's existing deadline
// or now+defaultTimeout. If the user has no deadline, defaultTimeout is used.
func (c *Client) withTimeout(ctx context.Context, defaultTimeout time.Duration) (context.Context, context.CancelFunc) { //nolint:funcorder,lll // withTimeout is a helper used by public methods above it
	if userDeadline, ok := ctx.Deadline(); ok {
		deadline := time.Now().Add(defaultTimeout)
		if userDeadline.Before(deadline) {
			return ctx, func() {} // user deadline is earlier
		}

		return context.WithDeadline(ctx, deadline)
	}

	return context.WithTimeout(ctx, defaultTimeout)
}

// GenerateCode executes a plugin to generate code.
func (c *Client) GenerateCode(
	ctx context.Context, pluginName string, req *pluginpb.CodeGeneratorRequest,
) (*pluginpb.CodeGeneratorResponse, error) {
	ctx, cancel := c.withTimeout(ctx, c.cfg.generateCodeTimeout)
	defer cancel()

	resp, err := c.genClient.GenerateCode(ctx, &generator.GenerateCodeRequest{
		PluginName:           pluginName,
		CodeGeneratorRequest: req,
	})
	if err != nil {
		return nil, fmt.Errorf("c.genClient.GenerateCode: %w", err)
	}

	return resp.GetCodeGeneratorResponse(), nil
}

// ListPlugins retrieves a list of available plugins, optionally filtered.
func (c *Client) ListPlugins(ctx context.Context, filter ...PluginFilter) ([]*generator.PluginInfo, error) {
	ctx, cancel := c.withTimeout(ctx, c.cfg.listPluginsTimeout)
	defer cancel()

	req := &generator.PluginsRequest{}
	if len(filter) > 0 {
		if filter[0].Group != "" {
			req.Group = new(filter[0].Group)
		}
		if filter[0].Name != "" {
			req.Name = new(filter[0].Name)
		}
		if filter[0].Version != "" {
			req.Version = new(filter[0].Version)
		}
		req.Tags = append([]string(nil), filter[0].Tags...)
	}

	resp, err := c.genClient.Plugins(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("c.genClient.Plugins: %w", err)
	}

	if len(filter) == 0 || filter[0].isEmpty() {
		return resp.GetPlugins(), nil
	}

	return applyFilter(resp.GetPlugins(), filter[0]), nil
}

// CreatePlugin registers a new plugin in the service.
// When S3 binary storage is enabled on the service, the plugin archive must
// be pushed to storage beforehand (easyp-svc plugins push): the service
// verifies its presence and records its sha256 checksum at registration.
func (c *Client) CreatePlugin(
	ctx context.Context,
	group, name, version string,
	pluginConfig map[string]any,
	tags []string,
) (*generator.PluginInfo, error) {
	ctx, cancel := c.withTimeout(ctx, c.cfg.createPluginTimeout)
	defer cancel()

	cfgStruct, err := structpb.NewStruct(pluginConfig)
	if err != nil {
		return nil, fmt.Errorf("structpb.NewStruct: %w", err)
	}

	resp, err := c.genClient.CreatePlugin(ctx, &generator.CreatePluginRequest{
		Group:   group,
		Name:    name,
		Version: version,
		Config:  cfgStruct,
		Tags:    tags,
	})
	if err != nil {
		return nil, fmt.Errorf("c.genClient.CreatePlugin: %w", err)
	}

	return resp.GetPlugin(), nil
}
