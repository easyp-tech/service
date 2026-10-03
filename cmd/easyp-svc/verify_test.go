package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/plugarchive"
)

// newBundle lays out a built version directory with an entrypoint.
func newBundle(t *testing.T) buildJob {
	t.Helper()

	out := filepath.Join(t.TempDir(), "plugins", "bufbuild", "connect-kotlin", "v0.1.10")
	require.NoError(t, os.MkdirAll(out, dirPermissions))
	require.NoError(t, os.WriteFile(filepath.Join(out, plugarchive.EntrypointName), []byte("#!/bin/sh\n"), binPermissions))

	return buildJob{group: "bufbuild", name: "connect-kotlin", version: "v0.1.10", outputDir: out}
}

// TestRelativizeSymlinksKeepsWhatTheBundleCarries pins the difference between
// the two kinds of absolute link an image dump holds. One that points at a file
// the bundle carries is rewritten so it still resolves once unpacked anywhere —
// a Debian JRE reads its security config through exactly such a link, and
// dropping it broke the JVM. One that points at nothing the bundle has is
// removed, which is what the service would do with it anyway.
func TestRelativizeSymlinksKeepsWhatTheBundleCarries(t *testing.T) {
	t.Parallel()

	job := newBundle(t)
	root := job.outputDir

	conf := filepath.Join(root, "etc", "java-17-openjdk", "security", "java.security")
	require.NoError(t, os.MkdirAll(filepath.Dir(conf), dirPermissions))
	require.NoError(t, os.WriteFile(conf, []byte("security"), 0o644))

	link := filepath.Join(root, "usr", "lib", "jvm", "java-17", "conf", "security", "java.security")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), dirPermissions))
	require.NoError(t, os.Symlink("/etc/java-17-openjdk/security/java.security", link))

	dangling := filepath.Join(root, "usr", "share", "zoneinfo", "localtime")
	require.NoError(t, os.MkdirAll(filepath.Dir(dangling), dirPermissions))
	require.NoError(t, os.Symlink("/etc/localtime", dangling))

	relative := filepath.Join(root, "lib-alias")
	require.NoError(t, os.Symlink("usr/lib", relative))

	require.NoError(t, relativizeSymlinks(root))

	target, err := os.Readlink(link)
	require.NoError(t, err)
	assert.False(t, filepath.IsAbs(target), "a link into the bundle must become relative, got %q", target)

	body, err := os.ReadFile(link)
	require.NoError(t, err)
	assert.Equal(t, "security", string(body), "and still resolve to the same file")

	_, err = os.Lstat(dangling)
	assert.True(t, os.IsNotExist(err), "a link to nothing the bundle carries is removed")

	kept, err := os.Readlink(relative)
	require.NoError(t, err)
	assert.Equal(t, "usr/lib", kept, "relative links are left alone")
}

// TestVerifyRefusesWhatTheServiceWouldRefuse is the round trip's reason to
// exist: an archive the service's unpacker rejects fails the build here, on the
// machine that made it, instead of on the first GenerateCode after a push.
func TestVerifyRefusesWhatTheServiceWouldRefuse(t *testing.T) {
	t.Parallel()

	job := newBundle(t)
	require.NoError(t, os.Symlink("../../../../../etc/passwd", filepath.Join(job.outputDir, "escape")))

	_, err := verifyBundle(t.Context(), job, verifyOptions{smoke: false})

	require.ErrorIs(t, err, plugarchive.ErrUnsafePath)
}

// TestSmokeRunsUnpackedBundleWithItsParameter checks what the smoke step is
// handed: the bundle as the service would unpack it, not the build output, and
// the parameter from plugin.yaml.
func TestSmokeRunsUnpackedBundleWithItsParameter(t *testing.T) {
	t.Parallel()

	job := newBundle(t)
	job.smoke = smokeConfig{Parameter: "extern_path=.=crate::proto"}

	var gotDir, gotParam, gotImage string

	_, err := verifyBundle(t.Context(), job, verifyOptions{
		smoke:        true,
		runtimeImage: defaultRuntimeImage,
		run: func(_ context.Context, image, dir, parameter string) ([]byte, error) {
			gotImage, gotDir, gotParam = image, dir, parameter
			assert.FileExists(t, filepath.Join(dir, plugarchive.EntrypointName))

			return nil, nil
		},
	})
	require.NoError(t, err)

	assert.Equal(t, defaultRuntimeImage, gotImage)
	assert.Equal(t, "extern_path=.=crate::proto", gotParam)
	assert.NotEqual(t, job.outputDir, gotDir, "the smoke run must see the unpacked archive, not the build output")
	assert.NoDirExists(t, gotDir, "the unpacked copy is removed afterwards")
}

// TestSmokeSkipIsHonoured covers the escape hatch: a plugin.yaml that says skip
// is not run, though its archive is still checked.
func TestSmokeSkipIsHonoured(t *testing.T) {
	t.Parallel()

	job := newBundle(t)
	job.smoke = smokeConfig{Skip: true}

	_, err := verifyBundle(t.Context(), job, verifyOptions{
		smoke: true,
		run: func(context.Context, string, string, string) ([]byte, error) {
			t.Fatal("a plugin marked skip must not be run")

			return nil, nil
		},
	})
	require.NoError(t, err)
}

// TestFailedSmokeLeavesNothingToShip pins why a failed version is emptied
// rather than merely reported: push, register and the build cache all key on
// the entrypoint being there. Left in place, a plugin that cannot start would
// be marked failed by this build and shipped by the next command.
func TestFailedSmokeLeavesNothingToShip(t *testing.T) {
	t.Parallel()

	job := newBundle(t)

	_, err := verifyBundle(t.Context(), job, verifyOptions{
		smoke: true,
		run: func(context.Context, string, string, string) ([]byte, error) {
			return []byte("exec: /nodejs/bin/node: not found"), errors.New("exit status 127")
		},
	})
	require.ErrorIs(t, err, ErrSmokeFailed)

	require.NoError(t, discardFailedBuild(job))

	assert.False(t, job.cached(), "a failed version must be rebuilt next time, not taken from cache")

	scanRoot := filepath.Dir(filepath.Dir(filepath.Dir(job.outputDir)))
	found, err := scanPlugins(scanRoot, "")
	require.NoError(t, err)
	assert.Empty(t, found, "push and register must not find a version that failed verification")
}

// TestSmokeImageMatchesServiceBase keeps the smoke test honest. A plugin is a
// dynamically linked process running on the service image's libraries, so a
// smoke run in any other base proves nothing — the glibc 2.38 failures were
// exactly a plugin built for one Debian and run on another.
func TestSmokeImageMatchesServiceBase(t *testing.T) {
	t.Parallel()

	dockerfile, err := os.ReadFile("../../Dockerfile")
	require.NoError(t, err)

	froms := regexp.MustCompile(`(?m)^FROM\s+(?:--\S+\s+)*(\S+)`).FindAllStringSubmatch(string(dockerfile), -1)
	require.NotEmpty(t, froms)

	runtimeBase := froms[len(froms)-1][1]
	assert.Equal(t, defaultRuntimeImage, runtimeBase,
		"plugins build smoke-tests in %s but the service runs on %s", defaultRuntimeImage, runtimeBase)
}
