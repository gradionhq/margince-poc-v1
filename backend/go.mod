module github.com/gradionhq/margince/backend

// Go 1.26 is a deliberate floor (M5): the code uses composite-FK
// `ON DELETE SET NULL (column_list)` semantics and current-toolchain
// tooling. Contributors/operators need the 1.26 toolchain; this is a PoC
// choice, revisit if broader portability becomes a goal.
go 1.26.5

// The composed extension set (ADR-0069): a constant import path the role
// binaries wire — api, worker and mcp today; migrate joins when the
// extension migration namespace lands. Bare builds resolve the committed
// vanilla stub via this replace, `make` lanes override it with the
// generated module under build/composition/ via the generated go.work (a
// workspace `use` wins over a member replace).
require github.com/gradionhq/margince/composition v0.0.0

replace github.com/gradionhq/margince/composition => ../composition

require (
	github.com/emersion/go-imap/v2 v2.0.0-beta.8
	github.com/emersion/go-message v0.18.2
	github.com/getkin/kin-openapi v0.142.0
	github.com/go-chi/chi/v5 v5.3.1
	github.com/go-pdf/fpdf v0.9.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/minio/minio-go/v7 v7.2.1
	github.com/oapi-codegen/runtime v1.6.0
	github.com/redis/go-redis/v9 v9.21.0
	github.com/riverqueue/river v0.40.0
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.40.0
	github.com/riverqueue/river/rivertype v0.40.0
	golang.org/x/crypto v0.54.0
	golang.org/x/net v0.57.0
	golang.org/x/term v0.45.0
	golang.org/x/text v0.40.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6 // indirect
	github.com/go-openapi/jsonpointer v0.23.1 // indirect
	github.com/go-openapi/swag/jsonname v0.26.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/klauspost/cpuid/v2 v2.2.11 // indirect
	github.com/klauspost/crc32 v1.3.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/minio/crc64nvme v1.1.1 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/oasdiff/yaml v0.1.1 // indirect
	github.com/oasdiff/yaml3 v0.0.14 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/riverqueue/river/riverdriver v0.40.0 // indirect
	github.com/riverqueue/river/rivershared v0.40.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/tinylib/msgp v1.6.1 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/goleak v1.3.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/ini.v1 v1.67.2 // indirect
)
