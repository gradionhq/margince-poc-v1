// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Writing a field discovered in a public search RESULT (ADR-0081 / A126).
//
// The distinction from the site-read pass next door is the source, and it is
// the whole point of the seam: nothing here read the page the value points
// at. A search index returned a title, a snippet and a URL, and those alone
// carry the fact. That is what lets a LinkedIn profile address reach a
// contact while the platform itself is never fetched — the address is
// evidence of where the claim appears, and the deny-list keeps it at that.
//
// Everything is fill-only-empty and evidence-backed, exactly as the signature
// and site-read passes are, so first verdict wins and a human's answer is
// structurally untouchable.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// searchFieldSource is the DM-CONV-11 channel for a value read out of a
// public search result. It is distinct from site_read so a reader can tell a
// value we found by searching from one we found by reading a page, and so an
// Art. 14 answer can name which it was.
const searchFieldSource = "web_search"

// DiscoveredField is one value a public search result asserted, with the
// result text it was read from.
type DiscoveredField struct {
	// Field is the person_profile_field key — 'linkedin' today; the enum on
	// the table is what bounds this, not a list here.
	Field string
	Value string

	// EvidenceSnippet is the result's own title and description, verbatim.
	// It is the receipt the reader checks the value against, and it exists
	// without anyone having fetched Value's page.
	EvidenceSnippet string

	// SourceRef names the search that found it, so the claim can say which
	// index answered and when.
	SourceRef string
}

// ApplyDiscoveredFields fills empty fields on a person from public search
// results. It reports which fields it wrote.
//
// Row-scoped like every read that names a record: a person the caller cannot
// see is ErrNotFound rather than a silent no-op, because a caller that cannot
// see the record must not learn it exists by watching a write succeed.
func (s *Store) ApplyDiscoveredFields(ctx context.Context, personID ids.PersonID, fields []DiscoveredField) ([]string, error) {
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return nil, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return nil, err
	}
	var applied []string
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", personID.UUID); err != nil {
			return err
		}
		out, err := fillDiscoveredFields(ctx, tx, personID, by, fields)
		if err != nil {
			return err
		}
		applied = out
		return nil
	})
	return applied, err
}

// fillDiscoveredFields writes the evidence rows and stamps provenance. The
// evidence row is the admission ticket and first verdict wins, so a search
// result can never overwrite what a signature, a site read or a human
// already answered.
func fillDiscoveredFields(ctx context.Context, tx pgx.Tx, personID ids.PersonID, by string, fields []DiscoveredField) ([]string, error) {
	wsID := workspaceID(ctx)
	var applied []string
	for _, f := range fields {
		value := strings.TrimSpace(f.Value)
		snippet := strings.TrimSpace(f.EvidenceSnippet)
		// Evidence-or-omit is enforced here rather than trusted: a value with
		// no result text behind it is exactly the unciteable claim this seam
		// exists to refuse.
		if value == "" || snippet == "" {
			continue
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO person_profile_field (workspace_id, person_id, field, value, evidence_snippet, source_ref, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (person_id, field) DO NOTHING`,
			wsID, personID, f.Field, value, snippet, f.SourceRef, searchFieldSource, by)
		if err != nil {
			return nil, fmt.Errorf("people: discovered field evidence row (%s): %w", f.Field, err)
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if err := storekit.StampFields(ctx, tx, entityPerson, personID.UUID, f.SourceRef, by,
			[]storekit.FieldStamp{{Field: f.Field}}); err != nil {
			return nil, err
		}
		applied = append(applied, f.Field)
	}
	return applied, nil
}
