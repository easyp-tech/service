package sdk

import (
	"net/url"

	"google.golang.org/grpc/resolver"
)

// passthroughScheme hands the address to the dialer as it is, which resolves
// it with the system resolver when it connects.
const passthroughScheme = "passthrough:///"

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
func dialTarget(addr string) string {
	parsed, err := url.Parse(addr)
	if err == nil && parsed.Scheme != "" && resolver.Get(parsed.Scheme) != nil {
		return addr
	}

	return passthroughScheme + addr
}
