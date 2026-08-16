// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command seed-demo fills a running Margince installation with the demo
// dataset: the real companies read off their own websites, and the people
// those sites publish.
//
// It writes through the ordinary HTTP API, never into the database, so every
// row it creates carries the same audit and outbox trail a user's would.
// That is the point of seeding this way — a demo database assembled behind
// the API proves nothing about the API.
//
// The dataset lives OUTSIDE this repo (it holds real company names, cached
// third-party pages and synthesized addresses for identifiable people), so
// the path is given at run time and defaults to a sibling checkout:
//
//	go run ./tools/seed-demo -dataset ~/develop/margince-demo-database
//
// Converging, not replaying: every company and person is probed before it is
// created, so a second run adds only what the dataset gained since the first.
// Re-running after editing the dataset is the supported way to extend it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed-demo: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	var (
		dataset = flag.String("dataset", filepath.Join(home, "develop", "margince-demo-database"),
			"path to the demo dataset checkout")
		baseURL  = flag.String("api", "http://localhost:8080", "base URL of the running installation")
		email    = flag.String("email", "admin@demo.test", "account to seed as")
		password = flag.String("password", "", "its password (or set MARGINCE_SEED_PASSWORD)")
		limit    = flag.Int("limit", 0, "seed at most N companies (0 = all)")
		dsn      = flag.String("dsn", "", "owner DSN for the teams and seats (or set MARGINCE_SEED_DSN); skipped when empty")
		dryRun   = flag.Bool("dry-run", false, "report what would be created, write nothing")
	)
	flag.Parse()

	if *password == "" {
		*password = os.Getenv("MARGINCE_SEED_PASSWORD")
	}
	if *password == "" {
		return fmt.Errorf("no password: pass -password or set MARGINCE_SEED_PASSWORD")
	}

	demo, err := loadDemoConfig(*dataset)
	if err != nil {
		return err
	}
	companies, err := loadDataset(*dataset, demo.Anchor.Domain, *limit)
	if err != nil {
		return err
	}
	fmt.Printf("dataset: %d company/companies from %s\n", len(companies), *dataset)

	client, err := login(*baseURL, *email, *password)
	if err != nil {
		return err
	}

	anchorRead, err := loadCompany(*dataset, demo.Anchor.Domain)
	if err != nil {
		return err
	}
	if err := seedAnchor(client, demo.Anchor, anchorRead, modeFor(*dryRun)); err != nil {
		return err
	}
	if err := seed(client, companies, *dryRun); err != nil {
		return err
	}

	refs, err := loadPipelineRefs(client, demo, time.Now())
	if err != nil {
		return err
	}
	if err := seedPipeline(client, demo, companies, refs, modeFor(*dryRun)); err != nil {
		return err
	}

	if *dsn == "" {
		*dsn = os.Getenv("MARGINCE_SEED_DSN")
	}
	if *dsn == "" {
		fmt.Println("\nno -dsn given, so the teams and seats were skipped (they need SQL — see users.go)")
		return verifySeed(client, demo, modeFor(*dryRun))
	}
	orgIDs, err := orgIDsByDomain(client)
	if err != nil {
		return err
	}
	if err := seedOrgWithDSN(*dsn, demo, orgIDs, modeFor(*dryRun)); err != nil {
		return err
	}
	facts, err := seedFacts(context.Background(), *dsn, client, companies, orgIDs, modeFor(*dryRun))
	if err != nil {
		return err
	}
	fmt.Printf("facts:         %d applied\n", facts)

	return verifySeed(client, demo, modeFor(*dryRun))
}
