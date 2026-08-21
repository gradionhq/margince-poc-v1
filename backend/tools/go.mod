// The repo's build and gate tooling: the code generators (contract-overlay,
// gen-stubs, gen-agentpolicy, gen-composition and the rest of gen-*) and
// extmigrategate, the extension migration gate. A separate module so the
// oapi-codegen tool directive's dependency zoo never lands in the product
// module's go.mod. The backend require (a directory replace) exists so
// gen-composition validates unit names through the ONE published
// extension.Name rule — scan-time acceptance must never drift from boot-time
// validation — and so extmigrategate reuses dbmigrate's namespace mapping and
// migration loader rather than restating either.
//
// NOT all of it is offline any more: extmigrategate opens database
// connections (pgx), which is why this module now carries a driver. It is
// still tooling — nothing here ships in a binary a customer runs.
module github.com/gradionhq/margince/backend/tools

go 1.26.6

tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen

require (
	github.com/getkin/kin-openapi v0.146.0
	github.com/gradionhq/margince/backend v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/oapi-codegen/oapi-codegen/v2 v2.8.0
	golang.org/x/crypto v0.55.0
	golang.org/x/mod v0.40.0
	golang.org/x/text v0.41.0
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/gradionhq/margince/backend => ../

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dprotaso/go-yit v0.0.0-20220510233725-9ba8df137936 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-chi/chi/v5 v5.3.1 // indirect
	github.com/go-openapi/jsonpointer v0.23.1 // indirect
	github.com/go-openapi/swag/jsonname v0.26.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/klauspost/cpuid/v2 v2.2.11 // indirect
	github.com/klauspost/crc32 v1.3.0 // indirect
	github.com/minio/crc64nvme v1.1.1 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/minio/minio-go/v7 v7.2.1 // indirect
	github.com/oapi-codegen/runtime v1.6.0 // indirect
	github.com/oasdiff/yaml v0.1.1 // indirect
	github.com/oasdiff/yaml3 v0.0.14 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/redis/go-redis/v9 v9.22.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/speakeasy-api/jsonpath v0.6.3 // indirect
	github.com/speakeasy-api/openapi v1.24.0 // indirect
	github.com/srwiley/oksvg v0.0.0-20221011165216-be6e8873101c // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	github.com/tinylib/msgp v1.6.1 // indirect
	github.com/vmware-labs/yaml-jsonpath v0.3.2 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/image v0.45.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	gopkg.in/ini.v1 v1.67.2 // indirect
)
