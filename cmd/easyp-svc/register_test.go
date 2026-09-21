package main

import (
	"errors"
	"github.com/easyp-tech/service/internal/core"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolvePluginsPrefix(t *testing.T) {
	t.Parallel()

	t.Run("explicit prefix wins over cfg", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeTempConfig(t, "./from-cfg")
		got, err := resolvePluginsPrefix(cfgPath, "./explicit", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "explicit" && got != filepath.Clean("./explicit") {
			t.Fatalf("expected cleaned explicit prefix, got %q", got)
		}
	})

	t.Run("cfg plugins_dir when prefix not explicit", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeTempConfig(t, "./plugins")
		got, err := resolvePluginsPrefix(cfgPath, defaultPluginsPrefix, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := filepath.Clean("./plugins")
		if got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	})

	t.Run("default when no cfg and prefix not explicit", func(t *testing.T) {
		t.Parallel()

		got, err := resolvePluginsPrefix("", defaultPluginsPrefix, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != defaultPluginsPrefix {
			t.Fatalf("expected %q, got %q", defaultPluginsPrefix, got)
		}
	})

	t.Run("cfg that omits plugins_dir resolves to the declared default", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeTempConfig(t, "")
		got, err := resolvePluginsPrefix(cfgPath, defaultPluginsPrefix, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		want, ok := declaredDefault("registry", "plugins_dir")
		if !ok {
			t.Fatal("registry.plugins_dir declares no default")
		}

		if got != filepath.Clean(want) {
			t.Fatalf("expected %q, got %q", want, got)
		}
	})

	t.Run("missing cfg file", func(t *testing.T) {
		t.Parallel()

		_, err := resolvePluginsPrefix(filepath.Join(t.TempDir(), "missing.yml"), defaultPluginsPrefix, false)
		if err == nil {
			t.Fatal("expected error for missing cfg")
		}
	})
}

func TestPluginCommandPath(t *testing.T) {
	t.Parallel()

	plg := pluginInfo{group: "grpc", name: "go", version: "v1.5.1"}
	got := pluginCommandPath("./plugins", plg)
	want := "plugins/grpc/go/v1.5.1/plugin"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}

	got = pluginCommandPath("/plugins", plg)
	want = "/plugins/grpc/go/v1.5.1/plugin"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFailOnError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		failOnError bool
		failed      int
		wantErr     bool
	}{
		{name: "fail on errors", failOnError: true, failed: 2, wantErr: true},
		{name: "no failures", failOnError: true, failed: 0, wantErr: false},
		{name: "ignore failures", failOnError: false, failed: 3, wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := registrationBatchError(tc.failOnError, tc.failed)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !errors.Is(err, ErrRegisterFailed) {
					t.Fatalf("expected ErrRegisterFailed, got %v", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func writeTempConfig(t *testing.T, pluginsDir string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	content := "registry:\n  plugins_dir: " + pluginsDir + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	return path
}

func TestWithThrottleBackoffRetriesThrottleButNotTheCap(t *testing.T) {
	t.Parallel()

	throttle := status.Error(codes.ResourceExhausted, "rejected by grpc_ratelimit")
	cap := status.Error(codes.ResourceExhausted, core.ErrMaxPluginsExceeded.Error())

	t.Run("a throttled call succeeds once the server lets it through", func(t *testing.T) {
		t.Parallel()

		calls := 0
		err := withThrottleBackoff(t.Context(), time.Millisecond, func() error {
			calls++
			if calls < 4 {
				return throttle
			}

			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 4 {
			t.Fatalf("expected 4 attempts, got %d", calls)
		}
	})

	t.Run("the plugin cap is not retried", func(t *testing.T) {
		t.Parallel()

		calls := 0
		err := withThrottleBackoff(t.Context(), time.Millisecond, func() error {
			calls++

			return cap
		})
		if !errors.Is(err, cap) {
			t.Fatalf("expected the cap error back, got %v", err)
		}
		if calls != 1 {
			t.Fatalf("expected 1 attempt, got %d", calls)
		}
	})

	t.Run("a server that never relents is given up on", func(t *testing.T) {
		t.Parallel()

		calls := 0
		err := withThrottleBackoff(t.Context(), time.Millisecond, func() error {
			calls++

			return throttle
		})
		if !errors.Is(err, throttle) {
			t.Fatalf("expected the throttle error back, got %v", err)
		}
		if calls != registerRetries+1 {
			t.Fatalf("expected %d attempts, got %d", registerRetries+1, calls)
		}
	})
}
