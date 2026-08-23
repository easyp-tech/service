package main

import (
	"fmt"
	"os"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	generator "github.com/easyp-tech/service/api/generator/v1"
)

// descriptorPermissions is the mode of the written descriptor set. It describes
// the public API and contains no secrets.
const descriptorPermissions = 0o644

// runAPIDescriptor writes a FileDescriptorSet for the gRPC API.
//
// The server does not register reflection, so tools like grpcurl cannot ask it
// what methods exist. The schema imports protos from the easyp module cache,
// which makes a plain --proto invocation depend on paths that differ per
// machine; a descriptor set sidesteps that entirely:
//
//	easyp-svc api descriptor -o api.protoset
//	grpcurl -protoset api.protoset ... api.generator.v1.ServiceAPI/Plugins
func runAPIDescriptor(outPath string) error {
	root, err := protoregistry.GlobalFiles.FindFileByPath(generator.File_api_generator_v1_generator_proto.Path())
	if err != nil {
		return fmt.Errorf("protoregistry.FindFileByPath: %w", err)
	}

	set := &descriptorpb.FileDescriptorSet{}
	collectFileDescriptors(root, set, make(map[string]struct{}))

	data, err := proto.Marshal(set)
	if err != nil {
		return fmt.Errorf("proto.Marshal: %w", err)
	}

	if outPath == "" || outPath == "-" {
		_, err = os.Stdout.Write(data)
		if err != nil {
			return fmt.Errorf("os.Stdout.Write: %w", err)
		}

		return nil
	}

	err = os.WriteFile(outPath, data, descriptorPermissions)
	if err != nil {
		return fmt.Errorf("os.WriteFile: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stderr, "Wrote %s (%d bytes)\n", outPath, len(data))

	return nil
}

// collectFileDescriptors appends fd and everything it imports, dependencies
// first, which is the order a FileDescriptorSet has to be in.
func collectFileDescriptors(
	fd protoreflect.FileDescriptor,
	set *descriptorpb.FileDescriptorSet,
	seen map[string]struct{},
) {
	if _, done := seen[fd.Path()]; done {
		return
	}

	seen[fd.Path()] = struct{}{}

	imports := fd.Imports()
	for idx := range imports.Len() {
		collectFileDescriptors(imports.Get(idx).FileDescriptor, set, seen)
	}

	set.File = append(set.File, protodesc.ToFileDescriptorProto(fd))
}
