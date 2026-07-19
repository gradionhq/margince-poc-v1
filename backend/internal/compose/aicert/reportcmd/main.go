// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command reportcmd prints the AI certification matrix — one row per
// task×provider×model×environment — from the committed records
// aicert.LoadRecords reads back (backend/internal/compose/aicert/records/
// by default). It is a go-run-only developer tool, not a shipped process
// role: `make e2e-ai-report` invokes it directly with `go run`, so it
// never gets a cmd/<role> entry of its own.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
)

func main() {
	dir := flag.String("dir", "internal/compose/aicert/records",
		"directory of certification records (relative to backend/, matching `make e2e-ai-report`'s cwd)")
	flag.Parse()

	records, err := aicert.LoadRecords(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reportcmd: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(renderMatrix(records)) //nolint:forbidigo // this IS the report — reportcmd's whole job is printing the matrix to stdout, not application logging
}
