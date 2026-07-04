// Package web serves the built-in UI: a dependency-free, hash-routed SPA
// embedded in the binary (no node toolchain — the PoC's UI budget goes to
// the design language, not a build pipeline). It talks to the same /v1
// contract surface as every other client; there is no privileged backdoor
// (ADR-0013: first-party tools are clients of the public surface).
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// Handler serves the SPA. Client-side routes live behind '#', so only
// real files are requested and no index fallback is needed.
func Handler() http.Handler {
	assets, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // embedded path is compile-time constant
	}
	return http.FileServerFS(assets)
}
