//go:build integration

package integration

import (
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/api"
	"github.com/easyp-tech/service/internal/grpchelper"
	"github.com/easyp-tech/service/sdk"
)

// gRPC's own default receive limit. Both ends used to sit on it while the
// service was configured to allow plugin output sixteen times larger.
const grpcDefaultRecvLimit = 4 << 20

type panicCounter struct{ c prometheus.Counter }

func (p panicCounter) PanicsTotal() prometheus.Counter { return p.c }

// serveOverTCP stands the real gRPC server up in front of the harness and
// returns an SDK client pointed at it. Both ends are the production ones: the
// limits under test live in NewServer and in the SDK's dial options, so a
// hand-rolled client or a bare grpc.NewServer would test neither.
func serveOverTCP(t *testing.T, h *harness) (*sdk.Client, string) {
	t.Helper()

	srv, healthSrv := grpchelper.NewServer(
		panicCounter{c: prometheus.NewCounter(prometheus.CounterOpts{ //nolint:exhaustruct
			Name: "panics_total",
			Help: "Panics recovered during the test.",
		})},
		slog.New(slog.DiscardHandler),
		grpc_prometheus.NewServerMetrics(),
		api.ErrorToStatus,
		insecure.NewCredentials(),
		nil,
		nil,
		grpchelper.ServerOptions{}, //nolint:exhaustruct // Defaults are what production gets.
	)

	api.New(srv, healthSrv, h.core, slog.New(slog.DiscardHandler))

	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() { _ = srv.Serve(listener) }()

	t.Cleanup(srv.Stop)

	addr := listener.Addr().String()

	client, err := sdk.NewClient(addr, sdk.WithInsecure())
	require.NoError(t, err)

	t.Cleanup(func() { _ = client.Close() })

	return client, addr
}

// padding builds a CodeGeneratorRequest of at least n bytes on the wire, using
// a field the service passes through untouched.
func padding(n int) []*descriptorpb.FileDescriptorProto {
	const chunk = 1 << 16

	files := make([]*descriptorpb.FileDescriptorProto, 0, n/chunk+1)
	blob := strings.Repeat("p", chunk)

	for total := 0; total < n; total += chunk {
		name := blob
		files = append(files, &descriptorpb.FileDescriptorProto{ //nolint:exhaustruct
			Name: &name,
		})
	}

	return files
}

// A request larger than gRPC's 4 MiB default has to reach the service. Proto
// trees of this size are ordinary in a monorepo, and the server used to refuse
// them at the transport before any of the service's own limits applied.
func TestLargeRequestIsAccepted(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	version := uniqueVersion()
	binary := buildStubPlugin(t, h, "test", "large-req", version)
	registerPlugin(t, h, "test", "large-req", version, binary)

	client, _ := serveOverTCP(t, h)

	param := "ok"
	req := &pluginpb.CodeGeneratorRequest{ //nolint:exhaustruct
		Parameter: &param,
		ProtoFile: padding(grpcDefaultRecvLimit + (1 << 20)),
	}

	resp, err := client.GenerateCode(t.Context(), "test/large-req:"+version, req)
	require.NoError(t, err, "a request above gRPC's default 4 MiB limit was refused")
	require.NotEmpty(t, resp.GetFile())
}

// The other direction, and the one that wastes the most work: the plugin runs
// to completion, the service marshals the answer, and the client used to reject
// it on arrival because its own default was still 4 MiB.
func TestLargeResponseIsDelivered(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	version := uniqueVersion()
	binary := buildStubPlugin(t, h, "test", "large-resp", version)
	registerPlugin(t, h, "test", "large-resp", version, binary)

	client, _ := serveOverTCP(t, h)

	const want = grpcDefaultRecvLimit + (1 << 20)

	param := "bytes=" + strconv.Itoa(want)
	req := &pluginpb.CodeGeneratorRequest{Parameter: &param} //nolint:exhaustruct

	resp, err := client.GenerateCode(t.Context(), "test/large-resp:"+version, req)
	require.NoError(t, err, "a response above gRPC's default 4 MiB limit was refused by the client")
	require.Len(t, resp.GetFile(), 1)
	require.Len(t, resp.GetFile()[0].GetContent(), want)
}

// A client that opts back down to the old limit must still be refused, which is
// what proves the passing cases above come from the raised limit rather than
// from the messages being smaller than expected.
func TestClientLimitStillApplies(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	version := uniqueVersion()
	binary := buildStubPlugin(t, h, "test", "small-client", version)
	registerPlugin(t, h, "test", "small-client", version, binary)

	_, addr := serveOverTCP(t, h)

	param := "bytes=" + strconv.Itoa(grpcDefaultRecvLimit+(1<<20))
	req := &pluginpb.CodeGeneratorRequest{Parameter: &param} //nolint:exhaustruct

	limited, err := sdk.NewClient(addr, sdk.WithInsecure(),
		sdk.WithMaxRecvMsgSize(grpcDefaultRecvLimit))
	require.NoError(t, err)

	t.Cleanup(func() { _ = limited.Close() })

	_, err = limited.GenerateCode(t.Context(), "test/small-client:"+version, req)
	require.Error(t, err, "a client configured with the small limit accepted an oversized response")
	require.Contains(t, err.Error(), "larger than max")
}
