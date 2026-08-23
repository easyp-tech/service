//go:build chartcheck

// Command chartcheck drives a chart-deployed easyp-svc through the SDK: it
// registers a handful of plugins with a write token, then walks the paginated
// listing page by page. It is a manual smoke check for a real cluster, kept
// out of every normal build by its tag.
//
//	kubectl -n easyp port-forward svc/easyp-easyp-service 18080:8080 &
//	EASYP_TOKEN=<token> go run -tags chartcheck ./test/chartcheck -addr localhost:18080
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	generator "github.com/easyp-tech/service/api/generator/v1"
	"github.com/easyp-tech/service/sdk"
)

func main() {
	addr := flag.String("addr", "localhost:18080", "gRPC address of the deployed service")
	group := flag.String("group", fmt.Sprintf("chartcheck%d", time.Now().Unix()), "isolation group for the fixtures")
	flag.Parse()

	if err := run(*addr, *group); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}

	fmt.Println("OK")
}

func run(addr, group string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := sdk.NewClient(addr, sdk.WithInsecure(), sdk.WithToken(os.Getenv("EASYP_TOKEN")))
	if err != nil {
		return fmt.Errorf("sdk.NewClient: %w", err)
	}
	defer client.Close()

	const total = 5

	for i := range total {
		name := fmt.Sprintf("plugin-%02d", i)

		_, err := client.CreatePlugin(ctx, group, name, "v1.0.0",
			map[string]any{"command": []any{"/plugins/stub"}}, []string{"chartcheck"})
		if err != nil {
			return fmt.Errorf("CreatePlugin %s/%s: %w", group, name, err)
		}
	}

	fmt.Printf("registered %d plugins in group %s\n", total, group)

	// The SDK walks pages itself; the raw client below checks the wire-level
	// paging. First the aggregate view:
	all, err := client.ListPlugins(ctx, sdk.PluginFilter{Group: group})
	if err != nil {
		return fmt.Errorf("ListPlugins: %w", err)
	}

	if len(all) != total {
		return fmt.Errorf("SDK walk returned %d plugins, want %d", len(all), total)
	}

	fmt.Printf("SDK walk returned all %d\n", len(all))

	// Wire-level: page_size=2 must take 3 pages of 2+2+1. Reads are anonymous,
	// so a plain connection beside the SDK is enough.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("grpc.NewClient: %w", err)
	}
	defer conn.Close()

	raw := generator.NewServiceAPIClient(conn)

	var pages []int

	req := &generator.PluginsRequest{}
	g := group
	req.Group = &g
	pageSize := uint32(2)
	req.PageSize = &pageSize

	for {
		resp, err := raw.Plugins(ctx, req)
		if err != nil {
			return fmt.Errorf("raw.Plugins: %w", err)
		}

		pages = append(pages, len(resp.GetPlugins()))

		if resp.GetNextPageToken() == "" {
			break
		}

		token := resp.GetNextPageToken()
		req.PageToken = &token

		if len(pages) > 4 {
			return fmt.Errorf("pagination did not terminate: pages so far %v", pages)
		}
	}

	fmt.Printf("wire pages: %v\n", pages)

	if len(pages) != 3 || pages[0] != 2 || pages[1] != 2 || pages[2] != 1 {
		return fmt.Errorf("want pages [2 2 1], got %v", pages)
	}

	return nil
}
