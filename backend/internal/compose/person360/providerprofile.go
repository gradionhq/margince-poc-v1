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
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

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
		profile.LatestRun = providerPtr(toWireRun(latest))
		profile.ContributingRuns = providerPtr(contributingRuns(runs))
		if s.providers != nil {
			if desc, err := s.providers.Descriptor(latest.providerName); err == nil {
				profile.CategoriesNotRequested = categoriesNotRequested(desc, latest.requested)
			}
		}
	}
	if err := s.foldClaims(ctx, tx, personID, &profile); err != nil {
		return err
	}
	out.ProviderProfile = &profile
	return nil
}

// providerRunRow is one run as this section reads it: the lifecycle facts and
// what the run was allowed to ask for. Not the frozen snapshot, the
// correlation id or the reservations — none of them is a fact about the
// PERSON, and the run endpoint serves them to anyone who needs them.
type providerRunRow struct {
	id                ids.UUID
	providerName      string
	trigger           string
	connectionVersion int64
	state             string
	claimsUnwritten   bool
	safeCode          string
	requested         []string
	skipReason        string
	createdAt         time.Time
	completedAt       *time.Time
}

// providerRuns reads this person's run history, newest first. Scrubbed runs
// are gone from it by construction: an erasure nulls person_id, so a run that
// no longer names anybody cannot be read back onto their page.
func (s *Service) providerRuns(ctx context.Context, tx pgx.Tx, personID ids.PersonID) ([]providerRunRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, provider, trigger, connection_version, state, claims_unwritten,
		       coalesce(last_safe_status_code, ''), coalesce(skip_reason, ''),
		       requested_categories, created_at, completed_at
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
		if err := rows.Scan(&r.id, &r.providerName, &r.trigger, &r.connectionVersion, &r.state, &r.claimsUnwritten,
			&r.safeCode, &r.skipReason, &r.requested, &r.createdAt, &r.completedAt); err != nil {
			return nil, fmt.Errorf("person360: scanning a provider run: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("person360: reading the provider runs: %w", err)
	}
	return out, nil
}

// toWireRun renders one run for the page: which provider, what was asked for,
// how it ended.
//
// The reservations and the frozen configuration snapshot are deliberately
// left empty. What a run COST is the settings card's subject, and a credit
// figure beside somebody's mobile number invites reading one as the price of
// the other; the run endpoint serves both to a caller who actually needs
// them.
func toWireRun(r providerRunRow) crmcontracts.ProviderRun {
	return crmcontracts.ProviderRun{
		Id:                  openapi_types.UUID(r.id),
		SubjectKind:         crmcontracts.ProviderRunSubjectKindPerson,
		Provider:            crmcontracts.Provider(r.providerName),
		Trigger:             crmcontracts.ProviderRunTrigger(r.trigger),
		State:               crmcontracts.ProviderRunState(r.state),
		ClaimsUnwritten:     r.claimsUnwritten,
		RequestedCategories: r.requested,
		ConnectionVersion:   r.connectionVersion,
		CreatedAt:           r.createdAt,
		UpdatedAt:           r.createdAt,
		CompletedAt:         r.completedAt,
	}
}

// contributingRuns names every run whose claims this snapshot drew on: the
// completed ones that delivered values. Normally the single latest run; after
// a merge it spans BOTH sides, because both were paid for and the section
// shows both (PI-AC-11).
func contributingRuns(runs []providerRunRow) []crmcontracts.ProviderRun {
	out := []crmcontracts.ProviderRun{}
	for _, r := range runs {
		if r.state == string(provider.RunCompleted) && !r.claimsUnwritten {
			out = append(out, toWireRun(r))
		}
	}
	return out
}

// categoriesNotRequested is what nobody asked for — the difference between
// the provider's full vocabulary and what the latest run was authorized to
// request. It is the page's answer to a blank field: "we never asked" is a
// different fact from "we asked and they had nothing", and only this list
// tells them apart.
func categoriesNotRequested(desc provider.Descriptor, requested []string) []string {
	asked := make(map[string]bool, len(requested))
	for _, c := range requested {
		asked[c] = true
	}
	out := []string{}
	for _, c := range desc.Categories {
		if !asked[string(c)] {
			out = append(out, string(c))
		}
	}
	sort.Strings(out)
	return out
}

// resolveProviderState answers the ONE sentence the page shows about this
// person's enrichment. The three "nothing here" states are three different
// facts and must never collapse into one: nobody connected a provider, this
// person is not eligible for one, and nobody has asked yet are different
// answers to "why is this empty", and only the reader can act on the right
// one.
func resolveProviderState(runs []providerRunRow, configured bool) crmcontracts.PersonProviderProfileState {
	if !configured {
		// Disconnected, and the data this installation already PAID for is
		// still on the page below — disconnecting stops new egress, it does
		// not delete what was bought. So the honest word is stale, not
		// not_connected: telling the reader the platform knows nothing while
		// showing them a purchased mobile number is the one thing this state
		// exists to prevent. not_connected is for a page with nothing on it.
		if len(runs) > 0 {
			return crmcontracts.PersonProviderProfileStateStale
		}
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
		// A skip is a decision, and its REASON is what the reader needs. An
		// exhausted budget is a fact about the installation's wallet; telling
		// somebody "this person is not eligible" instead would send them
		// looking at the contact for a problem that is not there.
		return skipStates[latest.skipReason]
	}
	if mapped, ok := providerRunStates[latest.state]; ok {
		return mapped
	}
	return crmcontracts.PersonProviderProfileStateProviderError
}

// skipStates says WHY nothing was bought, in the page's vocabulary. The zero
// value is not_eligible, which is the honest default for the two reasons that
// really are about the subject; everything else names the installation's own
// condition instead.
var skipStates = map[string]crmcontracts.PersonProviderProfileState{
	"":                            crmcontracts.PersonProviderProfileStateNotEligible,
	"not_eligible":                crmcontracts.PersonProviderProfileStateNotEligible,
	"duplicate_subject_candidate": crmcontracts.PersonProviderProfileStateNotEligible,
	// A consent decision, not an eligibility one. It reads as not_eligible
	// today because the contract has no suppressed state; the difference is
	// worth a spec change rather than a wrong word invented here.
	"suppressed":       crmcontracts.PersonProviderProfileStateNotEligible,
	"budget_exhausted": crmcontracts.PersonProviderProfileStateInsufficientCredits,
	"low_balance":      crmcontracts.PersonProviderProfileStateInsufficientCredits,
	"rate_limited":     crmcontracts.PersonProviderProfileStateRateLimited,
	// Nothing was bought because we already hold an answer inside the refresh
	// window — which is a completed enrichment, not a refusal.
	"already_fresh": crmcontracts.PersonProviderProfileStateCompleted,
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
