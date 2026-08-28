package plugarchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeArchive builds a tar.gz file from the given entries for negative tests.
func writeArchive(t *testing.T, entries []*tar.Header, bodies map[string][]byte) string {
	t.Helper()

	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzWriter)
	for _, h := range entries {
		require.NoError(t, tarWriter.WriteHeader(h))
		if body, ok := bodies[h.Name]; ok {
			_, err := tarWriter.Write(body)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzWriter.Close())

	path := filepath.Join(t.TempDir(), "archive.tgz")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	return path
}

func TestPackUnpackRoundTrip(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "plugin"), []byte("#!/bin/sh\nexec ./app/tool\n"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "app", "tool"), []byte("sidecar-binary"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "app", "data.txt"), []byte("read-only data"), 0o644))
	require.NoError(t, os.Symlink("app/tool", filepath.Join(srcDir, "tool-link")))

	archivePath := filepath.Join(t.TempDir(), "plugin.tgz")
	archiveFile, err := os.Create(archivePath)
	require.NoError(t, err)
	require.NoError(t, PackDir(srcDir, archiveFile))
	require.NoError(t, archiveFile.Close())

	destDir := filepath.Join(t.TempDir(), "v1.0.0")
	require.NoError(t, Unpack(archivePath, destDir))

	entry, err := os.ReadFile(filepath.Join(destDir, "plugin"))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\nexec ./app/tool\n", string(entry))

	info, err := os.Stat(filepath.Join(destDir, "plugin"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "entrypoint must be executable")

	sidecar, err := os.ReadFile(filepath.Join(destDir, "app", "tool"))
	require.NoError(t, err)
	assert.Equal(t, "sidecar-binary", string(sidecar))

	sidecarInfo, err := os.Stat(filepath.Join(destDir, "app", "tool"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), sidecarInfo.Mode().Perm(), "sidecar exec bit must survive")

	dataInfo, err := os.Stat(filepath.Join(destDir, "app", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), dataInfo.Mode().Perm())

	linkTarget, err := os.Readlink(filepath.Join(destDir, "tool-link"))
	require.NoError(t, err)
	assert.Equal(t, "app/tool", linkTarget)
}

func TestUnpackReplacesExistingDir(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "plugin"), []byte("new"), 0o755))

	archivePath := filepath.Join(t.TempDir(), "plugin.tgz")
	archiveFile, err := os.Create(archivePath)
	require.NoError(t, err)
	require.NoError(t, PackDir(srcDir, archiveFile))
	require.NoError(t, archiveFile.Close())

	destDir := filepath.Join(t.TempDir(), "v1.0.0")
	require.NoError(t, os.MkdirAll(destDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(destDir, "stale-sidecar"), []byte("old"), 0o644))

	require.NoError(t, Unpack(archivePath, destDir))

	_, err = os.Stat(filepath.Join(destDir, "stale-sidecar"))
	assert.True(t, os.IsNotExist(err), "stale files must be gone after replace")

	data, err := os.ReadFile(filepath.Join(destDir, "plugin"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))
}

func TestUnpackRejectsTraversal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry string
	}{
		{name: "dotdot", entry: "../evil"},
		{name: "nested_dotdot", entry: "app/../../evil"},
		{name: "absolute", entry: "/etc/evil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			archivePath := writeArchive(t,
				[]*tar.Header{
					{Name: "plugin", Typeflag: tar.TypeReg, Mode: 0o755, Size: 2},
					{Name: tt.entry, Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
				},
				map[string][]byte{"plugin": []byte("ok"), tt.entry: []byte("evil")},
			)

			err := Unpack(archivePath, filepath.Join(t.TempDir(), "dest"))
			assert.ErrorIs(t, err, ErrUnsafePath)
		})
	}
}

func TestUnpackRequiresEntrypoint(t *testing.T) {
	t.Parallel()

	archivePath := writeArchive(t,
		[]*tar.Header{{Name: "not-a-plugin", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4}},
		map[string][]byte{"not-a-plugin": []byte("data")},
	)

	destDir := filepath.Join(t.TempDir(), "dest")
	err := Unpack(archivePath, destDir)
	require.ErrorIs(t, err, ErrMissingEntrypoint)

	_, statErr := os.Stat(destDir)
	assert.True(t, os.IsNotExist(statErr), "dest must not be created on failure")
}

func TestUnpackSkipsSpecialEntries(t *testing.T) {
	t.Parallel()

	archivePath := writeArchive(t,
		[]*tar.Header{
			{Name: "plugin", Typeflag: tar.TypeReg, Mode: 0o755, Size: 2},
			{Name: "dev-node", Typeflag: tar.TypeChar, Mode: 0o644},
			{Name: "pipe", Typeflag: tar.TypeFifo, Mode: 0o644},
		},
		map[string][]byte{"plugin": []byte("ok")},
	)

	destDir := filepath.Join(t.TempDir(), "dest")
	require.NoError(t, Unpack(archivePath, destDir))

	_, err := os.Lstat(filepath.Join(destDir, "dev-node"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Lstat(filepath.Join(destDir, "pipe"))
	assert.True(t, os.IsNotExist(err))
}

// TestUnpackRefusesEscapingSymlink covers where a symlink *points*, which is a
// separate question from where it is written.
//
// safeJoin has always sanitised the entry name, so an archive could not write
// outside the destination. It said nothing about link targets, so an archive
// could ship the required `plugin` entrypoint as a link to /bin/sh: the file
// lands inside plugins_dir, every containment check the registry makes is
// satisfied, and Unpack's entrypoint check passes too — it only chmods regular
// files and skips a symlink without comment. Executing the plugin then runs the
// shell.
func TestUnpackRefusesEscapingSymlink(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"absolute path outside":  "/bin/sh",
		"traversal outside":      "../../../../bin/sh",
		"traversal via subdir":   "../../etc/passwd",
		"absolute path anywhere": "/etc/passwd",
	}

	for name, linkname := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			archive := writeArchive(t, []*tar.Header{
				{Name: EntrypointName, Typeflag: tar.TypeSymlink, Linkname: linkname, Mode: 0o777},
			}, nil)

			dest := filepath.Join(t.TempDir(), "v1.0.0")
			err := Unpack(archive, dest)

			require.ErrorIs(t, err, ErrUnsafePath)
			assert.NoDirExists(t, dest)
		})
	}
}

// TestUnpackAllowsInternalSymlink is the other half: a link that stays inside
// the archive is legitimate and must keep working. Plugins ship them — a
// versioned binary with a stable name beside it, for one.
func TestUnpackAllowsInternalSymlink(t *testing.T) {
	t.Parallel()

	archive := writeArchive(t, []*tar.Header{
		{Name: "real-binary", Typeflag: tar.TypeReg, Mode: 0o755, Size: 2},
		{Name: EntrypointName, Typeflag: tar.TypeSymlink, Linkname: "real-binary", Mode: 0o777},
	}, map[string][]byte{"real-binary": []byte("hi")})

	dest := filepath.Join(t.TempDir(), "v1.0.0")
	require.NoError(t, Unpack(archive, dest))

	link, err := os.Readlink(filepath.Join(dest, EntrypointName))
	require.NoError(t, err)
	assert.Equal(t, "real-binary", link)

	// And it resolves to the file that travelled with it.
	body, err := os.ReadFile(filepath.Join(dest, EntrypointName))
	require.NoError(t, err)
	assert.Equal(t, "hi", string(body))
}

// TestUnpackAllowsSymlinkIntoSubdirectory guards the containment arithmetic
// itself: a link one directory deep pointing back up to a sibling stays inside
// the archive and must be allowed, even though its target string starts with
// "..".
func TestUnpackAllowsSymlinkIntoSubdirectory(t *testing.T) {
	t.Parallel()

	archive := writeArchive(t, []*tar.Header{
		{Name: EntrypointName, Typeflag: tar.TypeReg, Mode: 0o755, Size: 2},
		{Name: "lib/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "lib/link", Typeflag: tar.TypeSymlink, Linkname: "../plugin", Mode: 0o777},
	}, map[string][]byte{EntrypointName: []byte("hi")})

	dest := filepath.Join(t.TempDir(), "v1.0.0")
	require.NoError(t, Unpack(archive, dest))

	link, err := os.Readlink(filepath.Join(dest, "lib", "link"))
	require.NoError(t, err)
	assert.Equal(t, "../plugin", link)
}
