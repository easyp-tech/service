package grpchelper

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/realip"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/easyp-tech/service/internal/core"
)

// resolveCaller runs the two interceptors that decide who is calling — realip
// then callerIP — the way NewServer chains them, and reports what the handler
// ends up seeing.
func resolveCaller(t *testing.T, trusted []string, peerAddr string, headers map[string]string) string {
	t.Helper()

	prefixes := make([]netip.Prefix, 0, len(trusted))
	for _, cidr := range trusted {
		p, err := netip.ParsePrefix(cidr)
		require.NoError(t, err)
		prefixes = append(prefixes, p)
	}

	host, portStr, err := net.SplitHostPort(peerAddr)
	require.NoError(t, err)

	ip, err := netip.ParseAddr(host)
	require.NoError(t, err)

	port, err := net.DefaultResolver.LookupPort(t.Context(), "tcp", portStr)
	require.NoError(t, err)

	ctx := peer.NewContext(t.Context(), &peer.Peer{ //nolint:exhaustruct // Only Addr is read.
		Addr: net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port))),
	})

	md := metadata.New(headers)
	ctx = metadata.NewIncomingContext(ctx, md)

	var seen string

	inner := func(ctx context.Context, _ any) (any, error) {
		seen = core.CallerIPFromContext(ctx)

		return nil, nil
	}

	// The two interceptors are run directly, in the order NewServer chains
	// them: realip decides the address, callerIP puts it where everything
	// downstream reads it from.
	realIP := realip.UnaryServerInterceptorOpts(realIPOptions(prefixes)...)
	callerIP := callerIPUnaryInterceptor()

	_, err = realIP(ctx, nil, &grpc.UnaryServerInfo{}, //nolint:exhaustruct // Unread by these two.
		func(ctx context.Context, req any) (any, error) {
			return callerIP(ctx, req, &grpc.UnaryServerInfo{}, inner) //nolint:exhaustruct // Unread.
		})
	require.NoError(t, err)

	return seen
}

// A proxy inside the trusted range speaks for its client, which is the whole
// reason the setting exists: behind an ingress every caller otherwise arrives
// from one address and shares one rate-limit bucket with everyone else.
func TestCallerIPFromTrustedProxyHeader(t *testing.T) {
	t.Parallel()

	got := resolveCaller(t,
		[]string{"10.0.0.0/8"},
		"10.1.2.3:44321",
		map[string]string{"x-forwarded-for": "203.0.113.7"},
	)

	require.Equal(t, "203.0.113.7", got)
}

// The half that matters for security. If a header from an untrusted source were
// believed, any caller could pick its own identity and step out of whatever
// limit or audit trail applied to it.
func TestCallerIPIgnoresHeaderFromUntrustedPeer(t *testing.T) {
	t.Parallel()

	got := resolveCaller(t,
		[]string{"10.0.0.0/8"},
		"198.51.100.9:44321",
		map[string]string{"x-forwarded-for": "203.0.113.7"},
	)

	require.Equal(t, "198.51.100.9", got, "a forged X-Forwarded-For was believed")
}

// X-Real-IP is the other convention proxies use, and is accepted on the same
// terms.
func TestCallerIPAcceptsXRealIPFromTrustedProxy(t *testing.T) {
	t.Parallel()

	got := resolveCaller(t,
		[]string{"10.0.0.0/8"},
		"10.1.2.3:44321",
		map[string]string{"x-real-ip": "203.0.113.7"},
	)

	require.Equal(t, "203.0.113.7", got)
}

// With nothing configured the peer is the caller — correct for a listener
// clients reach directly, and the reason this is not on by default.
func TestCallerIPWithoutTrustedProxiesUsesPeer(t *testing.T) {
	t.Parallel()

	got := resolveCaller(t, nil,
		"198.51.100.9:44321",
		map[string]string{"x-forwarded-for": "203.0.113.7"},
	)

	require.Equal(t, "198.51.100.9", got)
}

// The port used to be part of this value. Keeping it would make any per-caller
// limit keyed on the address unique per connection, and fill the audit log with
// ephemeral port numbers.
func TestCallerIPCarriesNoPort(t *testing.T) {
	t.Parallel()

	got := resolveCaller(t, nil, "198.51.100.9:44321", nil)

	require.Equal(t, "198.51.100.9", got)
	require.NotContains(t, got, ":44321")
}

func TestCallerIPUnknownWithoutPeer(t *testing.T) {
	t.Parallel()

	require.Equal(t, CallerIPUnknown, extractCallerIP(context.Background()))
}
