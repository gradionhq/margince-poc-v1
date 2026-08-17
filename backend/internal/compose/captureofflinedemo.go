// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the offline demo connector is allowed to know about the installation.
//
// The connector is a pure generator: it turns a mailbox description into
// correspondence and hands it to the sink. Reading people, deals and contracts
// is not capture's business, so the queries live here — the same split the
// finance mirror uses, where offline.go generates and jobs_finance.go reads
// the customer links.
//
// Everything is scoped to ONE SEAT. A rep's inbox holds the accounts that rep
// owns, which is also what the row-scope rules will allow the sink to link, so
// selecting by owner is not a nicety — a thread on an account the seat cannot
// see would be refused at the link and the message lost.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture/offlinedemo"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// offlineDemoDirectory answers the connector's questions from the database.
//
// Every read runs inside a WORKSPACE-BOUND transaction. The tenant tables
// carry FORCE RLS, so the bare pool sees nothing: the first version of this
// used it, the sync completed cleanly, and the generator was handed a mailbox
// with zero accounts every time. A silent empty read is exactly the shape RLS
// is supposed to produce for a caller who has not said which tenant they are.
type offlineDemoDirectory struct{ pool *pgxpool.Pool }

// Mailbox describes one seat and the accounts it owns.
func (d offlineDemoDirectory) Mailbox(ctx context.Context, userID string) (offlinedemo.Mailbox, error) {
	if d.pool == nil {
		return offlinedemo.Mailbox{}, fmt.Errorf("offline demo directory has no database")
	}
	var box offlinedemo.Mailbox
	box.UserID = userID

	err := database.WithWorkspaceTx(ctx, d.pool, func(tx pgx.Tx) error {
		return d.load(ctx, tx, userID, &box)
	})
	if err != nil {
		return box, err
	}
	return box, nil
}

// load does the reading inside one workspace-bound transaction.
func (d offlineDemoDirectory) load(ctx context.Context, tx pgx.Tx, userID string, box *offlinedemo.Mailbox) error {
	err := tx.QueryRow(ctx, `
		SELECT coalesce(display_name, email), email
		  FROM app_user WHERE id = $1`, userID).Scan(&box.DisplayName, &box.Email)
	if err != nil {
		return fmt.Errorf("reading the seat: %w", err)
	}

	// A colleague to CC. Any other seat will do — the point is that a thread
	// sometimes has a third party, not which one. An installation with a
	// single seat has no colleague, which is a real state rather than a
	// failure: the generator simply CCs nobody.
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(display_name, email), email
		  FROM app_user WHERE id <> $1 AND status = 'active'
		 ORDER BY created_at LIMIT 1`, userID).Scan(&box.ColleagueName, &box.ColleagueEmail); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("reading a colleague to copy: %w", err)
		}
		box.ColleagueName, box.ColleagueEmail = "", ""
	}

	accounts, err := d.accounts(ctx, tx, userID)
	if err != nil {
		return err
	}
	box.Accounts = accounts
	return nil
}

// accounts is every company this seat owns, with the parties and commercial
// state a thread can be written from.
func (d offlineDemoDirectory) accounts(ctx context.Context, tx pgx.Tx, userID string) ([]offlinedemo.Account, error) {
	rows, err := tx.Query(ctx, `
		SELECT o.id::text,
		       coalesce(o.display_name, o.legal_name, ''),
		       coalesce((SELECT domain FROM organization_domain
		                  WHERE organization_id = o.id ORDER BY created_at LIMIT 1), ''),
		       coalesce(o.lifecycle, 'unknown'),
		       coalesce((SELECT contract_number FROM contract
		                  WHERE organization_id = o.id AND archived_at IS NULL
		                  ORDER BY created_at DESC LIMIT 1), '')
		  FROM organization o
		 WHERE o.owner_id = $1 AND o.archived_at IS NULL AND NOT o.is_anchor
		 ORDER BY o.created_at, o.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("reading the seat's accounts: %w", err)
	}
	defer rows.Close()

	var out []offlinedemo.Account
	for rows.Next() {
		var a offlinedemo.Account
		if err := rows.Scan(&a.OrganizationID, &a.Name, &a.Domain, &a.Lifecycle, &a.ContractNumber); err != nil {
			return nil, fmt.Errorf("scanning an account: %w", err)
		}
		// The correspondence is dated backward from the run, never forward
		// from the row: in a fresh installation every organization was created
		// today, and a captured message in the future is refused outright.
		a.Now = time.Now().UTC()
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		if err := d.fillParties(ctx, tx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fillParties adds the people to write to and the deal to write about.
func (d offlineDemoDirectory) fillParties(ctx context.Context, tx pgx.Tx, account *offlinedemo.Account) error {
	people, err := tx.Query(ctx, `
		SELECT coalesce(p.full_name, ''), coalesce(e.email, ''), coalesce(r.role, '')
		  FROM relationship r
		  JOIN person p ON p.id = r.person_id
		  LEFT JOIN person_email e ON e.person_id = p.id AND e.is_primary
		 WHERE r.kind = 'employment' AND r.organization_id = $1::uuid
		   AND r.archived_at IS NULL AND p.archived_at IS NULL
		 ORDER BY p.created_at LIMIT 8`, account.OrganizationID)
	if err != nil {
		return fmt.Errorf("reading the people at %s: %w", account.Domain, err)
	}
	defer people.Close()
	for people.Next() {
		var person offlinedemo.Person
		if err := people.Scan(&person.Name, &person.Email, &person.Role); err != nil {
			return fmt.Errorf("scanning a contact: %w", err)
		}
		// A contact with no address is not somebody a mail can be written to.
		if person.Email != "" && person.Name != "" {
			account.People = append(account.People, person)
		}
	}
	if err := people.Err(); err != nil {
		return err
	}

	deals, err := tx.Query(ctx, `
		SELECT d.id::text, coalesce(d.name, ''), coalesce(s.name, '')
		  FROM deal d LEFT JOIN stage s ON s.id = d.stage_id
		 WHERE d.organization_id = $1::uuid AND d.archived_at IS NULL
		 ORDER BY d.created_at LIMIT 2`, account.OrganizationID)
	if err != nil {
		return fmt.Errorf("reading the deals at %s: %w", account.Domain, err)
	}
	defer deals.Close()
	for deals.Next() {
		var deal offlinedemo.Deal
		if err := deals.Scan(&deal.ID, &deal.Name, &deal.Stage); err != nil {
			return fmt.Errorf("scanning a deal: %w", err)
		}
		account.Deals = append(account.Deals, deal)
	}
	return deals.Err()
}
