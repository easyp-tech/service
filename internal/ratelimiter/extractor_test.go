package ratelimiter_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/peer"

	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/grpchelper"
	"github.com/easyp-tech/service/internal/ratelimiter"
)

func peerCtx(t *testing.T, addr string) context.Context {
	t.Helper()

	resolved, err := net.ResolveTCPAddr("tcp", addr)
	require.NoError(t, err)

	return peer.NewContext(t.Context(), &peer.Peer{Addr: resolved}) //nolint:exhaustruct // Only Addr is read.
}

// The regression. Behind a proxy every connection arrives from the proxy's
// address, so an extractor reading the peer directly hands the limiter one key
// for the entire world: a rate of 10/s and a concurrency of 2 shared by every
// caller at once. The interceptor has already resolved who is calling; this
// must use that answer.
func TestExtractorSeparatesCallersBehindOneProxy(t *testing.T) {
	t.Parallel()

	const proxy = "10.1.2.3:44321"

	first := core.WithCallerIP(peerCtx(t, proxy), "203.0.113.7")
	second := core.WithCallerIP(peerCtx(t, proxy), "203.0.113.8")

	require.Equal(t, "203.0.113.7", ratelimiter.PeerIPExtractor(first))
	require.Equal(t, "203.0.113.8", ratelimiter.PeerIPExtractor(second))
	require.NotEqual(t, ratelimiter.PeerIPExtractor(first), ratelimiter.PeerIPExtractor(second),
		"two callers behind one proxy share a limiter bucket")
}

// Without the interceptor — direct calls, tests — the peer is still the answer.
func TestExtractorFallsBackToPeer(t *testing.T) {
	t.Parallel()

	require.Equal(t, "198.51.100.9", ratelimiter.PeerIPExtractor(peerCtx(t, "198.51.100.9:44321")))
}

// "unknown" is the interceptor saying it could not tell, not an address. Used
// as a key it would file every unidentifiable caller under one bucket — the
// same defect this file exists for, wearing a different name. An empty key
// means fail-open, which is the existing contract.
func TestExtractorTreatsUnknownAsNoKey(t *testing.T) {
	t.Parallel()

	ctx := core.WithCallerIP(t.Context(), grpchelper.CallerIPUnknown)

	require.Empty(t, ratelimiter.PeerIPExtractor(ctx))
}

func TestExtractorEmptyWithoutPeerOrCaller(t *testing.T) {
	t.Parallel()

	require.Empty(t, ratelimiter.PeerIPExtractor(t.Context()))
}

// The extractor cannot import grpchelper — grpchelper chains the limiters, so
// the dependency would close a cycle — and duplicates the sentinel instead.
// This is what keeps the copy honest.
func TestUnknownSentinelMatchesGRPCHelper(t *testing.T) {
	t.Parallel()

	ctx := core.WithCallerIP(t.Context(), grpchelper.CallerIPUnknown)
	require.Empty(t, ratelimiter.PeerIPExtractor(ctx),
		"grpchelper.CallerIPUnknown changed and the extractor's copy did not")
}
