// The Go client, Apache-2.0, as its own module.
//
// Separate from the service for the same reason as api/: a client that imports
// this must not inherit the service's Elastic License 2.0 through the module
// it happens to live in.
module github.com/easyp-tech/service/sdk

go 1.26.6

require (
	github.com/easyp-tech/service/api v0.14.0
	github.com/stretchr/testify v1.11.1
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/easyp-tech/protoc-gen-easydoc v0.4.0 // indirect
	github.com/easyp-tech/protoc-gen-mcp v0.5.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/modelcontextprotocol/go-sdk v1.6.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// For development in this working tree only. A replace directive is honoured
// solely in the main module, so a consumer that runs `go get .../sdk@v0.14.0`
// never sees this line — it resolves the require above from the proxy. That is
// why the version there has to be a tag that exists rather than a placeholder:
// with v0.0.0 the require was unsatisfiable for everyone outside this
// repository, and nothing here would have noticed, because here the replace
// applies.
replace github.com/easyp-tech/service/api => ../api
