//go:build mcpgen

// Command mcpgen regenerates api/generator/v1/generator.mcp.go by invoking
// protoc-gen-mcp directly with a CodeGeneratorRequest built from the compiled
// descriptors. It exists because `easyp generate`'s command-plugin executor
// hands the plugin a request it answers with a valid, empty response — no
// error, no files — so the stale .mcp.go survives every "successful" generate
// run (observed on easyp v0.16.0 and v0.16.7-dev; the same plugin invoked
// directly, as below, generates fine; likely related to the request easyp
// assembles — it is three times the size of the real descriptor set).
//
// Remove this once the easyp CLI's command executor is fixed; the version in
// the go run line below has to match the mcpruntime version in go.mod.
//
//	go run -tags mcpgen ./test/mcpgen
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	generator "github.com/easyp-tech/service/api/generator/v1"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mcpgen:", err)
		os.Exit(1)
	}
}

func run() error {
	target := generator.File_api_generator_v1_generator_proto

	var files []*descriptorpb.FileDescriptorProto

	seen := map[string]bool{}

	var add func(protoreflect.FileDescriptor)
	add = func(f protoreflect.FileDescriptor) {
		if seen[f.Path()] {
			return
		}
		seen[f.Path()] = true

		for i := range f.Imports().Len() {
			add(f.Imports().Get(i).FileDescriptor)
		}

		files = append(files, protodesc.ToFileDescriptorProto(f))
	}
	add(target)

	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{target.Path()},
		Parameter:      proto.String("paths=source_relative"),
		ProtoFile:      files,
	}

	in, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	fmt.Printf("request: %d bytes, %d files, generating %s\n", len(in), len(files), target.Path())

	cmd := exec.Command("go", "run", "github.com/easyp-tech/protoc-gen-mcp/cmd/protoc-gen-mcp@v0.5.0")
	cmd.Stdin = bytes.NewReader(in)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("protoc-gen-mcp: %w\n%s", err, stderr.String())
	}

	var resp pluginpb.CodeGeneratorResponse
	if err := proto.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return fmt.Errorf("plugin error: %s", resp.GetError())
	}

	if len(resp.File) == 0 {
		return fmt.Errorf("plugin produced no files — the request above should have yielded one")
	}

	for _, f := range resp.File {
		if err := os.WriteFile(f.GetName(), []byte(f.GetContent()), 0o644); err != nil { //nolint:gosec // generated source
			return fmt.Errorf("write %s: %w", f.GetName(), err)
		}

		fmt.Println("wrote", f.GetName())
	}

	return nil
}
