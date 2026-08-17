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
		verify   = flag.Bool("verify-only", false, "check an already-seeded installation, write nothing")
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
	// What language each company's paper is written in. Read before anything
	// is generated, because a contract's title and its currency both depend
	// on it.
	if err := loadCompanyLocales(*dataset); err != nil {
		return err
	}
	companies, err := loadDataset(*dataset, demo.Anchor.Domain, *limit)
	if err != nil {
		return err
	}
	fmt.Printf("dataset: %d company/companies from %s\n", len(companies), *dataset)

	client, err := loginAllowingAnEarlierSeed(*baseURL, *email, password)
	if err != nil {
		return err
	}
	// A configured bootstrap holds the account until the operator's password
	// is replaced. Doing that first is what lets everything below write at
	// all, and it returns a client signed in on the new credential.
	client, *password, err = replaceOperatorPassword(*baseURL, *email, *password, client)
	if err != nil {
		return err
	}

	// -verify-only reads an installation somebody already seeded and reports
	// what is missing. It writes NOTHING, which makes it safe to point at an
	// installation another session is using — unlike -dry-run, which still
	// walks the seeding phases and needs the records to be absent.
	if *verify {
		return verifySeed(client, demo, modeWrite)
	}

	if *dsn == "" {
		*dsn = os.Getenv("MARGINCE_SEED_DSN")
	}
	// The seats come FIRST. Everything the pipeline writes is assigned to one,
	// and loadPipelineRefs resolves them by reading /v1/users — so on a fresh
	// installation a pipeline that ran first would assign every record to
	// nobody and only then fail in the ownership pass.
	if *dsn == "" {
		fmt.Println("no -dsn given, so the teams and seats are skipped (they need SQL — see users.go)")
	} else if err := seedSeatsWithDSN(*dsn, demo, modeFor(*dryRun)); err != nil {
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
	// One signed-in client per seat, so an activity is recorded by the
	// colleague who had the conversation rather than by whoever ran the seeder.
	seats := newSessions(*baseURL, demo.UserPassword, client)
	if err := seedPipeline(client, seats, demo, companies, refs, modeFor(*dryRun)); err != nil {
		return err
	}

	if err := seedWhatNeedsSQLAfterCompanies(*dsn, *dataset, client, demo, companies, modeFor(*dryRun)); err != nil {
		return err
	}
	return verifySeed(client, demo, modeFor(*dryRun))
}

// loginAllowingAnEarlierSeed signs in, and on a rejected credential tries the
// password THIS tool would have set on an earlier run.
//
// The first seed replaces the operator password with seedAdminPassword, while
// every run after it is handed the original by `make seed-demo`, which reads
// config/margince-admin-password and cannot know that. Re-running the seeder
// is meant to converge rather than stop at a 401, so a rejected credential is
// worth one more attempt before it becomes an error.
//
// It updates password in place, because replaceOperatorPassword needs to know
// which credential the returned client actually holds.
func loginAllowingAnEarlierSeed(baseURL, email string, password *string) (*client, error) {
	client, err := login(baseURL, email, *password)
	if err == nil {
		return client, nil
	}
	if !isUnauthorized(err) || *password == seedAdminPassword {
		return nil, err
	}
	client, err = login(baseURL, email, seedAdminPassword)
	if err != nil {
		return nil, fmt.Errorf("signing in as %s with the supplied password and with the one an earlier seed would have set: %w", email, err)
	}
	*password = seedAdminPassword
	return client, nil
}

// seedWhatNeedsSQLAfterCompanies runs everything that needs the companies on
// file: the finance links, the facts, the logos, and the mailboxes.
func seedWhatNeedsSQLAfterCompanies(dsn, dataset string, client *client, demo demoConfig, companies []company, mode runMode) error {
	if err := seedWhatNeedsCompanies(dsn, dataset, client, demo, companies, mode); err != nil {
		return err
	}
	// The inbox goes on LAST: the connector generates from the accounts,
	// people and deals this run has just written, so a mailbox connected
	// before them would generate from an empty installation.
	_, err := seedCommsConnections(dsn, demo, mode)
	return err
}

// seedWhatNeedsCompanies runs the SQL-and-in-process phases that need the
// companies to exist: the finance billing links, and the company facts (only a
// crawl may create a fact, so this calls people.ApplyDeepRead in process).
func seedWhatNeedsCompanies(dsn, dataset string, client *client, demo demoConfig, companies []company, mode runMode) error {
	if dsn == "" {
		return nil
	}
	orgIDs, err := orgIDsByDomain(client)
	if err != nil {
		return err
	}
	if err := seedFinanceLinksWithDSN(dsn, demo, orgIDs, mode); err != nil {
		return err
	}
	facts, err := seedFacts(context.Background(), dsn, client, companies, orgIDs, mode)
	if err != nil {
		return err
	}
	fmt.Printf("facts:         %d applied\n", facts)

	// seedLogos prints its own line: an upload count and a converged run are
	// different sentences, and it is the one that knows which happened.
	if _, err := seedLogos(context.Background(), dsn, dataset, client, orgIDs, mode); err != nil {
		return err
	}
	return nil
}
