// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command contract-overlay downgrades the authoritative OpenAPI 3.1
// contract to 3.0.3 at generate time so oapi-codegen (kin-openapi, 3.0)
// can consume it (B-EP01.9a). The 3.1 crm.yaml stays the single source of
// truth; the overlay output lives in a gitignored build dir and is never
// committed back. The transform itself lives in tools/internal/oas30 (the
// ONE spelling gen-payloads' generator shares, so the two pipelines can
// never disagree on what "3.0-safe" means).
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/gradionhq/margince/backend/tools/internal/oas30"
)

func main() {
	in := flag.String("in", "", "authoritative 3.1 contract")
	out := flag.String("out", "", "3.0.3 overlay output (build artifact)")
	flag.Parse()
	if *in == "" || *out == "" {
		log.Fatal("contract-overlay: -in and -out are required")
	}

	src, err := os.ReadFile(*in)
	if err != nil {
		log.Fatalf("contract-overlay: %v", err)
	}

	converted, err := oas30.Bytes(src)
	if err != nil {
		log.Fatalf("contract-overlay: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		log.Fatalf("contract-overlay: %v", err)
	}
	if err := os.WriteFile(*out, converted, 0o600); err != nil {
		log.Fatalf("contract-overlay: %v", err)
	}
}
