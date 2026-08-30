package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopkg.in/yaml.v3"

	"github.com/easyp-tech/service/internal/config"
)

// parse is the loader's own parse step, so these tests operate on the same node
// tree the real path does.
func parse(t *testing.T, doc string) *yaml.Node {
	t.Helper()

	root, _, err := config.ParseDocumentForTest([]byte(doc))
	require.NoError(t, err)

	return root
}

// render marshals the node tree back to YAML, which is how these tests observe
// a rename: the rewrite happens on nodes, and what matters is the document the
// decoder then sees.
func render(t *testing.T, root *yaml.Node) string {
	t.Helper()

	out, err := yaml.Marshal(root)
	require.NoError(t, err)

	return string(out)
}

// TestYAMLAliasRenamesInPlace covers the migration window a rename needs.
//
// The shipped table is empty — the mechanism has to exist before the rename
// that uses it, because after 1.0 the configuration surface is frozen and a
// rename without a window is a major version. The alias here is synthetic for
// that reason.
func TestYAMLAliasRenamesInPlace(t *testing.T) {
	t.Parallel()

	aliases := map[string]string{"worker_pool.workers_count": "worker_pool.workers"}

	root := parse(t, "worker_pool:\n  workers_count: 8\n")

	diags := config.ApplyYAMLAliasesWith(root, aliases)

	require.Len(t, diags, 1)
	assert.Equal(t, config.SeverityWarning, diags[0].Severity,
		"a renamed key still works; it must not refuse the start")
	assert.Contains(t, diags[0].Message, "worker_pool.workers")

	// The node tree now spells the current name, which is what makes the decode
	// fill the right field and the unknown-key check stay quiet.
	assert.Contains(t, render(t, root), "workers: 8")
	assert.NotContains(t, render(t, root), "workers_count")
}

// TestYAMLAliasRefusesBothNames: a file carrying old and new would otherwise be
// resolved by whichever the decoder reached first.
func TestYAMLAliasRefusesBothNames(t *testing.T) {
	t.Parallel()

	aliases := map[string]string{"worker_pool.workers_count": "worker_pool.workers"}

	root := parse(t, "worker_pool:\n  workers_count: 8\n  workers: 4\n")

	diags := config.ApplyYAMLAliasesWith(root, aliases)

	require.Len(t, diags, 1)
	assert.Equal(t, config.SeverityError, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "sets both")
}

// TestYAMLAliasLeavesEverythingElseAlone: the walk must not disturb a document
// that names no former key.
func TestYAMLAliasLeavesEverythingElseAlone(t *testing.T) {
	t.Parallel()

	aliases := map[string]string{"worker_pool.workers_count": "worker_pool.workers"}

	const doc = "worker_pool:\n  workers: 4\nlog:\n  level: debug\n"

	root := parse(t, doc)

	assert.Empty(t, config.ApplyYAMLAliasesWith(root, aliases))
	assert.Contains(t, render(t, root), "level: debug")
}

// TestYAMLAliasesRenameInPlace guards the shipped table itself: this rewrites a
// key where it stands and cannot move a value between sections, so an entry
// whose two paths have different parents would be applied halfway.
func TestYAMLAliasesRenameInPlace(t *testing.T) {
	t.Parallel()

	for old, current := range config.YAMLAliases() {
		assert.Equal(t,
			old[:strings.LastIndex(old, ".")+1],
			current[:strings.LastIndex(current, ".")+1],
			"alias %q -> %q crosses sections; applyYAMLAliases renames in place", old, current)
	}
}
