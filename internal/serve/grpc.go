package serve

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"

	"google.golang.org/grpc"
)

// GRPC starts gRPC server on addr, logged as service.
// It runs until failed or ctx.Done.
func GRPC(log *slog.Logger, host string, port uint16, srv *grpc.Server) func(context.Context) error {
	return func(ctx context.Context) error {
		ln, err := new(net.ListenConfig).Listen(ctx, "tcp", net.JoinHostPort(host, strconv.FormatUint(uint64(port), 10)))
		if err != nil {
			return fmt.Errorf("net.Listen: %w", err)
		}

		errc := make(chan error, 1)
		go func() { errc <- srv.Serve(ln) }()
		log.Info("started gRPC server", "host", host, "port", port)

		defer log.Info("shutdown gRPC server")

		select {
		case err = <-errc:
			// Serve returning on its own stops accepting, not serving: the
			// connections it already holds keep running handlers. Draining them
			// here keeps them from outliving the worker pool and the database,
			// which the caller closes as soon as this returns.
			srv.GracefulStop()
		case <-ctx.Done():
			srv.GracefulStop()
		}

		if err != nil {
			return fmt.Errorf("srv.Serve: %w", err)
		}

		return nil
	}
}
