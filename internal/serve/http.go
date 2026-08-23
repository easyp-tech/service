package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HTTP starts HTTP server on addr using handler logged as service.
// It runs until failed or ctx.Done.
func HTTP(log *slog.Logger, host string, port uint16, handler http.Handler) func(context.Context) error {
	return func(ctx context.Context) error {
		srv := &http.Server{ //nolint:gosec
			Addr:    net.JoinHostPort(host, strconv.FormatUint(uint64(port), 10)),
			Handler: handler,
		}

		errc := make(chan error, 1)
		go func() { errc <- srv.ListenAndServe() }()
		log.Info("started HTTP server", "host", host, "port", port)

		defer log.Info("shutdown HTTP server")

		var err error
		select {
		case err = <-errc:
		case <-ctx.Done():
			err = srv.Shutdown(context.Background()) //nolint:contextcheck
		}

		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("srv.ListenAndServe: %w", err)
		}

		return nil
	}
}

// Metrics starts HTTP server on addr path /metrics using reg as
// prometheus handler.
func Metrics(log *slog.Logger, host string, port uint16, reg *prometheus.Registry) func(context.Context) error {
	return func(ctx context.Context) error {
		mux := http.NewServeMux()
		HandleMetrics(mux, reg)

		return HTTP(log, host, port, mux)(ctx)
	}
}

// HandleMetrics adds reg's prometheus handler on /metrics at mux.
func HandleMetrics(mux *http.ServeMux, reg *prometheus.Registry) {
	handler := promhttp.InstrumentMetricHandler(
		reg,
		promhttp.HandlerFor(
			reg,
			promhttp.HandlerOpts{},
		),
	)
	mux.Handle("/metrics", handler)
}
