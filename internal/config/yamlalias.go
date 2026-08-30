package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlAliases maps a config key's former name to its current one, for keys that
// were renamed rather than removed.
//
// It is empty, and that is the point of adding it now.
//
// An unrecognised YAML key refuses the start — a good contract, and the right
// one for a service whose settings are spelled three ways (snake_case here,
// UPPER_SNAKE in the environment, camelCase in the chart). But it means a
// rename is a hard failure for every deployment still carrying the old name,
// with no window in between. Before 1.0 that is a bad afternoon; after 1.0,
// when the configuration surface is frozen, it is a major version.
//
// So the migration path has to exist before the rename that needs it. With this
// table a rename costs one entry: the old key keeps working, its value goes
// where the new name expects it, and the operator gets a warning naming both
// and the line to change. One release later the entry moves to retiredKeys and
// the old name stops working — deliberately, and after having said so.
//
// This is not where removals go. A key with no replacement, or one whose
// replacement takes a different shape — license.public_key became a map keyed
// by key id — cannot be migrated by renaming it, and belongs in retiredKeys
// where it produces an explanation instead of a silent misreading.
//
// Both paths must share a parent: this renames a key in place and does not move
// a value between sections. TestYAMLAliasesRenameInPlace enforces that, so an
// entry that would need a move fails the build rather than being applied
// halfway.
var yamlAliases = map[string]string{} //nolint:gochecknoglobals // static naming table

// applyYAMLAliases rewrites former key names in the parsed document to their
// current ones, and reports each rewrite as a warning.
//
// It runs on the node tree, before the document is decoded and before it is
// checked against the schema, so everything downstream sees only current names:
// the decode fills the right field, and the unknown-key check does not report a
// name this table just accepted.
func applyYAMLAliases(root *yaml.Node, aliases map[string]string) Diagnostics {
	if root == nil || len(root.Content) == 0 || len(aliases) == 0 {
		return nil
	}

	var out Diagnostics

	rewriteAliases(resolveAlias(root.Content[0]), nil, aliases, &out)

	return out
}

// rewriteAliases walks a mapping, renaming keys the table covers.
func rewriteAliases(node *yaml.Node, path []string, aliases map[string]string, out *Diagnostics) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}

	// Which current names this mapping already spells, so that a file carrying
	// both the old and the new name is not silently resolved in favour of
	// whichever came first.
	present := make(map[string]bool, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		present[node.Content[i].Value] = true
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]

		if key.Value == mergeKey {
			continue
		}

		name := strings.Join(append(append([]string{}, path...), key.Value), ".")

		current, renamed := aliases[name]
		if !renamed {
			rewriteAliases(resolveAlias(node.Content[i+1]), append(append([]string{}, path...), key.Value), aliases, out)

			continue
		}

		newLeaf := current[strings.LastIndex(current, ".")+1:]

		if present[newLeaf] {
			// Both names in one file. Renaming would produce a duplicate key
			// and let the decoder pick; refusing says which one to delete.
			*out = append(*out, Diagnostic{
				Severity: SeverityError,
				Source:   SourceFile,
				Path:     name,
				Line:     key.Line,
				Message: fmt.Sprintf(
					"%s was renamed to %s, and this file sets both; delete the old one",
					name, current,
				),
			})

			continue
		}

		*out = append(*out, Diagnostic{
			Severity: SeverityWarning,
			Source:   SourceFile,
			Path:     name,
			Line:     key.Line,
			Message:  fmt.Sprintf("%s was renamed to %s; the old name still works but will stop", name, current),
			Hint:     fmt.Sprintf("rename it to %s", newLeaf),
		})

		key.Value = newLeaf
		present[newLeaf] = true
	}
}
