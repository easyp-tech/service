//go:build ignore

// Command stubplugin is a protoc plugin used by the integration tests.
//
// It lives in the module tree rather than being written to a temp directory at
// test time so that `go build` resolves its imports against this module's
// go.mod. Built outside the tree, the compiler goes looking for
// google.golang.org/protobuf on the network and the test hangs instead of
// failing.
//
// The build tag keeps it out of `go build ./...`; the test builds it by file
// path, which ignores the tag.
//
// It is a real program rather than a script echoing fixed bytes because the
// point of these tests is the process boundary: marshal, hand over stdin, read
// back from stdout, unmarshal. A script would exercise none of that.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read stdin:", err)
		os.Exit(1)
	}

	var req pluginpb.CodeGeneratorRequest
	if err := proto.Unmarshal(in, &req); err != nil {
		fmt.Fprintln(os.Stderr, "unmarshal request:", err)
		os.Exit(1)
	}

	name := "stub.txt"
	// Echoed back so a test can prove the response came from this invocation.
	content := "parameter=" + req.GetParameter()

	// "bytes=N" asks for a response of roughly N bytes, so a test can drive a
	// message past the transport's size limits without needing a plugin that
	// genuinely generates that much.
	if size, ok := strings.CutPrefix(req.GetParameter(), "bytes="); ok {
		n, err := strconv.Atoi(size)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad bytes= parameter:", err)
			os.Exit(1)
		}

		content = strings.Repeat("x", n)
	}

	resp := &pluginpb.CodeGeneratorResponse{
		File: []*pluginpb.CodeGeneratorResponse_File{{Name: &name, Content: &content}},
	}

	out, err := proto.Marshal(resp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal response:", err)
		os.Exit(1)
	}

	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintln(os.Stderr, "write stdout:", err)
		os.Exit(1)
	}
}
