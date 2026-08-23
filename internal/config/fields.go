package config

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/sethvargo/go-envconfig"
)

// Leaf is one setting: a value that can be written in the config file, supplied
// through the environment, or left to a default in a struct tag. It carries the
// three names the same setting goes by — its place in the YAML document, its
// environment variable, and its position in the Go struct — so that code which
// has to reason about all of them at once does not have to walk the struct
// itself and get the prefix accumulation subtly different.
//
// Three things need exactly that: the loader, which must know whether the file
// mentioned a key before deciding whose value wins; `config print`, which
// reports where each value came from and must not print the secret ones; and the
// test that stops a deployment config from restating a default.
type Leaf struct {
	// YAMLPath is the key sequence that names this setting in the config file,
	// e.g. {"registry", "s3", "bucket"}.
	YAMLPath []string

	// EnvKey is the full environment variable, prefixes included, e.g.
	// REGISTRY_S3_BUCKET.
	EnvKey string

	// Default is the value from the `default=` option of the env tag, and
	// HasDefault says whether there was one. They are separate because an empty
	// default is a real thing to write down and means something different from
	// no default at all — see TelemetryConfig.
	Default    string
	HasDefault bool

	// Secret marks a value that must not be printed. Set by the `secret` struct
	// tag; see the comment on that tag's use in Config.
	Secret bool

	// Index locates the field for reflect.Value.FieldByIndex.
	Index []int
}

// Name is the leaf's dotted YAML path, which is how these are named in
// validation errors and in the output of `config print`.
func (l Leaf) Name() string {
	return strings.Join(l.YAMLPath, ".")
}

// Value returns this leaf's field within cfg.
func (l Leaf) Value(cfg *Config) reflect.Value {
	return reflect.ValueOf(cfg).Elem().FieldByIndex(l.Index)
}

// SameValue reports whether this setting holds the same thing in both configs.
// Compare against Defaults to ask whether a config file's key says anything the
// binary does not already say.
//
// An empty collection and an absent one are the same setting. They are not the
// same Go value — `write_tokens: []` decodes to a non-nil empty slice while the
// default is nil — and comparing them directly would report every such key as
// carrying information when it carries none.
func (l Leaf) SameValue(left, right *Config) bool {
	leftValue := l.Value(left)
	rightValue := l.Value(right)

	if isEmptyCollection(leftValue) && isEmptyCollection(rightValue) {
		return true
	}

	return reflect.DeepEqual(leftValue.Interface(), rightValue.Interface())
}

func isEmptyCollection(value reflect.Value) bool {
	switch value.Kind() { //nolint:exhaustive // only collections have the nil/empty distinction
	case reflect.Slice, reflect.Map:
		return value.Len() == 0
	default:
		return false
	}
}

// Leaves returns every setting in Config, in declaration order.
//
// The walk is computed once: it depends only on the type, and the loader would
// otherwise redo it on every call.
var Leaves = sync.OnceValues(computeLeaves) //nolint:gochecknoglobals // memoised pure function of the Config type

// ErrUntaggedField is returned when a field of Config carries no yaml or env
// tag. It is a programming error rather than a configuration one: a field
// without both names is one that the file cannot set, or the environment
// cannot, and the asymmetry is invisible until someone tries to configure it.
var ErrUntaggedField = errors.New("config: field is missing a tag")

func computeLeaves() ([]Leaf, error) {
	var out []Leaf

	err := walkFields(reflect.TypeOf(Config{}), nil, "", nil, &out)
	if err != nil {
		return nil, err
	}

	return out, nil
}

//nolint:gochecknoglobals // reflect type handles, computed once
var (
	decoderType         = reflect.TypeOf((*envconfig.Decoder)(nil)).Elem()
	decoderCtxType      = reflect.TypeOf((*envconfig.DecoderCtx)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

// walkFields appends a Leaf for every setting in typ, descending into the
// nested sections and accumulating both names as it goes.
func walkFields(typ reflect.Type, yamlPath []string, envPrefix string, index []int, out *[]Leaf) error {
	for i := range typ.NumField() {
		field := typ.Field(i)

		if !field.IsExported() {
			continue
		}

		yamlName, skip := yamlFieldName(field)
		if skip {
			continue
		}

		tag, err := parseEnvTag(field.Tag.Get("env"))
		if err != nil {
			return fmt.Errorf("%s: %w", field.Name, err)
		}

		// Both slices are copied rather than appended in place: append would
		// share the backing array between siblings, and a later branch would
		// overwrite the path an earlier leaf is still holding.
		childPath := append(append([]string{}, yamlPath...), yamlName)
		childIndex := append(append([]int{}, index...), i)

		if isSection(field.Type) {
			err = walkFields(field.Type, childPath, envPrefix+tag.prefix, childIndex, out)
			if err != nil {
				return err
			}

			continue
		}

		if yamlName == "" || tag.key == "" {
			return fmt.Errorf("%w: %s (yaml=%q env=%q)", ErrUntaggedField, field.Name, yamlName, tag.key)
		}

		*out = append(*out, Leaf{
			YAMLPath:   childPath,
			EnvKey:     envPrefix + tag.key,
			Default:    tag.def,
			HasDefault: tag.hasDefault,
			Secret:     field.Tag.Get("secret") == "true",
			Index:      childIndex,
		})
	}

	return nil
}

// isSection reports whether a field is a nested group of settings to descend
// into rather than a setting in its own right.
//
// A struct that decodes itself from a single string is a value, not a section:
// envconfig hands it the whole variable and never looks at its fields, and the
// YAML does the same through its own unmarshaler. Slices and maps are values for
// the same reason, which is what keeps auth.write_tokens one setting rather than
// a pair of them per entry.
func isSection(typ reflect.Type) bool {
	if typ.Kind() != reflect.Struct {
		return false
	}

	ptr := reflect.PointerTo(typ)

	for _, iface := range []reflect.Type{decoderType, decoderCtxType, textUnmarshalerType} {
		if typ.Implements(iface) || ptr.Implements(iface) {
			return false
		}
	}

	return true
}

// yamlFieldName returns the key this field takes in the config file, and
// whether it is excluded from the file entirely.
func yamlFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("yaml")
	if tag == "-" {
		return "", true
	}

	name, _, _ := strings.Cut(tag, ",")

	return strings.TrimSpace(name), false
}

// envTag is the part of an `env` struct tag this package cares about.
type envTag struct {
	key        string
	prefix     string
	def        string
	hasDefault bool
}

// parseEnvTag reads an `env` tag the way go-envconfig reads it, because a
// disagreement here would be silent: this package would report a default the
// service does not actually use.
//
// Faithful in the two respects that matter. The name is the first
// comma-separated part and may be empty, which is how the section fields are
// written (`env:", prefix=REGISTRY_"`). And everything after `default=` is the
// default, commas included — a list default is one value, not several options.
func parseEnvTag(tag string) (envTag, error) {
	parts := strings.Split(tag, ",")

	parsed := envTag{key: strings.TrimSpace(parts[0])}

	for i, opt := range parts[1:] {
		opt = strings.TrimLeft(opt, " \t")

		switch {
		case strings.HasPrefix(opt, "prefix="):
			parsed.prefix = strings.TrimPrefix(opt, "prefix=")
		case strings.HasPrefix(opt, "default="):
			rest := strings.TrimLeft(strings.Join(parts[i+1:], ","), " \t")
			parsed.def = strings.TrimPrefix(rest, "default=")
			parsed.hasDefault = true

			return parsed, nil
		case opt == "" || opt == "required" || opt == "noinit" ||
			opt == "overwrite" || opt == "decodeunset" ||
			strings.HasPrefix(opt, "delimiter=") || strings.HasPrefix(opt, "separator="):
			// Recognised by envconfig and of no interest here.
		default:
			return envTag{}, fmt.Errorf("unknown env tag option %q", opt)
		}
	}

	return parsed, nil
}

// SectionPrefixes returns the environment variable prefix of every section of
// Config, e.g. REGISTRY_ and REGISTRY_S3_.
//
// It is what makes an unrecognised variable reportable: a name carrying one of
// these prefixes was aimed at this service, so failing to match a setting is a
// mistake rather than an unrelated variable that happens to be exported.
//
// Computed once, like Leaves, and for the same reason.
var SectionPrefixes = sync.OnceValues(computeSectionPrefixes) //nolint:gochecknoglobals // memoised pure function

func computeSectionPrefixes() ([]string, error) {
	var out []string

	err := walkSections(reflect.TypeOf(Config{}), "", &out)
	if err != nil {
		return nil, err
	}

	return out, nil
}

func walkSections(typ reflect.Type, envPrefix string, out *[]string) error {
	for i := range typ.NumField() {
		field := typ.Field(i)

		if !field.IsExported() || !isSection(field.Type) {
			continue
		}

		if _, skip := yamlFieldName(field); skip {
			continue
		}

		tag, err := parseEnvTag(field.Tag.Get("env"))
		if err != nil {
			return fmt.Errorf("%s: %w", field.Name, err)
		}

		prefix := envPrefix + tag.prefix
		if prefix != "" {
			*out = append(*out, prefix)
		}

		if err = walkSections(field.Type, prefix, out); err != nil {
			return err
		}
	}

	return nil
}
