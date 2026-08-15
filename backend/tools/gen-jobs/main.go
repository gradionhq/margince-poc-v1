// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command gen-jobs compiles backend/api/jobs.yaml — the declaration of every
// River job kind this build runs — into the two tables the running system
// reads: a kind-keyed Spec table in internal/platform/jobs (data only, no
// knowledge of the composition layer) and, in internal/compose, the closed
// type set a worker may be registered under plus the role assertions that
// used to be hand-written.
//
// A contract that cannot be true fails generation rather than compiling into
// a fleet that behaves differently from what the file says: a kind with no
// chosen timeout, a queue nothing declares, a fan-out edge to a kind that
// does not exist, a dispatcher with no schedule, or an attempt cap on a kind
// whose options the file does not actually govern.
//
// The queues: block is emitted as names and bounds only. Building the queue
// set stays composition's (compose/jobqueues.go) — River takes a QueueConfig,
// and a generated copy of one would be a second spelling of the wiring — but
// the declared bounds are what the census compares that construction against,
// in both directions, so they have to reach the running module as data.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
)

var (
	contractPath = flag.String("contract", "../api/jobs.yaml", "the job contract to compile")
	outSpecsPath = flag.String("out-specs", "../internal/platform/jobs/specs_gen.go", "generated Spec table destination")
	outKindsPath = flag.String("out-kinds", "../internal/compose/jobkinds_gen.go", "generated closed-type-set destination")
)

func main() {
	flag.Parse()

	raw, err := os.ReadFile(*contractPath) // #nosec G304 -- build-time tool, operator-chosen contract path
	if err != nil {
		log.Fatalf("gen-jobs: reading %s: %v", *contractPath, err)
	}

	c, err := parseContract(raw)
	if err != nil {
		log.Fatalf("gen-jobs: %v", err)
	}

	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])

	specsSrc, err := emitSpecs(c, hash)
	if err != nil {
		log.Fatalf("gen-jobs: %v", err)
	}
	if err := os.WriteFile(*outSpecsPath, []byte(specsSrc), 0o600); err != nil {
		log.Fatalf("gen-jobs: writing %s: %v", *outSpecsPath, err)
	}

	kindsSrc, err := emitKinds(c, hash)
	if err != nil {
		log.Fatalf("gen-jobs: %v", err)
	}
	if err := os.WriteFile(*outKindsPath, []byte(kindsSrc), 0o600); err != nil {
		log.Fatalf("gen-jobs: writing %s: %v", *outKindsPath, err)
	}

	fmt.Printf("%d kinds, %d queues generated\n", len(c.Kinds), len(c.Queues))
}
