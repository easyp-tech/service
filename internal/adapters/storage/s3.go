// Package storage provides object storage adapters for plugin binaries.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/monitor"
)

// cacheDirPermissions is the mode of the local plugin cache directory. It is
// deliberately traversable by other users: the plugin unpacked into it has to
// stay executable even when the service runs under a different account.
const cacheDirPermissions = 0o755

// ErrBucketRequired is returned when no bucket was configured.
var ErrBucketRequired = errors.New("s3 bucket cannot be empty")

var _ core.BinaryStorage = &S3Storage{}

// S3Options configures an S3-compatible binary storage backend.
type S3Options struct {
	// Endpoint overrides the AWS endpoint, e.g. "http://localhost:9000" for
	// MinIO/RustFS. Empty means the default AWS endpoint resolution.
	Endpoint string
	// Bucket is the bucket holding plugin binaries. Required.
	Bucket string
	// Region is the signing region.
	Region string
	// Prefix is prepended to every object key.
	Prefix string
	// AccessKeyID and SecretAccessKey configure static credentials.
	// When empty, the default AWS credential chain is used.
	AccessKeyID     string
	SecretAccessKey string
	// ForcePathStyle enables path-style addressing (required by MinIO/RustFS).
	ForcePathStyle bool
}

// S3Storage implements core.BinaryStorage using the AWS S3-compatible API.
type S3Storage struct {
	client *s3.Client
	bucket string
	prefix string
}

// NewS3Storage creates a new S3Storage instance.
func NewS3Storage(ctx context.Context, opts S3Options) (*S3Storage, error) {
	if opts.Bucket == "" {
		return nil, ErrBucketRequired
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(opts.Region),
	}

	if opts.AccessKeyID != "" && opts.SecretAccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(opts.AccessKeyID, opts.SecretAccessKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("awsconfig.LoadDefaultConfig: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if opts.Endpoint != "" {
			o.BaseEndpoint = aws.String(opts.Endpoint)
		}
		o.UsePathStyle = opts.ForcePathStyle
	})

	prefix := opts.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	return &S3Storage{
		client: client,
		bucket: opts.Bucket,
		prefix: prefix,
	}, nil
}

// Download retrieves an object from S3 and atomically writes it to localPath.
func (s *S3Storage) Download(ctx context.Context, key string, localPath string) error {
	fullKey := s.formatKey(key)

	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return fmt.Errorf("s3.GetObject: %w", err)
	}
	defer func() {
		closeErr := output.Body.Close()
		if closeErr != nil {
			monitor.FromContext(ctx).Error("output.Body.Close", "error", closeErr)
		}
	}()

	dir := filepath.Dir(localPath)
	err = os.MkdirAll(dir, cacheDirPermissions)
	if err != nil {
		return fmt.Errorf("os.MkdirAll: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "plugin-*.tmp")
	if err != nil {
		return fmt.Errorf("os.CreateTemp: %w", err)
	}
	tmpPath := tmpFile.Name()

	_, err = io.Copy(tmpFile, output.Body)
	closeErr := tmpFile.Close()

	if err != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("io.Copy: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("tmpFile.Close: %w", closeErr)
	}

	err = os.Rename(tmpPath, localPath)
	if err != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("os.Rename: %w", err)
	}

	return nil
}

// Open returns a stream of the object under key and its size.
// The caller must close the returned reader.
func (s *S3Storage) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	fullKey := s.formatKey(key)

	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("s3.GetObject: %w", err)
	}

	return output.Body, aws.ToInt64(output.ContentLength), nil
}

// UploadFile stores the file at localPath under key.
// Used by the CLI push command only; the service never uploads.
func (s *S3Storage) UploadFile(ctx context.Context, key string, localPath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("os.Open: %w", err)
	}
	defer func() { _ = file.Close() }()

	fullKey := s.formatKey(key)

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
		Body:   file,
	})
	if err != nil {
		return fmt.Errorf("s3.PutObject: %w", err)
	}

	return nil
}

// Exists reports whether a key exists in S3.
func (s *S3Storage) Exists(ctx context.Context, key string) (bool, error) {
	fullKey := s.formatKey(key)

	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return false, nil
		}

		return false, fmt.Errorf("s3.HeadObject: %w", err)
	}

	return true, nil
}

// Delete removes a key from S3.
func (s *S3Storage) Delete(ctx context.Context, key string) error {
	fullKey := s.formatKey(key)

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return fmt.Errorf("s3.DeleteObject: %w", err)
	}

	return nil
}

// formatKey applies the configured prefix to an object key.
func (s *S3Storage) formatKey(key string) string {
	cleanKey := strings.TrimPrefix(key, "/")

	return s.prefix + cleanKey
}
