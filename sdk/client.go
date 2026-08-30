package sdk

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/pluginpb"

	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
)

// Client is a client for the EasyP API Service.
type Client struct {
	conn      *grpc.ClientConn
	genClient generator.GeneratorAPIClient
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
		genClient: generator.NewGeneratorAPIClient(conn),
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

// ListPlugins retrieves the complete list of available plugins, optionally
// filtered. The server pages its listing; this walks every page before
// returning, so the caller sees one list, not the first hundred entries. The
// configured list timeout spans the whole walk.
//
// Options rather than a variadic filter: `filter ...PluginFilter` was an
// optional argument wearing a variadic's clothes, and a second option could
// only have been added by changing the signature — which, past v1, means a new
// method with a worse name.
func (c *Client) ListPlugins(ctx context.Context, opts ...ListOption) ([]*generator.PluginInfo, error) {
	ctx, cancel := c.withTimeout(ctx, c.cfg.listPluginsTimeout)
	defer cancel()

	var listCfg listConfig
	for _, opt := range opts {
		opt.applyList(&listCfg)
	}

	req := &generator.PluginsRequest{}
	if listCfg.filter.Group != "" {
		req.Group = &listCfg.filter.Group
	}

	if listCfg.filter.Name != "" {
		req.Name = &listCfg.filter.Name
	}

	if listCfg.filter.Version != "" {
		req.Version = &listCfg.filter.Version
	}

	req.Tags = append([]string(nil), listCfg.filter.Tags...)

	var plugins []*generator.PluginInfo

	// The filter travels with every page: the continuation token is only
	// meaningful alongside the filters it was issued for.
	for {
		resp, err := c.genClient.Plugins(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("c.genClient.Plugins: %w", err)
		}

		plugins = append(plugins, resp.GetPlugins()...)

		token := resp.GetNextPageToken()
		if token == "" {
			break
		}

		req.PageToken = &token
	}

	// No second pass over the result. The server applies these filters itself,
	// so re-applying them here only mattered if the two disagreed — and then the
	// client would have quietly hidden the disagreement instead of surfacing it.
	return plugins, nil
}

// UpdatePlugin replaces the config and tags of a registered plugin.
//
// paths selects what to replace: "config", "tags", or both. Passing none
// replaces both, which is what the service does with an empty mask. Updating
// tags alone leaves the plugin's command line untouched — which is the point of
// the mask, since resending a command line to change a label is how a registry
// entry ends up pointing at the wrong binary.
func (c *Client) UpdatePlugin(
	ctx context.Context,
	group, name, version string,
	pluginConfig map[string]any,
	tags []string,
	paths ...string,
) (*generator.PluginInfo, error) {
	ctx, cancel := c.withTimeout(ctx, c.cfg.createPluginTimeout)
	defer cancel()

	req := &generator.UpdatePluginRequest{
		Group:   group,
		Name:    name,
		Version: version,
		Tags:    tags,
	}

	if len(paths) > 0 {
		req.UpdateMask = &fieldmaskpb.FieldMask{Paths: append([]string(nil), paths...)}
	}

	if pluginConfig != nil {
		cfgStruct, err := structpb.NewStruct(pluginConfig)
		if err != nil {
			return nil, fmt.Errorf("structpb.NewStruct: %w", err)
		}

		req.Config = cfgStruct
	}

	resp, err := c.genClient.UpdatePlugin(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("c.genClient.UpdatePlugin: %w", err)
	}

	return resp.GetPlugin(), nil
}

// DeletePlugin removes a plugin registration. The archive in object storage is
// left alone.
func (c *Client) DeletePlugin(ctx context.Context, group, name, version string) error {
	ctx, cancel := c.withTimeout(ctx, c.cfg.createPluginTimeout)
	defer cancel()

	_, err := c.genClient.DeletePlugin(ctx, &generator.DeletePluginRequest{
		Group:   group,
		Name:    name,
		Version: version,
	})
	if err != nil {
		return fmt.Errorf("c.genClient.DeletePlugin: %w", err)
	}

	return nil
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
