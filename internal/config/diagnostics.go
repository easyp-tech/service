package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Severity says whether a diagnostic stops the service or merely tells the
// operator something.
type Severity int

const (
	// SeverityWarning is worth saying and does not stop anything.
	SeverityWarning Severity = iota
	// SeverityError refuses the configuration.
	SeverityError
)

// String names the severity as it is printed.
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	default:
		return "unknown"
	}
}

// Diagnostic is one thing wrong with a configuration, named precisely enough to
// fix without opening the source.
//
// The precision is the point. This replaces a []string of whatever
// yaml.TypeError.Error() happened to concatenate — one multi-line blob naming Go
// types rather than YAML keys, with the line numbers buried in prose. An
// operator who mistypes a key is entitled to be told which key, on which line,
// and what the key they meant is called.
// Layers a diagnostic can come from.
const (
	SourceFile = "file"
	SourceEnv  = "env"
)

type Diagnostic struct {
	Severity Severity

	// Source is SourceFile or SourceEnv: which layer the problem is in. The
	// same mistake has a different fix depending on where it was made.
	Source string

	// Path names the setting: a dotted YAML path for the file layer
	// ("registry.cache_max_byte"), a variable name for the environment
	// ("REGISTRY_BUKKET").
	Path string

	// Line is the line in the config file, or 0 where there is none.
	Line int

	Message string

	// Hint is the suggested correction, empty when nothing plausible was found.
	Hint string
}

// String renders the diagnostic on one line.
func (d Diagnostic) String() string {
	var out strings.Builder

	if d.Line > 0 {
		fmt.Fprintf(&out, "line %d: ", d.Line)
	}

	if d.Path != "" {
		fmt.Fprintf(&out, "%s: ", d.Path)
	}

	out.WriteString(d.Message)

	if d.Hint != "" {
		fmt.Fprintf(&out, " (%s)", d.Hint)
	}

	return out.String()
}

// Diagnostics is what one attempt at resolving a configuration had to say about
// it.
type Diagnostics []Diagnostic

// Err returns an error naming every SeverityError diagnostic, or nil when there
// are none. Warnings never produce an error.
//
// This is what makes a mistyped key fatal: the loader returns Err(), so
// `config validate` and `service start` exit non-zero instead of starting on
// defaults the operator never asked for.
func (d Diagnostics) Err() error {
	var errs []error

	for _, diag := range d {
		if diag.Severity == SeverityError {
			errs = append(errs, errors.New(diag.String()))
		}
	}

	return errors.Join(errs...)
}

// HasErrors reports whether anything here refuses the configuration.
func (d Diagnostics) HasErrors() bool {
	for _, diag := range d {
		if diag.Severity == SeverityError {
			return true
		}
	}

	return false
}

// retiredKeys are keys that were real once, mapped to what to do instead.
//
// Naming them turns "unknown key, refusing to start" into an explanation. A
// removal and a typo are the same event to a schema check and completely
// different events to the person reading the failure: one is their mistake, the
// other is ours, and only one of them has a documented fix.
//
// These stay listed after the field is gone. The cost is a map entry; the cost
// of dropping one is an operator staring at a key they copied from our own
// README being called unrecognised.
var retiredKeys = map[string]string{ //nolint:gochecknoglobals // static schema history
	"db.driver": "removed in v0.13.0: postgres is the only driver this service has ever supported " +
		"and the migrations hard-code it; delete the key",
	"license.public_key": "removed in v0.13.0: move the key into license.public_keys, " +
		`under its key id, or under "*" to verify any key id`,
}

// documentSchema is the set of names a config file may use, derived from the
// Config type so it cannot drift from it.
type documentSchema struct {
	// leaves holds every dotted path that names a setting.
	leaves map[string]bool
	// sections holds every dotted path that names a group to descend into,
	// including "" for the document root.
	sections map[string]bool
	// children maps a section's path to the names directly under it, which is
	// the candidate set a misspelling is matched against.
	children map[string][]string
}

// schemaOf is memoised: it depends only on the Config type, and the loader would
// otherwise rebuild it on every call.
var schemaOf = sync.OnceValues(computeSchema) //nolint:gochecknoglobals // memoised pure function of the Config type

func computeSchema() (*documentSchema, error) {
	leaves, err := Leaves()
	if err != nil {
		return nil, err
	}

	schema := &documentSchema{
		leaves:   make(map[string]bool, len(leaves)),
		sections: map[string]bool{"": true},
		children: make(map[string][]string),
	}

	for _, leaf := range leaves {
		for depth := range leaf.YAMLPath {
			parent := strings.Join(leaf.YAMLPath[:depth], ".")
			name := leaf.YAMLPath[depth]
			path := strings.Join(leaf.YAMLPath[:depth+1], ".")

			if depth == len(leaf.YAMLPath)-1 {
				schema.leaves[path] = true
			} else {
				schema.sections[path] = true
			}

			if !contains(schema.children[parent], name) {
				schema.children[parent] = append(schema.children[parent], name)
			}
		}
	}

	return schema, nil
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

// documentDiagnostics reports every key in the file that names no setting.
//
// It walks the parsed document rather than reading yaml.v3's strict-decode
// error, because that error names Go types ("field porrt not found in type
// config.Ports") and concatenates every finding into one string. The document
// carries the line of each key, and the schema carries what the key should have
// been.
func documentDiagnostics(root *yaml.Node) (Diagnostics, error) {
	schema, err := schemaOf()
	if err != nil {
		return nil, err
	}

	if root == nil || len(root.Content) == 0 {
		// An empty file names nothing. It is a valid configuration: every
		// setting comes from the environment and the defaults.
		return nil, nil
	}

	var out Diagnostics

	walkDocument(resolveAlias(root.Content[0]), nil, schema, &out)

	return out, nil
}

// resolveAlias follows a YAML anchor reference to the node it points at, so that
// a config using anchors is checked rather than skipped.
func resolveAlias(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}

	return node
}

func walkDocument(node *yaml.Node, path []string, schema *documentSchema, out *Diagnostics) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}

	parent := strings.Join(path, ".")

	// A mapping's Content alternates key, value, key, value.
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		value := resolveAlias(node.Content[i+1])

		// A merge key is YAML's own syntax, not a setting. yaml.v3 has already
		// resolved it into the surrounding mapping by the time the file is
		// decoded, so the keys it brought in are checked where they are written.
		if key.Value == mergeKey {
			continue
		}

		// An anchor holder at the document root: the idiom for defining a block
		// once and referring to it from several places. It names no setting and
		// is not meant to. Only at the root, and only under the two prefixes
		// that conventionally mean "not a field" — anywhere else, an unknown key
		// is still a mistake.
		if len(path) == 0 && isAnchorHolder(key.Value) {
			continue
		}

		childPath := append(append([]string{}, path...), key.Value)
		name := strings.Join(childPath, ".")

		if reason, retired := retiredKeys[name]; retired {
			*out = append(*out, Diagnostic{
				Severity: SeverityError,
				Source:   SourceFile,
				Path:     name,
				Line:     key.Line,
				Message:  reason,
			})

			continue
		}

		// Stop at a leaf. license.public_keys and auth.write_tokens are single
		// settings whose values happen to be a mapping and a sequence;
		// descending into them would report every key id and every token name
		// as an unknown setting.
		if schema.leaves[name] {
			continue
		}

		if schema.sections[name] {
			walkDocument(value, childPath, schema, out)

			continue
		}

		*out = append(*out, Diagnostic{
			Severity: SeverityError,
			Source:   SourceFile,
			Path:     name,
			Line:     key.Line,
			Message:  "unknown key",
			Hint:     hintFor(key.Value, parent, schema),
		})
	}
}

// mergeKey is YAML's merge-key indicator, "<<".
const mergeKey = "<<"

// isAnchorHolder reports whether a root key is one of the conventional homes for
// a YAML anchor rather than a setting: a leading dot, or the "x-" prefix that
// docker-compose and OpenAPI use for extension fields.
func isAnchorHolder(key string) bool {
	return strings.HasPrefix(key, ".") || strings.HasPrefix(key, "x-")
}

// hintFor guesses which setting an unrecognised key was meant to be.
//
// Two passes, in order of how much they prove. Normalising away case and
// separators catches the whole class this repository actually produces: three
// dictionaries for one setting (YAML cache_max_bytes, env CACHE_MAX_BYTES, Helm
// cacheMaxBytes) means a key pasted from the chart's values is a normalisation
// away from correct, not a guess. Edit distance then catches ordinary
// mistyping — porrt, metrics, max_retry.
//
// Candidates are the siblings first, because a key is nearly always in the right
// section; only then everything, which is what finds a setting written at the
// wrong level.
func hintFor(key, parent string, schema *documentSchema) string {
	siblings := schema.children[parent]

	if match, ok := bestMatch(key, siblings); ok {
		return "did you mean " + qualify(parent, match) + "?"
	}

	var all []string

	for path := range schema.leaves {
		all = append(all, path)
	}

	// Match on the last segment: a setting written at the wrong level is spelled
	// correctly, just in the wrong place, and naming its full path is the fix.
	for _, path := range all {
		segments := strings.Split(path, ".")
		if normalise(segments[len(segments)-1]) == normalise(key) {
			return "did you mean " + path + "?"
		}
	}

	return ""
}

func qualify(parent, name string) string {
	if parent == "" {
		return name
	}

	return parent + "." + name
}

func bestMatch(key string, candidates []string) (string, bool) {
	for _, candidate := range candidates {
		if normalise(candidate) == normalise(key) {
			return candidate, true
		}
	}

	best := ""
	bestDistance := maxHintDistance + 1

	for _, candidate := range candidates {
		distance := editDistance(normalise(key), normalise(candidate))
		if distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}

	if bestDistance <= maxHintDistance {
		return best, true
	}

	return "", false
}

// maxHintDistance is deliberately small. A wrong suggestion is worse than none:
// it sends the reader to a setting they did not want and makes the checker look
// unreliable at the moment it is asking to be trusted.
const maxHintDistance = 2

// normalise folds the three spellings of a setting name onto one another:
// cacheMaxBytes, cache_max_bytes and CACHE-MAX-BYTES all become cachemaxbytes.
func normalise(name string) string {
	var out strings.Builder

	for _, char := range name {
		switch {
		case char == '_' || char == '-':
		case char >= 'A' && char <= 'Z':
			out.WriteRune(char - 'A' + 'a')
		default:
			out.WriteRune(char)
		}
	}

	return out.String()
}

// editDistance is Levenshtein distance, two rows rather than a full matrix.
func editDistance(left, right string) int {
	if left == right {
		return 0
	}

	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)

	for col := range previous {
		previous[col] = col
	}

	for row := 1; row <= len(left); row++ {
		current[0] = row

		for col := 1; col <= len(right); col++ {
			cost := 1
			if left[row-1] == right[col-1] {
				cost = 0
			}

			current[col] = min(previous[col]+1, min(current[col-1]+1, previous[col-1]+cost))
		}

		previous, current = current, previous
	}

	return previous[len(right)]
}
