package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/plugarchive"
)

// defaultRuntimeImage is the image a built plugin is smoke-tested in. It has to
// be the base of the service's own runtime stage: a plugin is a dynamically
// linked process that runs on that image's libraries, and a smoke test anywhere
// else proves nothing about the service. TestSmokeImageMatchesServiceBase keeps
// the two in step.
const defaultRuntimeImage = "debian:trixie-slim"

const (
	// smokeTimeout bounds one smoke run. JVM plugins start in a second or two;
	// the image pull on first use is what takes time, and it happens once.
	smokeTimeout = 3 * time.Minute

	// smokeUser is the service image's user. A plugin that only works as root
	// does not work in the service.
	smokeUser = "65532:65532"

	// stderrTail bounds how much of a failing plugin's stderr reaches the build
	// log line; the full output is in build.log.
	stderrTail = 600
)

// ErrSmokeFailed marks a plugin that built but does not run the way the
// service runs it.
var ErrSmokeFailed = errors.New("plugin does not run in the service runtime")

// smokeConfig is the optional `smoke` block of a plugin.yaml.
//
// Parameter is passed as CodeGeneratorRequest.parameter, for plugins that
// refuse to run without options. Skip turns the run off for a plugin that
// cannot be exercised with a synthetic request; it is meant to carry a comment
// saying why, since it removes the only check that the archive works.
type smokeConfig struct {
	Parameter string `yaml:"parameter"`
	Skip      bool   `yaml:"skip"`
}

// smokeRunner runs an unpacked bundle the way the service would and reports
// whether it produced a CodeGeneratorResponse. A variable so tests can verify
// what happens around a failure without a Docker daemon.
type smokeRunner func(ctx context.Context, image, bundleDir, parameter string) ([]byte, error)

// verifyOptions controls the checks run on every freshly built version.
type verifyOptions struct {
	runtimeImage string
	smoke        bool
	run          smokeRunner
}

// verifyBundle makes a built version directory what the service can use, or
// says why it cannot be.
//
// Three steps, each one a way a catalogue plugin was found broken in the field:
// absolute symlinks are made relative (the service skips absolute ones, and a
// Debian JRE needs its /etc/java-17-openjdk links to keep working); the bundle
// is packed and unpacked with the code the service uses, so an archive the
// service would refuse never leaves this machine; and the entrypoint is run in
// the service's base image with an empty environment, as the service's user,
// with a private working directory — which is what caught 22 plugins whose
// wrapper exec'd a path that only existed inside their build image.
func verifyBundle(ctx context.Context, job buildJob, opts verifyOptions) ([]byte, error) {
	err := relativizeSymlinks(job.outputDir)
	if err != nil {
		return nil, fmt.Errorf("normalising symlinks: %w", err)
	}

	unpacked, cleanup, err := roundTrip(job.outputDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if !opts.smoke || job.smoke.Skip {
		return nil, nil
	}

	run := opts.run
	if run == nil {
		run = dockerSmoke
	}

	out, err := run(ctx, opts.runtimeImage, unpacked, job.smoke.Parameter)
	if err != nil {
		return out, fmt.Errorf("%w: %w", ErrSmokeFailed, err)
	}

	return out, nil
}

// relativizeSymlinks rewrites every absolute symlink under root whose target
// exists inside root as the equivalent relative link, and removes the rest.
//
// A build dumps an image filesystem, where absolute links are normal and
// resolve against the image root. Unpacked into plugins_dir the same link would
// resolve against the service's root instead, so the service skips them; the
// ones that point at something the bundle carries are worth keeping, and
// rewriting them here is the only place that can.
func relativizeSymlinks(root string) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}

		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("os.Readlink %s: %w", path, err)
		}

		if !filepath.IsAbs(target) {
			return nil
		}

		return relativizeOne(root, path, target)
	})
	if err != nil {
		return fmt.Errorf("walking %s: %w", root, err)
	}

	return nil
}

// relativizeOne replaces one absolute link at path with a relative one, or
// removes it when root holds nothing at its target.
func relativizeOne(root, path, target string) error {
	inside := filepath.Join(root, target)

	_, statErr := os.Lstat(inside)
	if statErr != nil {
		err := os.Remove(path)
		if err != nil {
			return fmt.Errorf("os.Remove %s: %w", path, err)
		}

		return nil
	}

	rel, err := filepath.Rel(filepath.Dir(path), inside)
	if err != nil {
		return fmt.Errorf("filepath.Rel %s: %w", path, err)
	}

	err = os.Remove(path)
	if err != nil {
		return fmt.Errorf("os.Remove %s: %w", path, err)
	}

	err = os.Symlink(rel, path)
	if err != nil {
		return fmt.Errorf("os.Symlink %s: %w", path, err)
	}

	return nil
}

// roundTrip packs dir and unpacks the result with plugarchive, the code the
// service runs on every download, and returns where it landed.
func roundTrip(dir string) (string, func(), error) {
	archive, err := packPluginDir(dir)
	if err != nil {
		return "", func() {}, err
	}
	defer func() { _ = os.Remove(archive) }()

	parent, err := os.MkdirTemp("", "plugin-verify-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("os.MkdirTemp: %w", err)
	}

	cleanup := func() { _ = os.RemoveAll(parent) }
	unpacked := filepath.Join(parent, "bundle")

	err = plugarchive.Unpack(archive, unpacked)
	if err != nil {
		cleanup()

		return "", func() {}, fmt.Errorf("the service would refuse this archive: %w", err)
	}

	return unpacked, cleanup, nil
}

// dockerSmoke runs the bundle's entrypoint in image with the request on stdin.
//
// `env -i` empties the environment, as the service does for every plugin; the
// working directory is /tmp, writable by the service user, standing in for the
// private one the service creates per run. The bundle is mounted read-only.
func dockerSmoke(ctx context.Context, image, bundleDir, parameter string) ([]byte, error) {
	request, err := proto.Marshal(smokeRequest(parameter))
	if err != nil {
		return nil, fmt.Errorf("proto.Marshal: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, smokeTimeout)
	defer cancel()

	//nolint:gosec // every argument is ours; bundleDir is a temp dir this process created
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i",
		"--user", smokeUser,
		"--workdir", "/tmp",
		"-v", bundleDir+":/p:ro",
		"--entrypoint", "/usr/bin/env",
		image,
		"-i", "/p/"+plugarchive.EntrypointName,
	)

	var stdout, stderr bytes.Buffer

	cmd.Stdin = bytes.NewReader(request)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		return stderr.Bytes(), fmt.Errorf("%w: %s", err, tail(stderr.String()))
	}

	var response pluginpb.CodeGeneratorResponse

	err = proto.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		return stderr.Bytes(), fmt.Errorf("stdout is not a CodeGeneratorResponse: %w", err)
	}

	return stderr.Bytes(), nil
}

// tail returns the last stderrTail bytes of output on one line.
func tail(output string) string {
	output = strings.Join(strings.Fields(output), " ")
	if len(output) > stderrTail {
		return "…" + output[len(output)-stderrTail:]
	}

	return output
}

// smokeRequest is a request every protoc plugin should be able to answer: one
// proto3 file with a message and a service, and the options the Go and Java
// generators insist on. A plugin that also needs its own options gets them as
// parameter, from plugin.yaml.
//
// It asserts nothing about what comes back beyond its being a response — an
// `error` in the response still counts as running, because that is a plugin
// rejecting a synthetic request, not a plugin that cannot start.
func smokeRequest(parameter string) *pluginpb.CodeGeneratorRequest {
	file := &descriptorpb.FileDescriptorProto{
		Name:    new("probe/v1/probe.proto"),
		Package: new("probe.v1"),
		Syntax:  new("proto3"),
		Options: &descriptorpb.FileOptions{
			GoPackage:         new("example.com/probe/v1;probev1"),
			JavaPackage:       new("com.example.probe.v1"),
			JavaMultipleFiles: new(true),
		},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Ping"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     new("id"),
				Number:   new(int32(1)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: new("id"),
			}},
		}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: new("PingService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       new("Ping"),
				InputType:  new(".probe.v1.Ping"),
				OutputType: new(".probe.v1.Ping"),
			}},
		}},
	}

	const protocMajor = 29

	request := &pluginpb.CodeGeneratorRequest{
		FileToGenerate:  []string{file.GetName()},
		ProtoFile:       []*descriptorpb.FileDescriptorProto{file},
		CompilerVersion: &pluginpb.Version{Major: new(int32(protocMajor)), Minor: new(int32(0))},
	}

	if parameter != "" {
		request.Parameter = new(parameter)
	}

	return request
}

// discardFailedBuild empties a version directory that failed verification,
// keeping only its build log.
//
// Leaving the files in place would leave the entrypoint in place, and that is
// what `push`, `register` and the cache check all look for: a broken bundle
// would be reported failed here and then shipped by the next command anyway.
func discardFailedBuild(job buildJob) error {
	err := os.RemoveAll(job.outputDir)
	if err != nil {
		return fmt.Errorf("os.RemoveAll %s: %w", job.outputDir, err)
	}

	err = os.MkdirAll(job.outputDir, dirPermissions)
	if err != nil {
		return fmt.Errorf("os.MkdirAll %s: %w", job.outputDir, err)
	}

	return nil
}
