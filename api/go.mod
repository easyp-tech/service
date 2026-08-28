// The generated wire contract, Apache-2.0, as its own module.
//
// It is separate from the service because the service is Elastic License 2.0
// and Go tooling reads licences per module, not per directory: with api/ inside
// the root module, a client that imported it had a licence scanner report
// Elastic-2.0 on their own build, whatever api/LICENSE said.
//
// It still carries the generated MCP registration, and with it the MCP SDK,
// jsonschema-go and oauth2 — protoc-gen-mcp writes into the proto's own Go
// package and has no option to put it anywhere else. See docs on known
// limitations.
module github.com/easyp-tech/service/api

go 1.26.6

require (
	github.com/easyp-tech/protoc-gen-easydoc v0.4.0
	github.com/easyp-tech/protoc-gen-mcp v0.5.0
	github.com/modelcontextprotocol/go-sdk v1.6.1
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
)
