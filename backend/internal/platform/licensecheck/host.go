// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package licensecheck

// The wazero host that runs the bundled validation module in this process.
//
// Vendored from margince-constellation's tools/licensecheckwasm/host/host.go at
// 9e9c638b8d03f195abfb60317999435ca6400fbf, the same commit module/ was built
// from, and kept in step with it by hand. It is a copy rather than an import
// because that package lives in a private module: a public source installation
// could not resolve the import path, so importing it would make this product
// unbuildable for exactly the people who need to prove their entitlement.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// Grants is the set of attributes a product grant carries, decoded from the
// module's output. JSON numbers decode to float64.
type Grants = map[string]any

// check runs the module (compiled wasm bytes, gzipped or raw) to validate token
// for product at generation, issued by issuer. It returns the granted
// attributes, or an error when the module rejects the license or fails to run.
// The token is passed to the module as the MARGINCE_LICENSE environment
// variable; issuer, product and generation are passed as arguments.
func check(ctx context.Context, module []byte, issuer, product string, generation int, token string) (Grants, error) {
	module, err := maybeDecompress(module)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, module, issuer, product, generation, token)
	if err != nil {
		return nil, err
	}
	return decodeGrants(out)
}

// maybeDecompress gunzips module when it carries the gzip magic bytes, so the
// compressed module — about a quarter the size — can be embedded and passed
// straight to check. Raw wasm (no gzip header) is returned unchanged.
func maybeDecompress(module []byte) ([]byte, error) {
	if len(module) < 2 || module[0] != 0x1f || module[1] != 0x8b {
		return module, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(module))
	if err != nil {
		return nil, fmt.Errorf("gunzip module: %w", err)
	}
	//craft:ignore swallowed-errors a read-only gzip reader over a byte slice holds no resource whose close can fail meaningfully
	defer func() { _ = reader.Close() }()
	out, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("gunzip module: %w", err)
	}
	return out, nil
}

// run instantiates the module and returns its stdout. A non-zero exit is the
// module's way of rejecting a license; run turns it into an error carrying the
// module's stderr.
func run(ctx context.Context, module []byte, issuer, product string, generation int, token string) ([]byte, error) {
	runtime := wazero.NewRuntime(ctx)
	//craft:ignore swallowed-errors the runtime is torn down after its single-shot verdict is already in hand; a close failure cannot change it
	defer func() { _ = runtime.Close(ctx) }()
	wasi_snapshot_preview1.MustInstantiate(ctx, runtime)

	var stdout, stderr bytes.Buffer
	config := wazero.NewModuleConfig().
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithEnv("MARGINCE_LICENSE", token).
		WithArgs("module", issuer, product, strconv.Itoa(generation)).
		// The module checks the license against the current time (expiry, grace,
		// not-before). Without the real clock wazero uses a fake one fixed at the
		// epoch, so every unexpired license would read as "used before issued".
		WithSysWalltime().
		WithSysNanotime()

	// A Go wasip1 module that exits 0 returns a nil error; a non-zero exit
	// returns an ExitError (the module rejecting the license); any other error is
	// a run failure (a malformed module or a trap).
	_, err := runtime.InstantiateWithConfig(ctx, module, config)
	if err == nil {
		return stdout.Bytes(), nil
	}
	var exit *sys.ExitError
	if errors.As(err, &exit) {
		return nil, fmt.Errorf("license rejected: %s", strings.TrimSpace(stderr.String()))
	}
	return nil, fmt.Errorf("run module: %w", err)
}

// decodeGrants parses the module's JSON output into grants.
func decodeGrants(out []byte) (Grants, error) {
	var grants Grants
	if err := json.Unmarshal(out, &grants); err != nil {
		return nil, fmt.Errorf("decode grants: %w", err)
	}
	return grants, nil
}
