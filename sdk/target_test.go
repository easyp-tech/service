package sdk

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDialTarget(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"localhost:23410":                        "passthrough:///localhost:23410",
		"127.0.0.1:8080":                         "passthrough:///127.0.0.1:8080",
		"[::1]:8080":                             "passthrough:///[::1]:8080",
		"enterprise.plugins.beta.easyp.tech:443": "passthrough:///enterprise.plugins.beta.easyp.tech:443",
		"easyp-api":                              "passthrough:///easyp-api",
		"dns:///svc.ns.svc.cluster.local:8080":   "dns:///svc.ns.svc.cluster.local:8080",
		"passthrough:///bufnet":                  "passthrough:///bufnet",
		"unix:///run/easyp.sock":                 "unix:///run/easyp.sock",
	}

	for addr, want := range tests {
		assert.Equal(t, want, dialTarget(addr), addr)
	}
}
