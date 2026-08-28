package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestUpdateMaskFields(t *testing.T) {
	t.Parallel()

	t.Run("an absent mask replaces both fields", func(t *testing.T) {
		t.Parallel()

		// The pre-mask contract. A client written against it keeps working.
		config, tags, err := updateMaskFields(nil)
		require.NoError(t, err)
		assert.True(t, config)
		assert.True(t, tags)
	})

	t.Run("an empty mask replaces both fields", func(t *testing.T) {
		t.Parallel()

		config, tags, err := updateMaskFields(&fieldmaskpb.FieldMask{})
		require.NoError(t, err)
		assert.True(t, config)
		assert.True(t, tags)
	})

	t.Run("tags alone leaves config untouched", func(t *testing.T) {
		t.Parallel()

		// The case the mask exists for: renaming a tag without resending the
		// plugin's command line.
		config, tags, err := updateMaskFields(&fieldmaskpb.FieldMask{Paths: []string{"tags"}})
		require.NoError(t, err)
		assert.False(t, config)
		assert.True(t, tags)
	})

	t.Run("config alone leaves tags untouched", func(t *testing.T) {
		t.Parallel()

		config, tags, err := updateMaskFields(&fieldmaskpb.FieldMask{Paths: []string{"config"}})
		require.NoError(t, err)
		assert.True(t, config)
		assert.False(t, tags)
	})

	t.Run("both paths named explicitly", func(t *testing.T) {
		t.Parallel()

		config, tags, err := updateMaskFields(&fieldmaskpb.FieldMask{Paths: []string{"config", "tags"}})
		require.NoError(t, err)
		assert.True(t, config)
		assert.True(t, tags)
	})

	t.Run("surrounding whitespace is tolerated", func(t *testing.T) {
		t.Parallel()

		config, tags, err := updateMaskFields(&fieldmaskpb.FieldMask{Paths: []string{" tags "}})
		require.NoError(t, err)
		assert.False(t, config)
		assert.True(t, tags)
	})

	t.Run("an unknown path is refused, not ignored", func(t *testing.T) {
		t.Parallel()

		// Ignoring it would apply a different update than the one asked for,
		// and "tag" for "tags" is exactly the typo that gets made.
		_, _, err := updateMaskFields(&fieldmaskpb.FieldMask{Paths: []string{"tag"}})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.Contains(t, err.Error(), `"tag"`)
	})

	t.Run("one bad path poisons an otherwise valid mask", func(t *testing.T) {
		t.Parallel()

		_, _, err := updateMaskFields(&fieldmaskpb.FieldMask{Paths: []string{"tags", "nope"}})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}
