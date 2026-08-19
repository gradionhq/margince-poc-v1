// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Lead sources are an administered vocabulary (lead_source): the keys a
// human may pick when creating a lead, the labels the UI shows, and the
// intent the scorer reads. Connectors, imports and seeds still write their
// own source values; those appear in the list as "discovered" so an
// administrator can adopt them, and they score neutral until adopted.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// leadVocabularyObject is the RBAC object both vocabularies are gated by:
// the same posture as the custom-field catalog — everyone reads, admin/ops
// write — without adding an object to the policy matrix for a list that
// behaves exactly like one already there.
const leadVocabularyObject = "custom_field"

// SourceIntent is what the scorer makes of a source (formulas §3.1).
type SourceIntent string

const (
	SourceIntentHigh    SourceIntent = "high"
	SourceIntentNeutral SourceIntent = "neutral"
	SourceIntentLow     SourceIntent = "low"
)

// ParseSourceIntent is the seam guard for the three-value intent.
func ParseSourceIntent(raw string) (SourceIntent, error) {
	switch s := SourceIntent(raw); s {
	case SourceIntentHigh, SourceIntentNeutral, SourceIntentLow:
		return s, nil
	}
	return "", &values.ParseError{Field: "intent", Code: "invalid_intent", Message: "intent is one of high, neutral, low"}
}

// SourceIntents maps a stored source key to its intent. A key the table
// does not carry scores neutral; a connector value (`connector:<name>:<id>`)
// resolves through its `connector:<name>` family so adopting the family once
// weights every lead the connector ever wrote.
type SourceIntents map[string]SourceIntent

// Of answers the intent for one stored source value.
func (m SourceIntents) Of(source string) SourceIntent {
	if intent, ok := m[source]; ok {
		return intent
	}
	if family, ok := connectorFamily(source); ok {
		if intent, ok := m[family]; ok {
			return intent
		}
	}
	return SourceIntentNeutral
}

// connectorFamily reduces `connector:apollo:a-1` to `connector:apollo`.
func connectorFamily(source string) (string, bool) {
	parts := strings.SplitN(source, ":", 3)
	if len(parts) == 3 && parts[0] == "connector" {
		return parts[0] + ":" + parts[1], true
	}
	return "", false
}

// defaultSourceIntents is the seeded weighting — the same six keys the
// migration inserts — kept here so the unit scorer has a fixture and so the
// parity test can prove the seed and the code agree.
var defaultSourceIntents = SourceIntents{
	"manual": SourceIntentNeutral, "inbound": SourceIntentHigh, "webform": SourceIntentHigh,
	"referral": SourceIntentHigh, "import": SourceIntentLow, "crawl": SourceIntentLow,
}

// loadSourceIntents reads the installation's weighting inside the caller's
// transaction, for the scorer. Inactive sources still weight: a lead keeps
// its value when its source is retired, and so keeps its score.
func loadSourceIntents(ctx context.Context, tx pgx.Tx) (SourceIntents, error) {
	rows, err := tx.Query(ctx, `SELECT key, intent FROM lead_source`)
	if err != nil {
		return nil, fmt.Errorf("load lead source intents: %w", err)
	}
	defer rows.Close()
	out := SourceIntents{}
	for rows.Next() {
		var key, intent string
		if err := rows.Scan(&key, &intent); err != nil {
			return nil, err
		}
		out[key] = SourceIntent(intent)
	}
	return out, rows.Err()
}

// InactiveLeadSourceError refuses a human's choice of a retired source: the
// list hides it, and the API agrees with the list.
type InactiveLeadSourceError struct{ Key string }

func (e *InactiveLeadSourceError) Error() string {
	return "lead source " + e.Key + " is inactive; pick an active source or reactivate it in Settings › Data model"
}

// FieldFault names the source as the invalid input.
func (e *InactiveLeadSourceError) FieldFault() (field, code, message string) {
	return "source", "inactive_source", e.Error()
}

// ensureHumanSourceAllowed refuses an administered-but-inactive key on a
// human's write. Anything the table does not know (a connector family, an
// import value, a seed) passes: the vocabulary governs the pick list, not
// what capture may record.
func ensureHumanSourceAllowed(ctx context.Context, tx pgx.Tx, source string) error {
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return err
	}
	if actor.Type != principal.PrincipalHuman {
		return nil
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT active FROM lead_source WHERE key = $1`, source).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check lead source %q: %w", source, err)
	}
	if !active {
		return &InactiveLeadSourceError{Key: source}
	}
	return nil
}

// CreateLeadSourceInput is one new administered source.
type CreateLeadSourceInput struct {
	Key       string // optional; derived from Label when empty
	Label     string
	Intent    SourceIntent
	SortOrder int
}

// UpdateLeadSourceInput is a sparse patch; nil leaves the column alone.
type UpdateLeadSourceInput struct {
	Label     *string
	Intent    *SourceIntent
	SortOrder *int
	Active    *bool
}

var nonKeyRunes = regexp.MustCompile(`[^a-z0-9]+`)

// deriveSourceKey turns "Trade show" into "trade_show" — the stable stored
// value behind a label that may later be renamed.
func deriveSourceKey(label string) string {
	key := nonKeyRunes.ReplaceAllString(strings.ToLower(strings.TrimSpace(label)), "_")
	return strings.Trim(key, "_")
}

// leadSourceColumns renders the select list. lead_count carries the caller's
// row scope — a narrowed actor counts only the leads they may see — and
// counts a connector family's leads through the family prefix, because the
// leads carry `connector:<name>:<id>` while the row carries `connector:<name>`.
// The key's underscores are escaped so LIKE reads them as letters.
func leadSourceColumns(ctx context.Context, args *[]any) (string, error) {
	arg := func(v any) int { *args = append(*args, v); return len(*args) }
	scope, err := scopeOrAllRows(ctx, "lead", "", arg)
	if err != nil {
		return "", err
	}
	return `id, key, label, intent, sort_order, active, system, version, created_at, updated_at,
	(SELECT count(*) FROM lead
	  WHERE archived_at IS NULL AND ` + scope + `
	    AND (source = lead_source.key
	         OR (lead_source.key LIKE 'connector:%' AND source LIKE replace(lead_source.key, '_', '\_') || ':%')))`, nil
}

func scanLeadSource(row pgx.Row) (crmcontracts.LeadSource, error) {
	var out crmcontracts.LeadSource
	var id ids.UUID
	var intent string
	var system bool
	var version int64
	var count int
	if err := row.Scan(&id, &out.Key, &out.Label, &intent, &out.SortOrder, &out.Active, &system, &version,
		&out.CreatedAt, &out.UpdatedAt, &count); err != nil {
		return out, err
	}
	out.Id = openapi_types.UUID(id)
	out.Intent = crmcontracts.LeadSourceIntent(intent)
	out.System = &system
	out.Version = &version
	out.LeadCount = &count
	return out, nil
}

// readLeadSource reads one row; lock takes it FOR UPDATE first so a patch
// built from the read cannot be overtaken by a concurrent writer.
func readLeadSource(ctx context.Context, tx pgx.Tx, id ids.UUID, lock bool) (crmcontracts.LeadSource, error) {
	if lock {
		if _, err := storekit.LockRow(ctx, tx, "lead_source", id, storekit.NoArchiveColumn); err != nil {
			return crmcontracts.LeadSource{}, err
		}
	}
	args := []any{id}
	cols, err := leadSourceColumns(ctx, &args)
	if err != nil {
		return crmcontracts.LeadSource{}, err
	}
	out, err := scanLeadSource(tx.QueryRow(ctx, `SELECT `+cols+` FROM lead_source WHERE id = $1`, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, apperrors.ErrNotFound
	}
	return out, err
}

// ListLeadSources answers the administered list plus the discovered values.
func (s *Store) ListLeadSources(ctx context.Context) (crmcontracts.LeadSourceListResponse, error) {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionRead); err != nil {
		return crmcontracts.LeadSourceListResponse{}, err
	}
	out := crmcontracts.LeadSourceListResponse{Data: []crmcontracts.LeadSource{}, Discovered: []crmcontracts.DiscoveredLeadSource{}}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var args []any
		cols, err := leadSourceColumns(ctx, &args)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+cols+` FROM lead_source ORDER BY sort_order, label, id`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			src, err := scanLeadSource(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out.Data = append(out.Data, src)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		out.Discovered, err = discoveredLeadSources(ctx, tx)
		return err
	})
	return out, err
}

// discoveredLeadSources groups the live leads' source values the table does
// not name: connector values by family, everything else by its own value.
// Under the caller's row scope, like every other lead read.
func discoveredLeadSources(ctx context.Context, tx pgx.Tx) ([]crmcontracts.DiscoveredLeadSource, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := scopeOrAllRows(ctx, "lead", "", arg)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx,
		`SELECT CASE WHEN source LIKE 'connector:%'
		             THEN split_part(source, ':', 1) || ':' || split_part(source, ':', 2)
		             ELSE source END AS family, count(*)
		   FROM lead
		  WHERE archived_at IS NULL AND `+scope+`
		    AND source NOT IN (SELECT key FROM lead_source)
		    AND (source NOT LIKE 'connector:%'
		         OR split_part(source, ':', 1) || ':' || split_part(source, ':', 2) NOT IN (SELECT key FROM lead_source))
		  GROUP BY family ORDER BY count(*) DESC, family`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []crmcontracts.DiscoveredLeadSource{}
	for rows.Next() {
		var d crmcontracts.DiscoveredLeadSource
		if err := rows.Scan(&d.Key, &d.LeadCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CreateLeadSource adds an administered source. A key already in the list
// is a 409; a blank label or an unknown intent is a 422.
func (s *Store) CreateLeadSource(ctx context.Context, in CreateLeadSourceInput) (crmcontracts.LeadSource, error) {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionCreate); err != nil {
		return crmcontracts.LeadSource{}, err
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		return crmcontracts.LeadSource{}, &values.ParseError{Field: "label", Code: codeRequired, Message: "label is required"}
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		key = deriveSourceKey(label)
	}
	if key == "" || key != strings.ToLower(key) {
		return crmcontracts.LeadSource{}, &values.ParseError{Field: "key", Code: "invalid_key", Message: "key must be a non-empty lowercase value"}
	}
	intent := in.Intent
	if intent == "" {
		intent = SourceIntentNeutral
	}
	if _, err := ParseSourceIntent(string(intent)); err != nil {
		return crmcontracts.LeadSource{}, err
	}
	var out crmcontracts.LeadSource
	err := s.tx(ctx, func(tx pgx.Tx) error {
		id := ids.NewV7()
		_, err := tx.Exec(ctx,
			`INSERT INTO lead_source (id, key, label, intent, sort_order) VALUES ($1, $2, $3, $4, $5)`,
			id, key, label, string(intent), in.SortOrder)
		if storekit.IsUniqueViolation(err) {
			return apperrors.ErrConflict
		}
		if err != nil {
			return fmt.Errorf("insert lead source: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "create", "lead_source", id, nil,
			map[string]any{"key": key, "label": label, "intent": string(intent)}); err != nil {
			return err
		}
		out, err = readLeadSource(ctx, tx, id, false)
		return err
	})
	return out, err
}

// UpdateLeadSource renames, reorders, re-weights or (de)activates a source.
// The key never changes: it is the value leads carry.
func (s *Store) UpdateLeadSource(ctx context.Context, id ids.UUID, in UpdateLeadSourceInput) (crmcontracts.LeadSource, error) {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionUpdate); err != nil {
		return crmcontracts.LeadSource{}, err
	}
	if in.Label != nil && strings.TrimSpace(*in.Label) == "" {
		return crmcontracts.LeadSource{}, &values.ParseError{Field: "label", Code: codeRequired, Message: "label must not be empty"}
	}
	if in.Intent != nil {
		if _, err := ParseSourceIntent(string(*in.Intent)); err != nil {
			return crmcontracts.LeadSource{}, err
		}
	}
	var out crmcontracts.LeadSource
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := readLeadSource(ctx, tx, id, true)
		if err != nil {
			return err
		}
		p := storekit.NewPatch()
		if in.Label != nil {
			p.Set("label", before.Label, strings.TrimSpace(*in.Label))
		}
		if in.Intent != nil {
			p.Set("intent", string(before.Intent), string(*in.Intent))
		}
		if in.SortOrder != nil {
			p.Set("sort_order", before.SortOrder, *in.SortOrder)
		}
		if in.Active != nil {
			p.Set("active", before.Active, *in.Active)
		}
		if p.Empty() {
			out = before
			return nil
		}
		if err := p.ApplyGuardedIn(ctx, tx, "lead_source", id, nil, storekit.NoArchiveColumn); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "lead_source", id, p.Before(), p.After()); err != nil {
			return err
		}
		out, err = readLeadSource(ctx, tx, id, false)
		return err
	})
	return out, err
}

// DeleteLeadSource removes a source nothing depends on. A built-in or an
// in-use source is a 409: deactivate it instead.
func (s *Store) DeleteLeadSource(ctx context.Context, id ids.UUID) error {
	if err := auth.Require(ctx, leadVocabularyObject, principal.ActionDelete); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		current, err := readLeadSource(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if (current.System != nil && *current.System) || (current.LeadCount != nil && *current.LeadCount > 0) {
			return apperrors.ErrConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM lead_source WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete lead source: %w", err)
		}
		_, err = storekit.Audit(ctx, tx, "erase", "lead_source", id, map[string]any{"key": current.Key, "label": current.Label}, nil)
		return err
	})
}
