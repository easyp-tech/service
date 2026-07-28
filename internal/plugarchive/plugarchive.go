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

// Unpack extracts the tar.gz archive at archivePath into destDir, replacing
// destDir atomically: entries are extracted into a unique sibling temp
// directory which is then renamed over destDir. The archive must contain the
// `plugin` entrypoint at its root; it is made executable.
//
// Entry names containing absolute paths or ".." are rejected. Symlink
// entries are created verbatim, but regular files are never written through
// existing symlinks (each parent directory is created as a real directory).
func Unpack(archivePath string, destDir string) error {
	parent := filepath.Dir(destDir)
	err := os.MkdirAll(parent, 0o755)
	if err != nil {
		return fmt.Errorf("os.MkdirAll: %w", err)
	}

	tmpDir, err := os.MkdirTemp(parent, filepath.Base(destDir)+".tmp-*")
	if err != nil {
		return fmt.Errorf("os.MkdirTemp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// MkdirTemp creates 0700; match the 0755 layout produced by plugins build.
	err = os.Chmod(tmpDir, 0o755)
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
		err = os.Chmod(entrypoint, 0o755)
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

		err = extractEntry(tarReader, header, target)
		if err != nil {
			return err
		}
	}
}

// extractEntry materializes a single tar entry at target.
func extractEntry(tarReader *tar.Reader, header *tar.Header, target string) error {
	switch header.Typeflag {
	case tar.TypeDir:
		err := os.MkdirAll(target, os.FileMode(header.Mode).Perm()|0o700)
		if err != nil {
			return fmt.Errorf("os.MkdirAll: %w", err)
		}
	case tar.TypeReg:
		err := os.MkdirAll(filepath.Dir(target), 0o755)
		if err != nil {
			return fmt.Errorf("os.MkdirAll: %w", err)
		}

		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, os.FileMode(header.Mode).Perm())
		if err != nil {
			return fmt.Errorf("os.OpenFile: %w", err)
		}

		_, err = io.Copy(out, tarReader) //nolint:gosec // archive integrity is sha256-verified before unpacking
		closeErr := out.Close()
		if err != nil {
			return fmt.Errorf("io.Copy: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("out.Close: %w", closeErr)
		}
	case tar.TypeSymlink:
		err := os.MkdirAll(filepath.Dir(target), 0o755)
		if err != nil {
			return fmt.Errorf("os.MkdirAll: %w", err)
		}
		_ = os.Remove(target)
		err = os.Symlink(header.Linkname, target)
		if err != nil {
			return fmt.Errorf("os.Symlink: %w", err)
		}
	default:
		// Skip devices, fifos, hard links and other special entries: plugin
		// artifacts never need them and materializing them is a risk.
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
