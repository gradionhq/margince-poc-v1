// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// The "Provided by Surfe" snapshot (ADR-0101, PO-EXT-9): what a licensed data
// provider was paid to tell us about this person, kept BESIDE the canonical
// record and never folded into it.
//
// The fold is a UNION over every retained completed run, not newest-per-key.
// After a merge the survivor holds both sides' purchases, and both were paid
// for (PI-AC-11) — newest-per-key would silently discard the losing side's
// answer, which is data the customer bought. Where two runs assert the same
// category they stand as peer assertions, each with its own provider and
// retrieval time, and contributing_runs names every run the section drew on.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// providerProfileSection assembles the snapshot.
//
// The gate is the PERSON read: a claim is a fact about this person, bought
// about them, so seeing it requires seeing them. No separate grant exists and
// none should — a rep who may open the record may see what the installation
// paid to learn about it.
func (s *Service) providerProfileSection(ctx context.Context, tx pgx.Tx, personID ids.PersonID, out *crmcontracts.Person360) error {
	if err := requireRead(ctx, "person"); err != nil {
		return err
	}
	// Whether a provider is connected AT ALL, read from the same row the
	// settings card reads: a page that said "never run" while the card said
	// "not connected" would have the reader looking for a button that is not
	// there. No registry is consulted — a connection row exists only where an
	// adapter registered one.
	var connected bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM provider_connection WHERE status = 'connected')`).
		Scan(&connected); err != nil {
		return fmt.Errorf("person360: reading the provider connection state: %w", err)
	}
	runs, err := s.providerRuns(ctx, tx, personID)
	if err != nil {
		return err
	}
	profile := crmcontracts.PersonProviderProfile{
		State:                  resolveProviderState(runs, connected),
		CategoriesNotRequested: []string{},
		Emails:                 []crmcontracts.PersonProviderEmail{},
		MobilePhones:           []crmcontracts.PersonProviderPhone{},
		JobHistory:             []crmcontracts.PersonProviderJobHistory{},
		Departments:            []string{},
		Seniorities:            []string{},
	}
	if len(runs) > 0 {
		latest := runs[0]
		profile.Provider = providerPtr(crmcontracts.Provider(latest.providerName))
		profile.RetrievedAt = latest.completedAt
		if latest.safeCode != "" {
			profile.SafeStatusCode = providerPtr(latest.safeCode)
		}
	}
	if err := s.foldClaims(ctx, tx, personID, &profile); err != nil {
		return err
	}
	out.ProviderProfile = &profile
	return nil
}

// providerRunRow is one run as this section reads it — the lifecycle facts,
// never the frozen snapshot or the correlation id.
type providerRunRow struct {
	id              string
	providerName    string
	state           string
	claimsUnwritten bool
	safeCode        string
	completedAt     *time.Time
}

// providerRuns reads this person's run history, newest first. Scrubbed runs
// are gone from it by construction: an erasure nulls person_id, so a run that
// no longer names anybody cannot be read back onto their page.
func (s *Service) providerRuns(ctx context.Context, tx pgx.Tx, personID ids.PersonID) ([]providerRunRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, provider, state, claims_unwritten,
		       coalesce(last_safe_status_code, ''), completed_at
		  FROM provider_run
		 WHERE person_id = $1 AND subject_kind = 'person'
		 ORDER BY created_at DESC`, personID)
	if err != nil {
		return nil, fmt.Errorf("person360: reading the provider runs: %w", err)
	}
	defer rows.Close()
	var out []providerRunRow
	for rows.Next() {
		var r providerRunRow
		if err := rows.Scan(&r.id, &r.providerName, &r.state, &r.claimsUnwritten, &r.safeCode, &r.completedAt); err != nil {
			return nil, fmt.Errorf("person360: scanning a provider run: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("person360: reading the provider runs: %w", err)
	}
	return out, nil
}

// resolveProviderState answers the ONE sentence the page shows about this
// person's enrichment. The three "nothing here" states are three different
// facts and must never collapse into one: nobody connected a provider, this
// person is not eligible for one, and nobody has asked yet are different
// answers to "why is this empty", and only the reader can act on the right
// one.
func resolveProviderState(runs []providerRunRow, configured bool) crmcontracts.PersonProviderProfileState {
	if !configured {
		return crmcontracts.PersonProviderProfileStateNotConnected
	}
	if len(runs) == 0 {
		return crmcontracts.PersonProviderProfileStateNeverRun
	}
	latest := runs[0]
	if latest.state == string(provider.RunCompleted) && latest.claimsUnwritten {
		// Paid, and the values never reached the record. Its own state
		// because it is neither a success nor a failure: the customer was
		// charged and has nothing to show for it, which is a thing somebody
		// needs to see rather than a completed run with empty fields.
		return crmcontracts.PersonProviderProfileStateCompletedClaimsUnwritten
	}
	if latest.state == string(provider.RunSkipped) {
		// A skip is a decision, and its reason is what the reader needs: an
		// ineligible subject is not the same fact as an exhausted budget.
		return crmcontracts.PersonProviderProfileStateNotEligible
	}
	if mapped, ok := providerRunStates[latest.state]; ok {
		return mapped
	}
	return crmcontracts.PersonProviderProfileStateProviderError
}

// providerRunStates maps the run machine onto the page's vocabulary. The two
// are deliberately separate: a run state is what the platform is doing, and
// this is what the reader is told.
var providerRunStates = map[string]crmcontracts.PersonProviderProfileState{
	string(provider.RunQueued):            crmcontracts.PersonProviderProfileStateQueued,
	string(provider.RunSubmitting):        crmcontracts.PersonProviderProfileStateInProgress,
	string(provider.RunInProgress):        crmcontracts.PersonProviderProfileStateInProgress,
	string(provider.RunCompleted):         crmcontracts.PersonProviderProfileStateCompleted,
	string(provider.RunNoMatch):           crmcontracts.PersonProviderProfileStateNoMatch,
	string(provider.RunSubmissionUnknown): crmcontracts.PersonProviderProfileStateSubmissionUnknown,
	string(provider.RunCancelled):         crmcontracts.PersonProviderProfileStateNeverRun,
}
