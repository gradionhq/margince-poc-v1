// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Consent, and the finance links that make a customer's revenue card appear.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// seedConsent records who agreed to be contacted about what.
//
// A marketing grant runs the real double-opt-in round trip — mint the token,
// then redeem it — rather than writing the state. A marketing consent nobody
// confirmed is precisely the record the product exists to refuse, so faking
// one here would seed a lie into the surface that is supposed to catch it.
func seedConsent(c *client, cfg demoConfig, companies []company, refs pipelineRefs, mode runMode) (int, error) {
	purposes, err := loadPurposes(c, mode)
	if err != nil {
		return 0, err
	}
	peopleByDomain := map[string][]datasetPers{}
	for _, comp := range companies {
		peopleByDomain[strings.ToLower(comp.Domain)] = comp.People
	}

	recorded := 0
	for _, want := range cfg.Consent {
		people := peopleByDomain[strings.ToLower(want.Company)]
		if want.PersonIndex >= len(people) {
			// The read found fewer people than the dataset assumed. Skipping
			// is right: a consent state belongs to a named person, and there
			// is no honest way to move it to somebody else.
			continue
		}
		person := people[want.PersonIndex]
		email, _ := person.email()
		if email == "" || mode == modeDryRun {
			if email != "" {
				recorded++
			}
			continue
		}
		// Free-text search does not index addresses, so the person is found
		// by the name the page printed — which is what the record carries.
		personID, found, err := findPersonByName(c, person.Name)
		if err != nil {
			return recorded, err
		}
		if !found {
			continue
		}
		purpose, ok := purposes[want.Purpose]
		if !ok {
			return recorded, fmt.Errorf("consent names purpose %q, which this workspace does not define", want.Purpose)
		}

		body := jsonBody{"purpose_id": purpose.id, "new_state": want.State, "source": seedSource}
		if want.State == "granted" && purpose.requiresDOI {
			token, err := issueDoubleOptIn(c, personID, purpose.id)
			if err != nil {
				return recorded, fmt.Errorf("consent for %s: %w", email, err)
			}
			body["double_opt_in_token"] = token
		}
		// Recording the state a person is already in is a no-op the API
		// accepts, so the count reflects what CHANGED rather than what was
		// sent — otherwise every run reports the same six as fresh.
		current, err := consentState(c, personID, purpose.id)
		if err != nil {
			return recorded, err
		}
		if current == want.State {
			continue
		}
		if err := c.post("/v1/people/"+personID+"/consent", body, nil); err != nil {
			return recorded, fmt.Errorf("consent for %s: %w", email, err)
		}
		recorded++
	}
	return recorded, nil
}

// consentState reads what a person has already agreed to for one purpose.
func consentState(c *client, personID, purposeID string) (string, error) {
	// The reply is a consent LEDGER — the current state per purpose, plus the
	// events that produced it — not a paged list, so it has no "data" key.
	var ledger struct {
		State []struct {
			PurposeID string `json:"purpose_id"`
			State     string `json:"state"`
		} `json:"state"`
	}
	if err := c.get("/v1/people/"+personID+"/consent", nil, &ledger); err != nil {
		return "", fmt.Errorf("reading consent: %w", err)
	}
	for _, row := range ledger.State {
		if row.PurposeID == purposeID {
			return row.State, nil
		}
	}
	return "", nil
}

// findPersonByName resolves a seeded person by their printed name.
func findPersonByName(c *client, name string) (string, bool, error) {
	var page struct {
		Data []struct {
			ID       string `json:"id"`
			FullName string `json:"full_name"`
		} `json:"data"`
	}
	if err := c.get("/v1/people", url.Values{"q": {name}, "limit": {"10"}}, &page); err != nil {
		return "", false, fmt.Errorf("searching for %s: %w", name, err)
	}
	for _, row := range page.Data {
		if strings.EqualFold(row.FullName, name) {
			return row.ID, true, nil
		}
	}
	return "", false, nil
}

type consentPurpose struct {
	id          string
	requiresDOI bool
}

func loadPurposes(c *client, mode runMode) (map[string]consentPurpose, error) {
	out := map[string]consentPurpose{}
	if mode == modeDryRun {
		return out, nil
	}
	var page struct {
		Data []struct {
			ID                  string `json:"id"`
			Key                 string `json:"key"`
			RequiresDoubleOptIn bool   `json:"requires_double_opt_in"`
		} `json:"data"`
	}
	if err := c.get("/v1/consent-purposes", nil, &page); err != nil {
		return nil, fmt.Errorf("listing consent purposes: %w", err)
	}
	for _, row := range page.Data {
		out[row.Key] = consentPurpose{id: row.ID, requiresDOI: row.RequiresDoubleOptIn}
	}
	return out, nil
}

// issueDoubleOptIn mints the confirmation token a marketing grant redeems.
// deliver=false: the demo has no mailbox to receive it, and sending real
// confirmation mail to a synthesized address is exactly what must not happen.
func issueDoubleOptIn(c *client, personID, purposeID string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	body := jsonBody{"purpose_id": purposeID, "deliver": false}
	if err := c.post("/v1/people/"+personID+"/consent/double-opt-in", body, &out); err != nil {
		return "", fmt.Errorf("minting the double-opt-in token: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("the double-opt-in mint returned no token")
	}
	return out.Token, nil
}

// seedFinanceLinks marks which customers have a billing relationship.
//
// It writes two rows and stops. The product's own offline_demo provider
// generates the ledger from them on the next sync — invoices, payments, a
// credit note — so the seeder never writes an invoice: those rows live behind
// the finance mirror (ADR-0083) and the next sync would overwrite anything
// put there by hand.
func seedFinanceLinks(ctx context.Context, conn *pgx.Conn, cfg demoConfig, orgIDs map[string]string, mode runMode) (int, error) {
	if mode == modeDryRun || len(cfg.FinanceCustomers) == 0 {
		return 0, nil
	}
	// captured_by names the principal a row is attributable to, and the API
	// stamps it from the session. This phase writes SQL, so it reads the same
	// value off a row the API already wrote rather than inventing a principal
	// that answers to nobody.
	var capturedBy string
	if err := conn.QueryRow(ctx,
		`SELECT captured_by FROM organization WHERE captured_by LIKE 'human:%' LIMIT 1`).Scan(&capturedBy); err != nil {
		return 0, fmt.Errorf("resolving the seeding principal: %w", err)
	}
	// finance_connection and finance_customer_link carry NO workspace_id: the
	// installation is a singleton, so the tenant column was dropped and these
	// rows are installation-wide by construction.
	var connectionID string
	err := conn.QueryRow(ctx,
		`SELECT id FROM finance_connection WHERE provider = 'offline_demo'`).Scan(&connectionID)
	if err == pgx.ErrNoRows {
		if err := conn.QueryRow(ctx,
			// credential_ref names where the provider's secret lives. The
			// offline demo provider has no secret to keep, so the reference is
			// a stated placeholder rather than an empty string pretending to be
			// one.
			`INSERT INTO finance_connection (provider, status, credential_ref, source, captured_by)
			 VALUES ('offline_demo', 'active', 'offline-demo-no-credential', $1, $2) RETURNING id`,
			seedSource, capturedBy).Scan(&connectionID); err != nil {
			return 0, fmt.Errorf("creating the finance connection: %w", err)
		}
	} else if err != nil {
		return 0, fmt.Errorf("looking up the finance connection: %w", err)
	}

	linked := 0
	for _, domain := range cfg.FinanceCustomers {
		orgID, ok := orgIDs[strings.ToLower(domain)]
		if !ok {
			continue
		}
		tag, err := conn.Exec(ctx,
			// sync_hash is how the mirror notices a customer changed at the
			// source. Nothing in the product writes these links — the sync only
			// reads them — so an operator provisions them, and the hash is
			// seeded from the link's own identity. The next sync recomputes it
			// from the provider's answer, which is the point: a hash that never
			// matches means every customer looks changed forever.
			`INSERT INTO finance_customer_link (connection_id, organization_id, external_customer_id, sync_hash, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (connection_id, external_customer_id) WHERE archived_at IS NULL DO NOTHING`,
			connectionID, orgID, "demo-"+domain, "seed:"+domain, seedSource, capturedBy)
		if err != nil {
			return linked, fmt.Errorf("linking %s to finance: %w", domain, err)
		}
		linked += int(tag.RowsAffected())
	}
	return linked, nil
}

// orgIDsByDomain re-reads the seeded organizations so the finance phase can
// map a dataset domain to the row it created.
func orgIDsByDomain(c *client) (map[string]string, error) {
	out := map[string]string{}
	err := c.getAll("/v1/organizations", nil, func(raw json.RawMessage) error {
		var rows []struct {
			ID      string `json:"id"`
			Domains []struct {
				Domain string `json:"domain"`
			} `json:"domains"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			for _, dom := range row.Domains {
				out[strings.ToLower(dom.Domain)] = row.ID
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing organizations: %w", err)
	}
	return out, nil
}
