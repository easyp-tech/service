package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/easyp-tech/service/internal/plugarchive"
)

// writePluginTree lays out {group}/{name}/{version}/{leaf} under a temp dir.
func writePluginTree(t *testing.T, leaf string, coords ...pluginInfo) string {
	t.Helper()

	root := t.TempDir()
	for _, plg := range coords {
		dir := filepath.Join(root, plg.group, plg.name, plg.version)
		err := os.MkdirAll(dir, dirPermissions)
		if err != nil {
			t.Fatalf("os.MkdirAll: %v", err)
		}
		err = os.WriteFile(filepath.Join(dir, leaf), []byte("payload"), archivePermissions)
		if err != nil {
			t.Fatalf("os.WriteFile: %v", err)
		}
	}

	return root
}

// wantOnly fails unless got holds exactly the expected plugins.
func wantOnly(t *testing.T, got []pluginInfo, want ...pluginInfo) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestScanPushSourcePacked(t *testing.T) {
	t.Parallel()

	plg := pluginInfo{group: "grpc", name: "go", version: "v1.6.2"}
	root := writePluginTree(t, plugarchive.ArchiveName, plg)

	got, err := scanPushSource(pushOptions{scanPath: root, packed: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantOnly(t, got, plg)
}

func TestScanPushSourceBuilt(t *testing.T) {
	t.Parallel()

	plg := pluginInfo{group: "grpc", name: "go", version: "v1.6.2"}
	root := writePluginTree(t, pluginEntrypointName, plg)

	got, err := scanPushSource(pushOptions{scanPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantOnly(t, got, plg)
}

// A packed tree holds no entrypoints, so a push without --packed must find
// nothing there rather than silently uploading a partial set.
func TestScanPushSourceIgnoresOtherLayout(t *testing.T) {
	t.Parallel()

	plg := pluginInfo{group: "grpc", name: "go", version: "v1.6.2"}
	root := writePluginTree(t, plugarchive.ArchiveName, plg)

	got, err := scanPushSource(pushOptions{scanPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantOnly(t, got)
}

func TestScanPushSourcePackedFilter(t *testing.T) {
	t.Parallel()

	plg := pluginInfo{group: "grpc", name: "go", version: "v1.6.2"}
	other := pluginInfo{group: "protocolbuffers", name: "go", version: "v1.36.0"}
	root := writePluginTree(t, plugarchive.ArchiveName, plg, other)

	got, err := scanPushSource(pushOptions{scanPath: root, packed: true, filter: "grpc/*"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantOnly(t, got, plg)
}

func TestResolveArchivePacked(t *testing.T) {
	t.Parallel()

	plg := pluginInfo{group: "grpc", name: "go", version: "v1.6.2"}
	root := writePluginTree(t, plugarchive.ArchiveName, plg)
	want := filepath.Join(root, plg.group, plg.name, plg.version, plugarchive.ArchiveName)

	got, cleanup, err := resolveArchive(pushOptions{scanPath: root, packed: true}, plg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cleanup()

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}

	// The archive belongs to the packed tree: uploading it must not consume it.
	_, err = os.Stat(want)
	if err != nil {
		t.Fatalf("archive removed after cleanup: %v", err)
	}
}
