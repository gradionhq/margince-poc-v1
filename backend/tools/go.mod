// Build tooling only (contract-overlay, gen-stubs, gen-agentpolicy): a
// separate module so the oapi-codegen tool directive's dependency zoo
// never lands in the product module's go.mod. The backend require (a
// directory replace) exists so gen-composition validates unit names
// through the ONE published extension.Name rule — scan-time acceptance
// must never drift from boot-time validation.
module github.com/gradionhq/margince/backend/tools

go 1.26.5

tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen

require (
	github.com/getkin/kin-openapi v0.142.0
	github.com/gradionhq/margince/backend v0.0.0
	github.com/oapi-codegen/oapi-codegen/v2 v2.8.0
	golang.org/x/mod v0.38.0
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/gradionhq/margince/backend => ../

require (
	github.com/dlclark/regexp2 v1.12.0 // indirect
	github.com/dprotaso/go-yit v0.0.0-20220510233725-9ba8df137936 // indirect
	github.com/fsnotify/fsnotify v1.5.4 // indirect
	github.com/go-openapi/jsonpointer v0.23.1 // indirect
	github.com/go-openapi/swag/jsonname v0.26.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/oasdiff/yaml v0.1.1 // indirect
	github.com/oasdiff/yaml3 v0.0.14 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/speakeasy-api/jsonpath v0.6.3 // indirect
	github.com/speakeasy-api/openapi v1.24.0 // indirect
	github.com/vmware-labs/yaml-jsonpath v0.3.2 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
)
