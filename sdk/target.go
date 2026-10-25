package sdk

import (
	"net"
	"net/url"
	"strings"

	"google.golang.org/grpc/resolver"
)

// passthroughScheme hands the address to the dialer as it is, which resolves
// it with the system resolver when it connects.
const passthroughScheme = "passthrough:///"

// defaultPort is what grpc-go's dns resolver assumes for an address without
// one. passthrough assumes nothing, so dialTarget supplies it.
const defaultPort = "443"

// dialTarget is the target NewClient dials for addr.
//
// An address naming a registered scheme — dns:///, unix:, passthrough:/// — is
// used as given. A plain host:port gets passthrough rather than grpc-go's
// default dns resolver. That resolver queries the configured nameservers
// itself, for every name including localhost, and on a host whose first
// nameserver does not answer (a VPN's, typically) it waits about twenty
// seconds before falling back, on every connection. The SDK's list timeout is
// ten: `easyp-svc plugins publish` failed before doing anything, against the
// dev stand and against a stack on the same machine alike. The system
// resolver answers those names at once. grpc.Dial, which NewClient replaced,
// used passthrough too.
//
// An address without a port gets 443, as the dns resolver would have given it.
func dialTarget(addr string) string {
	parsed, err := url.Parse(addr)
	if err == nil && parsed.Scheme != "" && resolver.Get(parsed.Scheme) != nil {
		return addr
	}

	_, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		addr = net.JoinHostPort(strings.Trim(addr, "[]"), defaultPort)
	}

	return passthroughScheme + addr
}
