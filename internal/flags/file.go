package flags

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
)

var (
	_ flag.Value = (*File)(nil)
	_ io.Reader  = (*File)(nil)
)

// File for getting.
type File struct {
	DefaultPath string
	MaxSize     int64
	file        *bytes.Buffer
}

// String implements flag.Value.
func (f *File) String() string {
	return f.DefaultPath
}

// IsNil checks if the file is not set.
func (f *File) IsNil() bool {
	return f.file == nil
}

// Set implements flag.Value.
func (f *File) Set(s string) error {
	file, err := os.Open(s)
	if err != nil {
		return fmt.Errorf("os.Open: %w", err)
	}
	defer func() {
		closeErr := file.Close()
		if closeErr != nil {
			err = fmt.Errorf("originalErr: %w: file.Close: %w", err, closeErr)
		}
	}()

	buf, err := io.ReadAll(io.LimitReader(file, f.MaxSize))
	if err != nil {
		return fmt.Errorf("io.ReadAll: %w", err)
	}

	f.file = bytes.NewBuffer(buf)

	return nil
}

// Read implements io.ReadCloser.
func (f *File) Read(b []byte) (n int, err error) {
	return f.file.Read(b)
}
