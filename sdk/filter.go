package sdk

// PluginFilter narrows a plugin listing. Empty fields are ignored.
//
// The service applies these itself, so a filter is a smaller response rather
// than a smaller slice: the client does not re-check the result. It used to,
// and a second pass could only ever differ from the server's own filtering by
// hiding a disagreement between the two.
type PluginFilter struct {
	// Group matches exactly, e.g. "protocolbuffers".
	Group string
	// Name matches exactly within the group, e.g. "go".
	Name string
	// Version matches exactly, e.g. "v1.36.10".
	Version string
	// Tags must all be present on a plugin for it to match.
	Tags []string
}

// listConfig is what the ListOptions resolve to.
type listConfig struct {
	filter PluginFilter
}

// ListOption configures a ListPlugins call.
type ListOption interface {
	applyList(*listConfig)
}

type listOptionFunc func(*listConfig)

func (f listOptionFunc) applyList(c *listConfig) {
	f(c)
}

// WithFilter narrows the listing to plugins matching every non-empty field.
func WithFilter(filter PluginFilter) ListOption {
	return listOptionFunc(func(c *listConfig) {
		c.filter = filter
	})
}
