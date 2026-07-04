// Package sor defines the System-of-Record Provider seam (interfaces.md
// §3, 03e §2.1): the one interface that binds the AI layers, the MCP tool
// surface, and the UI to either crm-core (SoR-mode) or an incumbent
// adapter (Overlay-mode). Nothing above this seam imports crm-core or an
// incumbent SDK directly (AC-OV-1); identical signatures in both modes
// (AC-OV-2).
package sor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gradionhq/margince/backend/kernel/ids"
)

// EntityType names the domain entities the provider serves.
type EntityType string

const (
	EntityPerson       EntityType = "person"
	EntityOrganization EntityType = "organization"
	EntityDeal         EntityType = "deal"
	EntityLead         EntityType = "lead"
	EntityActivity     EntityType = "activity"
)

// EntityRef points at one record.
type EntityRef struct {
	Type EntityType
	ID   ids.UUID
}

// SystemOfRecordProvider abstracts the record store. Verbs mirror the MCP
// tool verbs; writes are split (Create/Update/AdvanceDeal) so AdvanceDeal
// can emit the first-class deal.stage_changed event and apply the
// won/lost 🟡 gate without a verb-sniffing generic write.
type SystemOfRecordProvider interface {
	// Reads are mirror-served in overlay mode to meet P4 read budgets.
	Read(ctx context.Context, ref EntityRef) (Record, error)
	Search(ctx context.Context, q SearchQuery) (SearchResult, error)
	ListObjects(ctx context.Context) ([]ObjectDef, error)
	ListFields(ctx context.Context, objectType EntityType) ([]FieldDef, error)
	RunReport(ctx context.Context, plan ReportPlan) (ReportResult, error)

	// Writes are canonical in SoR-mode and write BACK to the incumbent in
	// overlay mode. Every write carries provenance and the acting
	// Principal from ctx.
	Create(ctx context.Context, in CreateInput) (EntityRef, error)
	Update(ctx context.Context, in UpdateInput) (EntityRef, error)
	AdvanceDeal(ctx context.Context, in AdvanceDealInput) (EntityRef, error)
	// Archive soft-deletes one person/organization/deal (🟡 on the tool
	// surface: a visibility change is hard to undo for whoever needed the
	// row). Leads leave through their own lifecycle verbs.
	Archive(ctx context.Context, ref EntityRef) (EntityRef, error)
	// Merge folds source into target (person/organization only), non-lossy,
	// and returns the survivor's ref (features/01 §1.3). 🟡 on the tool
	// surface: collapsing two records into one is destructive and hard to
	// reverse, so an agent stages it for human confirmation.
	Merge(ctx context.Context, in MergeInput) (EntityRef, error)

	// Freshness lets a 🟡 high-value action force a synchronous live
	// read-through to the incumbent before acting (03e §2.3), bypassing
	// the mirror exactly where correctness matters.
	Freshness(ctx context.Context, ref EntityRef) (FreshnessInfo, error)
}

// Record is one provider-served record plus the trust metadata that rides
// with it (overlay reads are T2-labelled end-to-end, AC-OV-5).
type Record struct {
	Ref       EntityRef
	Fields    json.RawMessage
	Version   int64
	Freshness FreshnessInfo
}

// CreateInput — Fields is the typed domain struct for EntityType
// (*crmcore.Person, …); the provenance stamps are required, not optional.
type CreateInput struct {
	EntityType EntityType
	Fields     any
	Source     string
	CapturedBy string
}

// UpdateInput — IfVersion carries the caller's If-Match value; on skew the
// provider returns errs.ErrVersionSkew and changes nothing.
type UpdateInput struct {
	Ref        EntityRef
	Patch      any
	Source     string
	CapturedBy string
	IfVersion  *int64
}

// AdvanceDealInput moves a deal to a stage; the provider appends
// deal_stage_history and emits deal.stage_changed.
type AdvanceDealInput struct {
	DealID    ids.UUID
	ToStageID ids.UUID
	// LostReason is required when the target stage's semantic is lost
	// (deal_lost_reason); ignored otherwise.
	LostReason *string
	Source     string
	CapturedBy string
	IfVersion  *int64
}

// MergeInput folds SourceID into TargetID (the survivor). Type is person
// or organization only — deals and leads have no merge verb. The audit
// provenance comes from the acting Principal on ctx, like every write.
type MergeInput struct {
	Type     EntityType
	SourceID ids.UUID
	TargetID ids.UUID
}

// FreshnessInfo travels in tool responses so an agent knows mirror
// staleness (03e §2.3). Authoritative is false while pending_sync in
// overlay mode; in SoR-mode it is always true.
type FreshnessInfo struct {
	LastSyncedAt  time.Time
	Authoritative bool
}

// SearchQuery is the governed search shape: full-text plus structured
// filters, cursor-paginated per the contract's keyset convention.
type SearchQuery struct {
	Text        string
	EntityTypes []EntityType
	Filters     map[string]string
	Cursor      string
	Limit       int
}

type SearchResult struct {
	Records    []Record
	NextCursor string
	HasMore    bool
}

// ObjectDef / FieldDef expose schema introspection — ours in SoR-mode,
// the incumbent's in overlay mode.
type ObjectDef struct {
	Type   EntityType
	Label  string
	Fields []FieldDef
}

type FieldDef struct {
	Name     string
	Type     string
	Nullable bool
	Custom   bool // true for fork-owned x_ columns
}

// ReportPlan is a compiled, declarative query plan — never free SQL
// (ADR-0004; the crm gen report golden-file pins its serialization).
type ReportPlan struct {
	Entity  EntityType        `json:"entity"`
	Select  []string          `json:"select"`
	Filter  map[string]string `json:"filter,omitempty"`
	GroupBy []string          `json:"group_by,omitempty"`
}

type ReportResult struct {
	Columns []string
	Rows    [][]any
}
