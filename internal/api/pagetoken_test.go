package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
	"github.com/easyp-tech/service/internal/core"
)

func TestPageTokenRoundTrip(t *testing.T) {
	t.Parallel()

	key := &core.PluginKey{Group: "grpc", Name: "go", Version: "v1.5.1"}

	decoded, err := decodePageToken(encodePageToken(key))
	require.NoError(t, err)
	assert.Equal(t, key, decoded)
}

func TestPageTokenEmptyMeansFirstPage(t *testing.T) {
	t.Parallel()

	assert.Empty(t, encodePageToken(nil), "no next page must encode as the empty token")

	decoded, err := decodePageToken("")
	require.NoError(t, err)
	assert.Nil(t, decoded, "the empty token must mean the first page, not an error")
}

func TestPageTokenGarbageIsRejected(t *testing.T) {
	t.Parallel()

	for _, token := range []string{"not a token", "%%%", "bm90IGpzb24"} {
		_, err := decodePageToken(token)
		require.ErrorIs(t, err, errBadPageToken, "token %q", token)
	}
}

// pagingService returns one fixed page and records the page it was asked for.
type pagingService struct {
	core.Service

	gotPage core.PluginPage
	list    core.PluginList
}

func (s *pagingService) ListPlugins(_ context.Context, _ core.PluginFilter, page core.PluginPage) (core.PluginList, error) {
	s.gotPage = page

	return s.list, nil
}

// TestPluginsHandlerSpeaksThePagingContract pins the wire mapping in both
// directions: request fields reach the core as a page, and the core's
// continuation comes back as a token the next request can carry.
func TestPluginsHandlerSpeaksThePagingContract(t *testing.T) {
	t.Parallel()

	next := &core.PluginKey{Group: "grpc", Name: "go", Version: "v1.5.1"}
	svc := &pagingService{list: core.PluginList{Next: next}}
	handler := &API{app: svc}

	pageSize := uint32(50)
	pageToken := encodePageToken(&core.PluginKey{Group: "a", Name: "b", Version: "v1.0.0"})
	resp, err := handler.Plugins(t.Context(), &generator.PluginsRequest{
		PageSize:  &pageSize,
		PageToken: &pageToken,
	})
	require.NoError(t, err)

	assert.Equal(t, 50, svc.gotPage.Size)
	require.NotNil(t, svc.gotPage.After)
	assert.Equal(t, "a", svc.gotPage.After.Group)

	decoded, err := decodePageToken(resp.GetNextPageToken())
	require.NoError(t, err)
	assert.Equal(t, next, decoded, "the response token must resume where the core said the page ended")
}

func TestPluginsHandlerRejectsForeignToken(t *testing.T) {
	t.Parallel()

	handler := &API{app: &pagingService{}}

	badToken := "definitely-not-issued-here"
	_, err := handler.Plugins(t.Context(), &generator.PluginsRequest{
		PageToken: &badToken,
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err),
		"a broken token is the caller's error, not the server's")
}
