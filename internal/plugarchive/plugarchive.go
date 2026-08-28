// Package plugarchive packs and unpacks plugin version directories as
// tar.gz archives — the unit of storage for plugin delivery via S3.
// An archive contains the full contents of plugins/{group}/{name}/{version}/:
// the required `plugin` entrypoint plus optional sidecar files.
package plugarchive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// EntrypointName is the required executable name inside every plugin archive.
const EntrypointName = "plugin"

// ArchiveName is the file name a packed plugin version directory is stored
// under, both as an S3 object and on disk.
const ArchiveName = "plugin.tgz"

// Permissions of an unpacked plugin.
//
// gosec wants 0750/0600 or less, but these are deliberately world-readable and
// traversable: the service may run under a different user than the one that
// unpacked the archive, and a plugin is useless unless its directory can be
// traversed and its entrypoint executed. This matches the layout that
// `plugins build` produces. The same reasoning covers the file modes restored
// verbatim from the archive.
const (
	dirPermissions        = 0o755
	entrypointPermissions = 0o755
	// ownerAccess is OR-ed into restored directory modes so the extracting
	// process can always descend into what it just created.
	ownerAccess = 0o700
)

// Package errors.
var (
	ErrUnsafePath        = errors.New("unsafe path in archive")
	ErrMissingEntrypoint = errors.New("archive has no plugin entrypoint")
)

// PackDir writes a tar.gz archive of the contents of dirPath to w.
// Entry names are relative to dirPath; file modes (including exec bits) are
// preserved and symlinks are stored verbatim.
func PackDir(dirPath string, w io.Writer) error {
	gzWriter := gzip.NewWriter(w)
	tarWriter := tar.NewWriter(gzWriter)

	walkErr := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == dirPath {
			return nil
		}

		return packEntry(tarWriter, dirPath, path, info)
	})
	if walkErr != nil {
		return fmt.Errorf("filepath.Walk: %w", walkErr)
	}

	err := tarWriter.Close()
	if err != nil {
		return fmt.Errorf("tarWriter.Close: %w", err)
	}
	err = gzWriter.Close()
	if err != nil {
		return fmt.Errorf("gzWriter.Close: %w", err)
	}

	return nil
}

// packEntry writes one file, directory or symlink into the archive.
func packEntry(tarWriter *tar.Writer, dirPath string, path string, info os.FileInfo) error {
	rel, err := filepath.Rel(dirPath, path)
	if err != nil {
		return fmt.Errorf("filepath.Rel: %w", err)
	}
	rel = filepath.ToSlash(rel)

	var linkTarget string
	if info.Mode()&os.ModeSymlink != 0 {
		linkTarget, err = os.Readlink(path)
		if err != nil {
			return fmt.Errorf("os.Readlink: %w", err)
		}
	}

	header, err := tar.FileInfoHeader(info, linkTarget)
	if err != nil {
		return fmt.Errorf("tar.FileInfoHeader: %w", err)
	}
	header.Name = rel
	if info.IsDir() {
		header.Name += "/"
	}

	err = tarWriter.WriteHeader(header)
	if err != nil {
		return fmt.Errorf("tarWriter.WriteHeader: %w", err)
	}

	if !info.Mode().IsRegular() {
		return nil
	}

	// gosec flags Walk-derived paths for symlink TOCTOU. dirPath is a plugin
	// version directory this process just built or unpacked itself, not
	// attacker-supplied, and entries are only read — never written through.
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("os.Open: %w", err)
	}
	defer func() { _ = file.Close() }()

	_, err = io.Copy(tarWriter, file)
	if err != nil {
		return fmt.Errorf("io.Copy: %w", err)
	}

	return nil
}

// Unpack extracts the tar.gz archive at archivePath into destDir, replacing
// destDir atomically: entries are extracted into a unique sibling temp
// directory which is then renamed over destDir. The archive must contain the
// `plugin` entrypoint at its root; it is made executable.
//
// Entry names containing absolute paths or ".." are rejected, and so are
// symlinks pointing anywhere outside the directory being unpacked into —
// otherwise an archive could ship `plugin` as a link to /bin/sh and satisfy
// every containment check the registry makes. Regular files are never written
// through existing symlinks (each parent directory is created as a real
// directory).
func Unpack(archivePath string, destDir string) error {
	parent := filepath.Dir(destDir)
	err := os.MkdirAll(parent, dirPermissions)
	if err != nil {
		return fmt.Errorf("os.MkdirAll: %w", err)
	}

	tmpDir, err := os.MkdirTemp(parent, filepath.Base(destDir)+".tmp-*")
	if err != nil {
		return fmt.Errorf("os.MkdirTemp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// MkdirTemp creates 0700; match the 0755 layout produced by plugins build.
	err = os.Chmod(tmpDir, dirPermissions)
	if err != nil {
		return fmt.Errorf("os.Chmod tmpdir: %w", err)
	}

	err = extractTo(archivePath, tmpDir)
	if err != nil {
		return err
	}

	entrypoint := filepath.Join(tmpDir, EntrypointName)
	info, err := os.Lstat(entrypoint)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrMissingEntrypoint, archivePath)
	}
	if info.Mode().IsRegular() {
		err = os.Chmod(entrypoint, entrypointPermissions)
		if err != nil {
			return fmt.Errorf("os.Chmod entrypoint: %w", err)
		}
	}

	err = os.RemoveAll(destDir)
	if err != nil {
		return fmt.Errorf("os.RemoveAll dest: %w", err)
	}

	err = os.Rename(tmpDir, destDir)
	if err != nil {
		return fmt.Errorf("os.Rename: %w", err)
	}

	return nil
}

// extractTo extracts all archive entries into destDir with path sanitation.
func extractTo(archivePath string, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("os.Open archive: %w", err)
	}
	defer func() { _ = file.Close() }()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("gzip.NewReader: %w", err)
	}
	defer func() { _ = gzReader.Close() }()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tarReader.Next: %w", err)
		}

		target, err := safeJoin(destDir, header.Name)
		if err != nil {
			return err
		}

		err = extractEntry(tarReader, header, destDir, target)
		if err != nil {
			return err
		}
	}
}

// extractEntry materializes a single tar entry at target.
func extractEntry(tarReader *tar.Reader, header *tar.Header, root, target string) error {
	switch header.Typeflag {
	case tar.TypeDir:
		err := os.MkdirAll(target, header.FileInfo().Mode().Perm()|ownerAccess)
		if err != nil {
			return fmt.Errorf("os.MkdirAll: %w", err)
		}
	case tar.TypeReg:
		return extractRegular(tarReader, header, target)
	case tar.TypeSymlink:
		return extractSymlink(root, header, target)
	default:
		// Skip devices, fifos, hard links and other special entries: plugin
		// artifacts never need them and materializing them is a risk.
	}

	return nil
}

// extractRegular writes a regular file entry, never following an existing
// symlink at the target path.
func extractRegular(tarReader *tar.Reader, header *tar.Header, target string) error {
	err := os.MkdirAll(filepath.Dir(target), dirPermissions)
	if err != nil {
		return fmt.Errorf("os.MkdirAll: %w", err)
	}

	out, err := os.OpenFile(
		target,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW,
		header.FileInfo().Mode().Perm(),
	)
	if err != nil {
		return fmt.Errorf("os.OpenFile: %w", err)
	}

	_, err = io.Copy(out, tarReader)
	closeErr := out.Close()

	if err != nil {
		return fmt.Errorf("io.Copy: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("out.Close: %w", closeErr)
	}

	return nil
}

// extractSymlink recreates a symlink entry verbatim.
// extractSymlink materialises a symlink entry, refusing one that points outside
// the directory being unpacked into.
//
// safeJoin already sanitises where a link is *written*; this is the other half —
// where it *points*. Without it an archive could ship `plugin` as a symlink to
// /bin/sh: it lands at a path inside plugins_dir, so every containment check
// upstream is satisfied, and Unpack's own entrypoint check passes too (it only
// chmods when the entry is a regular file, and skips a symlink without
// comment). Executing it then runs the shell with the plugin's arguments.
//
// Checked lexically against the unpack root rather than with EvalSymlinks: the
// archive is being written out right now, so intermediate links may not resolve
// yet, and a link to a path that does not exist is still a link that will
// resolve once something creates it.
func extractSymlink(root string, header *tar.Header, target string) error {
	err := checkSymlinkTarget(root, target, header.Linkname)
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(target), dirPermissions)
	if err != nil {
		return fmt.Errorf("os.MkdirAll: %w", err)
	}

	_ = os.Remove(target)

	err = os.Symlink(header.Linkname, target)
	if err != nil {
		return fmt.Errorf("os.Symlink: %w", err)
	}

	return nil
}

// checkSymlinkTarget reports whether linkname, resolved from the directory
// holding target, stays inside root. An absolute linkname never does.
func checkSymlinkTarget(root, target, linkname string) error {
	if linkname == "" {
		return fmt.Errorf("%w: empty symlink target for %s", ErrUnsafePath, target)
	}

	cleanedLink := filepath.Clean(filepath.FromSlash(linkname))
	if filepath.IsAbs(cleanedLink) {
		return fmt.Errorf("%w: symlink %s points outside the archive: %s", ErrUnsafePath, target, linkname)
	}

	// Relative links resolve against the directory the link itself sits in.
	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), cleanedLink))

	cleanedRoot := filepath.Clean(root)
	if resolved != cleanedRoot && !strings.HasPrefix(resolved, cleanedRoot+string(filepath.Separator)) {
		return fmt.Errorf("%w: symlink %s escapes the archive: %s", ErrUnsafePath, target, linkname)
	}

	return nil
}

// safeJoin joins destDir with the archive entry name, rejecting absolute
// paths and path traversal.
func safeJoin(destDir string, name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrUnsafePath, name)
	}

	return filepath.Join(destDir, cleaned), nil
}
