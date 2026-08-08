package core

import (
	"errors"
	"testing"
)

// TestValidateVersion pins down what a version may look like. The rule used to
// demand three components, which quietly refused every protobuf language
// runtime — they are published as v33.1, not v33.1.0 — and so refused a third
// of the plugin catalogue at registration time.
func TestValidateVersion(t *testing.T) {
	t.Parallel()

	valid := []string{
		"v1.2.3",
		"v33.1",  // protobuf's own runtimes
		"v35.0",  //
		"v0.0.0", //
		"v10.20", //
		"latest", // the moving target, allowed by name
	}
	for _, version := range valid {
		if err := validateVersion(version); err != nil {
			t.Errorf("expected %q to be accepted, got %v", version, err)
		}
	}

	invalid := []string{
		"",         // absent
		"1.2.3",    // no v
		"v1",       // a major alone says nothing about compatibility
		"v1.2.3.4", // more components than the scheme has meaning for
		"v1.2.x",   // a wildcard is a query, not a version
		"vlatest",  //
		"v1.2.3-rc1",
	}
	for _, version := range invalid {
		err := validateVersion(version)
		if err == nil {
			t.Errorf("expected %q to be rejected", version)

			continue
		}
		if !errors.Is(err, ErrInvalidPluginName) {
			t.Errorf("expected %q to fail with ErrInvalidPluginName, got %v", version, err)
		}
	}
}

func TestValidateGroupName(t *testing.T) {
	t.Parallel()

	valid := [][2]string{
		{"protocolbuffers", "go"},
		{"grpc-ecosystem", "gateway"},
		{"bufbuild", "validate-go"},
		{"community", "stephenh-ts-proto"},
	}
	for _, pair := range valid {
		if err := validateGroupName(pair[0], pair[1]); err != nil {
			t.Errorf("expected %q/%q to be accepted, got %v", pair[0], pair[1], err)
		}
	}

	invalid := [][2]string{
		{"", "go"},
		{"protocolbuffers", ""},
		{"Protocolbuffers", "go"}, // upper case
		{"9grpc", "go"},           // leading digit
		{"grpc", "go_lang"},       // underscore
		{"grpc/x", "go"},          // separator inside a component
	}
	for _, pair := range invalid {
		err := validateGroupName(pair[0], pair[1])
		if err == nil {
			t.Errorf("expected %q/%q to be rejected", pair[0], pair[1])

			continue
		}
		if !errors.Is(err, ErrInvalidPluginName) {
			t.Errorf("expected %q/%q to fail with ErrInvalidPluginName, got %v", pair[0], pair[1], err)
		}
	}
}
