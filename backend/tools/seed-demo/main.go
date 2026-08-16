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
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
		dryRun   = flag.Bool("dry-run", false, "report what would be created, write nothing")
	)
	flag.Parse()

	if *password == "" {
		*password = os.Getenv("MARGINCE_SEED_PASSWORD")
	}
	if *password == "" {
		return fmt.Errorf("no password: pass -password or set MARGINCE_SEED_PASSWORD")
	}

	companies, err := loadDataset(*dataset, *limit)
	if err != nil {
		return err
	}
	fmt.Printf("dataset: %d company/companies from %s\n", len(companies), *dataset)

	client, err := login(*baseURL, *email, *password)
	if err != nil {
		return err
	}

	return seed(client, companies, *dryRun)
}
