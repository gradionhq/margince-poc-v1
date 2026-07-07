// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ids

import (
	"database/sql/driver"
	"fmt"
)

// ID is one entity's identifier: the UUID plus a phantom kind tag. Two
// entities' IDs are distinct types — assignment AND conversion between
// them are compile errors (the zero-size [0]K field blocks conversion),
// so MergePerson(orgID, personID) stops compiling instead of silently
// merging the wrong records. Embedding keeps the UUID's String/IsZero/
// text-marshalling; Value/Scan below carry it through pgx (pgtype falls
// back to driver.Valuer / sql.Scanner for wrapper structs).
type ID[K EntityKind] struct {
	UUID
	_ [0]K
}

// EntityKind is the closed vocabulary of entity tags; only this package
// mints kinds, so an ID's EntityType always comes from the closed set.
type EntityKind interface{ kind() string }

// New mints a fresh UUIDv7 for the entity.
func New[K EntityKind]() ID[K] { return ID[K]{UUID: NewV7()} }

// From is the ONE explicit, greppable widening point: an untyped UUID
// (the wire, a scan, an envelope) asserted to be this entity's id. The
// assertion is the caller's — it belongs at the contracts edge, where
// the route/field already names the entity.
func From[K EntityKind](u UUID) ID[K] { return ID[K]{UUID: u} }

// ParseAs parses the canonical string form into a typed id.
func ParseAs[K EntityKind](s string) (ID[K], error) {
	u, err := Parse(s)
	if err != nil {
		return ID[K]{}, err
	}
	return ID[K]{UUID: u}, nil
}

// EntityType names the id's entity — the closed discriminator string
// the polymorphic tables and the audit/event envelopes carry.
func (id ID[K]) EntityType() string { var k K; return k.kind() }

// Ref pairs the id with its discriminator for the polymorphic seams.
func (id ID[K]) Ref() Ref { return Ref{Type: id.EntityType(), ID: id.UUID} }

func (id ID[K]) Value() (driver.Value, error) { return id.String(), nil }

//craft:ignore naked-any sql.Scanner mandates the any source parameter
func (id *ID[K]) Scan(src any) error {
	switch v := src.(type) {
	case string:
		u, err := Parse(v)
		if err != nil {
			return err
		}
		id.UUID = u
	case []byte:
		if len(v) == 16 {
			copy(id.UUID[:], v)
			return nil
		}
		u, err := Parse(string(v))
		if err != nil {
			return err
		}
		id.UUID = u
	default:
		return fmt.Errorf("ids: cannot scan %T into a typed id", src)
	}
	return nil
}

// The entity vocabulary: one 4-line block per entity. The kind types
// are exported so signatures can name ID[PersonKind] generically, and
// the aliases are the everyday spelling.
type (
	WorkspaceKind    struct{}
	UserKind         struct{}
	TeamKind         struct{}
	PersonKind       struct{}
	OrganizationKind struct{}
	LeadKind         struct{}
	DealKind         struct{}
	PipelineKind     struct{}
	StageKind        struct{}
	OfferKind        struct{}
	ProductKind      struct{}
	ActivityKind     struct{}
	SignalKind       struct{}
	ListKind         struct{}
	TagKind          struct{}
	SavedViewKind    struct{}
	ApprovalKind     struct{}
	AutomationKind   struct{}
	PassportKind     struct{}
	PurposeKind      struct{}
)

func (WorkspaceKind) kind() string    { return "workspace" }
func (UserKind) kind() string         { return "user" }
func (TeamKind) kind() string         { return "team" }
func (PersonKind) kind() string       { return "person" }
func (OrganizationKind) kind() string { return "organization" }
func (LeadKind) kind() string         { return "lead" }
func (DealKind) kind() string         { return "deal" }
func (PipelineKind) kind() string     { return "pipeline" }
func (StageKind) kind() string        { return "stage" }
func (OfferKind) kind() string        { return "offer" }
func (ProductKind) kind() string      { return "product" }
func (ActivityKind) kind() string     { return "activity" }
func (SignalKind) kind() string       { return "signal" }
func (ListKind) kind() string         { return "list" }
func (TagKind) kind() string          { return "tag" }
func (SavedViewKind) kind() string    { return "saved_view" }
func (ApprovalKind) kind() string     { return "approval" }
func (AutomationKind) kind() string   { return "automation" }
func (PassportKind) kind() string     { return "passport" }
func (PurposeKind) kind() string      { return "consent_purpose" }

type (
	WorkspaceID    = ID[WorkspaceKind]
	UserID         = ID[UserKind]
	TeamID         = ID[TeamKind]
	PersonID       = ID[PersonKind]
	OrganizationID = ID[OrganizationKind]
	LeadID         = ID[LeadKind]
	DealID         = ID[DealKind]
	PipelineID     = ID[PipelineKind]
	StageID        = ID[StageKind]
	OfferID        = ID[OfferKind]
	ProductID      = ID[ProductKind]
	ActivityID     = ID[ActivityKind]
	SignalID       = ID[SignalKind]
	ListID         = ID[ListKind]
	TagID          = ID[TagKind]
	SavedViewID    = ID[SavedViewKind]
	ApprovalID     = ID[ApprovalKind]
	AutomationID   = ID[AutomationKind]
	PassportID     = ID[PassportKind]
	PurposeID      = ID[PurposeKind]
)
