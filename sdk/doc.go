// Package sdk provides a client for the EasyP API Service.
//
// The SDK simplifies interaction with the EasyP API Service, allowing you to
// list available plugins and execute code generation requests remotely.
//
// Example usage:
//
//	c, err := sdk.NewClient("localhost:23410", sdk.WithInsecure())
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer c.Close()
//
//	plugins, err := c.ListPlugins(context.Background())
//
// Filtering is an option, so a call can gain one without changing shape:
//
//	plugins, err := c.ListPlugins(ctx, sdk.WithFilter(sdk.PluginFilter{Group: "grpc"}))
//	if err != nil {
//	    log.Fatal(err)
//	}
package sdk
