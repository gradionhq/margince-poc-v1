// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file implements the frozen datasource.SystemOfRecordProvider seam
// (interfaces.md §3, design.md §4.5) over the overlay mirror: reads are
// served from MirrorStore (visibility-joined, T2-labelled honest —
// Authoritative is always false); every write verb plus RunReport is
// declared unsupported until branch 2 lands the write-back path.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Provider is the overlay-mode datasource.SystemOfRecordProvider: read
// verbs delegate to ms; Freshness delegates to ff when present, falling
// back to the mirror row's own freshness otherwise (see freshness.go).
// Both may be nil — NewProvider(nil, nil) is the shape the write-verb
// unit tests construct, since those verbs never touch either field.
type Provider struct {
	ms *MirrorStore
	ff *FreshnessReader
}

// NewProvider constructs a Provider over ms (mirror reads) and ff
// (force-fresh reads). Either may be nil; see the Provider doc.
func NewProvider(ms *MirrorStore, ff *FreshnessReader) *Provider {
	return &Provider{ms: ms, ff: ff}
}

var _ datasource.SystemOfRecordProvider = (*Provider)(nil)

// errNoMirrorStore is the honest hard-case answer a read verb gives when
// asked to run against a Provider built with a nil MirrorStore (only the
// write-verb unit tests do this) — a clear, actionable error rather than
// a nil-pointer panic.
func errNoMirrorStore() error {
	return fmt.Errorf("overlay: provider has no mirror store configured")
}

// externalIDToUUID/uuidToExternalID bridge the overlay mirror's natural
// key (object_class, external_id string) to the frozen
// datasource.EntityRef.ID shape (ids.UUID). HubSpot's own object ids are
// always decimal numeric strings (a v1/HubSpot scope assumption, not a
// generic id codec); packing the numeric value into the UUID's low 8
// bytes makes the bridge exactly reversible without a persisted
// external-id<->UUID mapping table. This is a build-repo bridging detail
// to reconcile with the spec upstream: the frozen SystemOfRecordProvider
// seam assumes a UUID-native identity, which overlay's incumbent natural
// key is not: the natural key has no UUID of its own, so this bridge
// packs the numeric id rather than persisting a mapping table.
func externalIDToUUID(externalID string) (ids.UUID, error) {
	n, err := strconv.ParseUint(externalID, 10, 64)
	if err != nil {
		return ids.UUID{}, fmt.Errorf("overlay: external id %q is not numeric — cannot bridge it to the frozen EntityRef.ID shape", externalID)
	}
	var u ids.UUID
	binary.BigEndian.PutUint64(u[8:], n)
	return u, nil
}

// uuidToExternalID reverses externalIDToUUID. It never errors: every
// ids.UUID has a well-defined low-8-bytes integer, even one this package
// never minted itself (that ref simply won't resolve to a mirror row,
// which Get/Read report as apperrors.ErrNotFound like any other miss).
func uuidToExternalID(id ids.UUID) string {
	return strconv.FormatUint(binary.BigEndian.Uint64(id[8:]), 10)
}

// recordFromRow builds a datasource.Record literally from a mirror Row
// — never via datasource.NewRecord, which hardcodes Authoritative:true.
// An overlay mirror read is T2-labelled end-to-end (AC-OV-5): it is
// never allowed to claim the authority only the incumbent itself has.
func recordFromRow(et datasource.EntityType, row Row) (datasource.Record, error) {
	fieldsJSON, err := json.Marshal(row.Fields)
	if err != nil {
		return datasource.Record{}, fmt.Errorf("overlay: marshaling mirror fields for %s/%s: %w", row.ObjectClass, row.ExternalID, err)
	}
	id, err := externalIDToUUID(row.ExternalID)
	if err != nil {
		return datasource.Record{}, err
	}
	return datasource.Record{
		Ref:     datasource.EntityRef{Type: et, ID: id},
		Fields:  fieldsJSON,
		Version: 0,
		Freshness: datasource.FreshnessInfo{
			LastSyncedAt:  row.LastSyncedAt,
			Authoritative: false,
		},
	}, nil
}

// Read serves ref from the mirror (visibility-joined via MirrorStore.Get
// — Read never bypasses to a visibility-blind path).
func (p *Provider) Read(ctx context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	if p.ms == nil {
		return datasource.Record{}, errNoMirrorStore()
	}
	row, err := p.ms.Get(ctx, string(ref.Type), uuidToExternalID(ref.ID))
	if err != nil {
		return datasource.Record{}, err
	}
	return recordFromRow(ref.Type, row)
}

// Search pages one entity type's mirror rows (visibility-joined via
// MirrorStore.List) and applies q.Text as a naive case-insensitive
// substring filter over the row's string-valued fields — a branch-1
// scope limit, not the FTS/RRF hybrid search's own retrieval path.
func (p *Provider) Search(ctx context.Context, q datasource.SearchQuery) (datasource.SearchResult, error) {
	if p.ms == nil {
		return datasource.SearchResult{}, errNoMirrorStore()
	}
	if len(q.EntityTypes) != 1 {
		return datasource.SearchResult{}, fmt.Errorf("overlay: search requires exactly one entity type in this branch, got %d", len(q.EntityTypes))
	}
	et := q.EntityTypes[0]
	rows, next, err := p.ms.List(ctx, string(et), q.Cursor, q.Limit)
	if err != nil {
		return datasource.SearchResult{}, err
	}

	text := strings.ToLower(strings.TrimSpace(q.Text))
	records := make([]datasource.Record, 0, len(rows))
	for _, row := range rows {
		if text != "" && !mirrorRowMatchesText(row, text) {
			continue
		}
		rec, err := recordFromRow(et, row)
		if err != nil {
			return datasource.SearchResult{}, err
		}
		records = append(records, rec)
	}
	return datasource.SearchResult{Records: records, NextCursor: next, HasMore: next != ""}, nil
}

// mirrorRowMatchesText reports whether any string-valued field of row
// contains lowerText.
func mirrorRowMatchesText(row Row, lowerText string) bool {
	for _, v := range row.Fields {
		if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), lowerText) {
			return true
		}
	}
	return false
}

// knownEntityTypes is the fixed set of entity types the overlay mirror
// can ever hold (datasource.go's frozen EntityType constants).
var knownEntityTypes = []datasource.EntityType{
	datasource.EntityPerson,
	datasource.EntityOrganization,
	datasource.EntityDeal,
	datasource.EntityLead,
	datasource.EntityActivity,
}

// schemaSampleSize bounds how many mirrored rows ListFields samples to
// infer a field's presence and shape — introspection has no incumbent
// schema endpoint wired to this seam yet, so it is honestly derived from
// observed mirror data, not fabricated.
const schemaSampleSize = 200

// ListObjects reports every entity type with at least one mirrored row
// (visibility-joined per row, via ListFields).
func (p *Provider) ListObjects(ctx context.Context) ([]datasource.ObjectDef, error) {
	if p.ms == nil {
		return nil, errNoMirrorStore()
	}
	var defs []datasource.ObjectDef
	for _, et := range knownEntityTypes {
		fields, err := p.ListFields(ctx, et)
		if err != nil {
			return nil, err
		}
		if len(fields) == 0 {
			continue
		}
		defs = append(defs, datasource.ObjectDef{Type: et, Label: capitalize(string(et)), Fields: fields})
	}
	return defs, nil
}

// ListFields infers objectType's field set from a sample of its mirrored
// rows (visibility-joined via MirrorStore.List) — the incumbent's own
// authoritative schema, not this build's, per the port's doc comment;
// this seam has no incumbent schema endpoint wired to it yet, so it is
// a best-effort read of what the mirror has actually observed.
func (p *Provider) ListFields(ctx context.Context, objectType datasource.EntityType) ([]datasource.FieldDef, error) {
	if p.ms == nil {
		return nil, errNoMirrorStore()
	}
	rows, _, err := p.ms.List(ctx, string(objectType), "", schemaSampleSize)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var defs []datasource.FieldDef
	for _, row := range rows {
		for k, v := range row.Fields {
			if seen[k] {
				continue
			}
			seen[k] = true
			defs = append(defs, datasource.FieldDef{
				Name:     k,
				Type:     inferFieldKind(v),
				Nullable: true,
				Custom:   strings.HasPrefix(k, "x_"),
			})
		}
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

// inferFieldKind names the coarse JSON-value shape v was decoded as —
// best-effort schema inference from an observed sample, never a
// fabricated incumbent type.
//
//craft:ignore naked-any v is a JSON-decoded incumbent field value; the any is inherent to the decoded shape, not a missed type
func inferFieldKind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return "unknown"
	}
}

// capitalize upper-cases s's first byte, leaving the rest untouched —
// good enough for the lowercase ASCII entity-type names this package
// declares (strings.Title is deprecated and over-generalizes for them).
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// StageSemantic has no incumbent stage-mapping data source wired to this
// seam yet (the Incumbent interface exposes no pipeline/stage lookup,
// and Provider is constructed with no Incumbent reference at all) — it
// is declared unsupported like the write verbs rather than fabricate a
// resolution. design.md §4.5 groups StageSemantic with the read verbs,
// but the branch-1 substrate to serve it genuinely (an
// incumbent->canonical stage map) does not exist yet.
func (p *Provider) StageSemantic(_ context.Context, _ ids.UUID) (string, ids.UUID, error) {
	return "", ids.UUID{}, apperrors.ErrUnsupportedBySoR
}

// RunReport has no HubSpot analogue (design.md §4.5, AC-OV-2's own
// example) — declared unsupported, not silently stubbed.
func (p *Provider) RunReport(_ context.Context, _ datasource.ReportPlan) (datasource.ReportResult, error) {
	return datasource.ReportResult{}, apperrors.ErrUnsupportedBySoR
}

// Create is unsupported until branch 2's write-back path lands.
func (p *Provider) Create(_ context.Context, _ datasource.CreateInput) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, apperrors.ErrUnsupportedBySoR
}

// Update is unsupported until branch 2's write-back path lands.
func (p *Provider) Update(_ context.Context, _ datasource.UpdateInput) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, apperrors.ErrUnsupportedBySoR
}

// AdvanceDeal is unsupported until branch 2's write-back path lands.
func (p *Provider) AdvanceDeal(_ context.Context, _ datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, apperrors.ErrUnsupportedBySoR
}

// Archive is unsupported until branch 2's write-back path lands.
func (p *Provider) Archive(_ context.Context, _ datasource.EntityRef) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, apperrors.ErrUnsupportedBySoR
}

// Merge is unsupported until branch 2's write-back path lands.
func (p *Provider) Merge(_ context.Context, _ datasource.MergeInput) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, apperrors.ErrUnsupportedBySoR
}

// PromoteLead is unsupported until branch 2's write-back path lands.
func (p *Provider) PromoteLead(_ context.Context, _ ids.UUID, _ string, _ *string) (datasource.EntityRef, bool, error) {
	return datasource.EntityRef{}, false, apperrors.ErrUnsupportedBySoR
}

// Freshness delegates to ff (the metered force-fresh reader) when
// configured; otherwise it falls back to the mirror row's own
// freshness, so a Provider built with ff==nil never nil-panics.
func (p *Provider) Freshness(ctx context.Context, ref datasource.EntityRef) (datasource.FreshnessInfo, error) {
	if p.ff != nil {
		return p.ff.Read(ctx, ref)
	}
	if p.ms == nil {
		return datasource.FreshnessInfo{}, errNoMirrorStore()
	}
	row, err := p.ms.Get(ctx, string(ref.Type), uuidToExternalID(ref.ID))
	if err != nil {
		return datasource.FreshnessInfo{}, err
	}
	return datasource.FreshnessInfo{LastSyncedAt: row.LastSyncedAt, Authoritative: false}, nil
}
