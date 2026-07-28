package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestVersionEntry_UnmarshalYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    versionEntry
		wantErr bool
	}{
		{
			name:  "scalar inherits defaults",
			input: `- v1.2.3`,
			want:  versionEntry{version: "v1.2.3"},
		},
		{
			name: "mapping with version only",
			input: `- version: v2.0.0
`,
			want: versionEntry{version: "v2.0.0"},
		},
		{
			name: "mapping with full per-version overrides",
			input: `- version: v2.0.0
  binary: protoc-gen-grpc-swift
  build_args:
    GO_MODULE: github.com/foo/bar/v2
  dockerfile: Dockerfile.legacy
  args: ["--network=none"]
  skip: true
`,
			want: versionEntry{
				version:    "v2.0.0",
				buildArgs:  map[string]string{"GO_MODULE": "github.com/foo/bar/v2"},
				dockerfile: "Dockerfile.legacy",
				args:       []string{"--network=none"},
				skip:       true,
			},
		},
		{
			name: "scalar and mapping entries coexist in a versions list",
			input: `- v1.0.0
- version: v2.0.0
  binary: foo
`,
			want: versionEntry{version: "v1.0.0"},
		},
		{
			name: "sequence node is rejected",
			input: `- [v1.0.0]
`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got []versionEntry
			if err := yaml.Unmarshal([]byte(tc.input), &got); err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr {
				t.Fatalf("expected error, got %+v", got)
			}
			if len(got) < 1 {
				t.Fatalf("expected at least 1 entry, got %d: %+v", len(got), got)
			}
			if !reflect.DeepEqual(got[0], tc.want) {
				t.Fatalf("expected %+v, got %+v", tc.want, got[0])
			}
		})
	}
}

func TestJobsFromConfig(t *testing.T) {
	t.Parallel()

	cfg := pluginConfig{
		Binary:    "protoc-gen-default", // deprecated, ignored
		BuildArgs: map[string]string{"GO_MODULE": "github.com/default", "SHARED": "top"},
		Versions: []versionEntry{
			{version: "v1.0.0"}, // inherits defaults
			{version: "v2.0.0", buildArgs: map[string]string{"GO_MODULE": "github.com/v2"}},
			{version: "v2.1.0", skip: true},
			{version: "v2.0.0", buildArgs: map[string]string{"GO_MODULE": "github.com/v2"}},
		},
	}

	jobs, skipped, err := jobsFromConfig(cfg, "grpc", "go", "/registry/grpc/go", "/plugins", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d: %+v", len(jobs), jobs)
	}
	if got, want := skipped, []string{"grpc/go:v2.1.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skipped: expected %v, got %v", want, got)
	}

	// v1.0.0 inherits defaults.
	j0 := jobs[0]
	if got, want := j0.buildArgs, map[string]string{"GO_MODULE": "github.com/default", "SHARED": "top"}; !reflect.DeepEqual(got, want) {
		t.Errorf("v1.0.0 buildArgs: expected %v, got %v", want, got)
	}

	// v2.0.0 overrides GO_MODULE, keeps SHARED.
	j1 := jobs[1]
	if got, want := j1.buildArgs, map[string]string{"GO_MODULE": "github.com/v2", "SHARED": "top"}; !reflect.DeepEqual(got, want) {
		t.Errorf("v2.0.0 buildArgs: expected %v, got %v", want, got)
	}
	if j1.dockerfile != "" {
		t.Errorf("v2.0.0 dockerfile: expected empty, got %q", j1.dockerfile)
	}

	// outputDir encodes group/name/version.
	if got, want := j1.outputDir, "/plugins/grpc/go/v2.0.0"; got != want {
		t.Errorf("v2.0.0 outputDir: expected %q, got %q", want, got)
	}
}

func TestJobsFromConfig_Filter(t *testing.T) {
	t.Parallel()

	cfg := pluginConfig{
		Versions: []versionEntry{
			{version: "v1.5.1"},
			{version: "v1.4.0", skip: true},
		},
	}

	jobs, skipped, err := jobsFromConfig(cfg, "grpc", "go", "/registry/grpc/go", "/plugins", "grpc/go:v1.5.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].version != "v1.5.1" {
		t.Fatalf("filter should keep only v1.5.1 job, got %+v", jobs)
	}
	// skip applies after filter: v1.4.0 does not match filter, so it is neither job nor skipped.
	if len(skipped) != 0 {
		t.Fatalf("filter should exclude v1.4.0 from skipped too, got %v", skipped)
	}
}

func TestMergeBuildArgs(t *testing.T) {
	t.Parallel()

	if got := mergeBuildArgs(nil, nil); got != nil {
		t.Errorf("nil+nil should stay nil, got %v", got)
	}
	if got := mergeBuildArgs(map[string]string{"A": "1"}, nil); !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
		t.Errorf("base only: got %v", got)
	}
	if got := mergeBuildArgs(map[string]string{"A": "1", "B": "2"}, map[string]string{"B": "20", "C": "3"}); !reflect.DeepEqual(
		got, map[string]string{"A": "1", "B": "20", "C": "3"},
	) {
		t.Errorf("merge: got %v", got)
	}
}

func TestOverrideString(t *testing.T) {
	t.Parallel()

	if got := overrideString("def", ""); got != "def" {
		t.Errorf("empty override should keep default: got %q", got)
	}
	if got := overrideString("def", "ovr"); got != "ovr" {
		t.Errorf("non-empty override should win: got %q", got)
	}
	if got := overrideString("", "ovr"); got != "ovr" {
		t.Errorf("empty default with override: got %q", got)
	}
}

func TestDockerfilePath(t *testing.T) {
	t.Parallel()

	if got, want := dockerfilePath(buildJob{pluginDir: "/r/grpc/go"}), "/r/grpc/go/Dockerfile"; got != want {
		t.Errorf("unset: expected %q, got %q", want, got)
	}
	if got, want := dockerfilePath(buildJob{pluginDir: "/r/grpc/go", dockerfile: "Dockerfile.legacy"}), "/r/grpc/go/Dockerfile.legacy"; got != want {
		t.Errorf("relative: expected %q, got %q", want, got)
	}
	if got, want := dockerfilePath(buildJob{pluginDir: "/r/grpc/go", dockerfile: "/abs/Dockerfile"}), "/abs/Dockerfile"; got != want {
		t.Errorf("absolute: expected %q, got %q", want, got)
	}
}

func TestBuildJobCached(t *testing.T) {
	t.Parallel()

	t.Run("regular file is cached", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, pluginBinaryName), []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !(buildJob{outputDir: dir}).cached() {
			t.Error("expected a regular plugin file to count as cached")
		}
	})

	// A directory named "plugin" is what a Dockerfile produces when it copies
	// a directory instead of the entrypoint; it must not pass for a build.
	t.Run("directory is not cached", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, pluginBinaryName), 0o755); err != nil {
			t.Fatal(err)
		}
		if (buildJob{outputDir: dir}).cached() {
			t.Error("expected a plugin directory not to count as cached")
		}
	})

	t.Run("missing is not cached", func(t *testing.T) {
		t.Parallel()
		if (buildJob{outputDir: t.TempDir()}).cached() {
			t.Error("expected a missing plugin not to count as cached")
		}
	})
}

func TestNormalizeOutputRejectsDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, pluginBinaryName), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := normalizeOutput(buildJob{outputDir: dir})
	if !errors.Is(err, ErrNoOutputBinary) {
		t.Errorf("expected ErrNoOutputBinary, got %v", err)
	}
}
