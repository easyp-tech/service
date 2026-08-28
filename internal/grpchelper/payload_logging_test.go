package grpchelper

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/monitor"
)

// The request and response below carry strings that could not plausibly come
// from anywhere else in a log line, so finding them is proof of where they came
// from rather than a coincidence of formatting.
const (
	callerPackage   = "acme.billing.v1"
	generatedSource = "package billing // GENERATED-SOURCE-MARKER"
)

// runLoggedCall drives one unary call through the production logging
// interceptor and returns everything it wrote.
func runLoggedCall(t *testing.T) string {
	t.Helper()

	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := monitor.WithContext(context.Background(), log)

	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"acme/internal/billing.proto"},
		ProtoFile: []*descriptorpb.FileDescriptorProto{
			{Name: new("acme/internal/billing.proto"), Package: new(callerPackage)},
		},
	}
	resp := &pluginpb.CodeGeneratorResponse{
		File: []*pluginpb.CodeGeneratorResponse_File{
			{Name: new("billing.pb.go"), Content: new(generatedSource)},
		},
	}

	interceptor := logging.UnaryServerInterceptor(interceptorLogger(log), loggingOptions()...)

	_, err := interceptor(ctx, req,
		&grpc.UnaryServerInfo{
			FullMethod: "/easyp.generator.v1.GeneratorAPI/GenerateCode",
		},
		func(context.Context, any) (any, error) { return resp, nil },
	)
	require.NoError(t, err)

	return buf.String()
}

// TestRequestPayloadStaysOutOfTheLog guards the half of the leak that matters
// most: the caller's proto definitions. Enabling logging.PayloadReceived turns
// this red.
func TestRequestPayloadStaysOutOfTheLog(t *testing.T) {
	t.Parallel()

	out := runLoggedCall(t)

	require.NotContains(t, out, callerPackage,
		"the caller's proto definitions reached the log; payload logging is back on")
	require.NotContains(t, out, "grpc.request.content")
}

// TestResponsePayloadStaysOutOfTheLog guards the other half: the code the
// service generated for the caller. Enabling logging.PayloadSent turns this red.
func TestResponsePayloadStaysOutOfTheLog(t *testing.T) {
	t.Parallel()

	out := runLoggedCall(t)

	require.NotContains(t, out, generatedSource,
		"generated source reached the log; payload logging is back on")
	require.NotContains(t, out, "grpc.response.content")
}

// TestCallIsStillLogged exists so the two tests above cannot be satisfied by
// removing logging altogether. Method, code and duration are the reason the
// interceptor is in the chain at all.
func TestCallIsStillLogged(t *testing.T) {
	t.Parallel()

	out := runLoggedCall(t)

	require.Contains(t, out, "finished call")
	require.Contains(t, out, "GenerateCode")
	require.Contains(t, out, `"grpc.code":"OK"`)
}

// TestLoggingOptionsExcludePayloadEvents states the intent directly, so that a
// future edit to loggingOptions fails against the decision rather than only
// against its visible effect.
func TestLoggingOptionsExcludePayloadEvents(t *testing.T) {
	t.Parallel()

	opts := loggingOptions()
	require.Len(t, opts, 1, "loggingOptions grew an option this test has not considered")

	out := runLoggedCall(t)
	require.Equal(t, 2, strings.Count(out, "\n"),
		"expected exactly two log lines per call (started, finished), got:\n"+out)
}
