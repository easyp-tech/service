package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/easyp-tech/service/internal/core"
)

// errBadPageToken marks a continuation token the server cannot have issued.
// The gRPC handler maps it to InvalidArgument: the fault is in the request,
// and retrying the same token cannot succeed.
var errBadPageToken = errors.New("page_token is not one this server issued")

// pageTokenKey is the wire form of a core.PluginKey. Short JSON field names
// keep the token compact; base64url keeps it safe to put in a URL or a shell
// argument. The token is documented as opaque, so its shape can change between
// releases — a client that decodes it has no contract to stand on.
type pageTokenKey struct {
	Group   string `json:"g"`
	Name    string `json:"n"`
	Version string `json:"v"`
}

// encodePageToken renders a continuation key as an opaque token. A nil key —
// the last page — encodes as the empty string, which is how the proto contract
// spells "no next page".
func encodePageToken(key *core.PluginKey) string {
	if key == nil {
		return ""
	}

	raw, err := json.Marshal(pageTokenKey{Group: key.Group, Name: key.Name, Version: key.Version})
	if err != nil {
		// Three strings cannot fail to marshal; the branch exists because the
		// signature of Marshal says so.
		return ""
	}

	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodePageToken parses a token produced by encodePageToken. The empty string
// is the first page. Anything undecodable is the caller's error: a token is
// only ever obtained from a previous response, so a broken one was either
// truncated in transit or invented.
func decodePageToken(token string) (*core.PluginKey, error) {
	if token == "" {
		return nil, nil //nolint:nilnil // nil key is the documented "first page"
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadPageToken, err)
	}

	var key pageTokenKey

	err = json.Unmarshal(raw, &key)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadPageToken, err)
	}

	return &core.PluginKey{Group: key.Group, Name: key.Name, Version: key.Version}, nil
}
