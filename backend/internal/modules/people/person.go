// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DuplicateEmailError carries the existing person for the 409 dedupe
// contract (data-model §3.2: "create with an existing email returns 409 +
// existing id").
type DuplicateEmailError struct {
	Email      string
	ExistingID ids.PersonID
}

func (e *DuplicateEmailError) Error() string {
	return "person with email " + e.Email + " already exists"
}
func (e *DuplicateEmailError) Is(target error) bool { return target == apperrors.ErrConflict }

// PersonEmailInput / PersonPhoneInput are the child rows a create carries.
type PersonEmailInput struct {
	Email     string
	EmailType string
	IsPrimary bool
	Position  int
}

type PersonPhoneInput struct {
	Phone     string
	PhoneType string
	IsPrimary bool
	Position  int
}

type CreatePersonInput struct {
	FullName  string
	FirstName *string
	LastName  *string
	Title     *string
	OwnerID   *ids.UserID
	Social    map[string]any
	Address   *crmcontracts.Address
	Emails    []PersonEmailInput
	Phones    []PersonPhoneInput
	Source    string
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (customfields.go).
	CustomFields map[string]any
}

// CreatePerson inserts the person + child rows + audit + event atomically.
// The email dedupe unique index turns a duplicate into the 409 contract.
func (s *Store) CreatePerson(ctx context.Context, in CreatePersonInput) (crmcontracts.Person, error) {
	if err := auth.Require(ctx, "person", principal.ActionCreate); err != nil {
		return crmcontracts.Person{}, err
	}
	if err := parsePersonContacts(in.Emails, in.Phones); err != nil {
		return crmcontracts.Person{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Person{}, err
	}
	active, err := s.activeColumns(ctx, "person")
	if err != nil {
		return crmcontracts.Person{}, err
	}

	var out crmcontracts.Person
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensurePersonEmailsUnclaimed(ctx, tx, in.Emails); err != nil {
			return err
		}

		match, err := manualDedupePerson(ctx, tx, in)
		if err != nil {
			return err
		}

		wsID := workspaceID(ctx)
		id := ids.New[ids.PersonKind]()
		addr := addressColumns(in.Address)
		cfCols, cfHolders, cfArgs := storekit.InsertFragments(active, in.CustomFields, 16)
		args := []any{
			id, wsID, in.FullName, in.FirstName, in.LastName, in.Title, in.OwnerID,
			addr.Line1, addr.Line2, addr.City, addr.Region, addr.PostalCode, addr.Country,
			in.Source, by,
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO person (id, workspace_id, full_name, first_name, last_name, title, owner_id,
			                     address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
			                     source, captured_by`+cfCols+`)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15`+cfHolders+`)`,
			append(args, cfArgs...)...)
		if err != nil {
			return fmt.Errorf("insert person: %w", err)
		}

		if err := replacePersonSocial(ctx, tx, wsID, id, in.Social); err != nil {
			return err
		}
		if err := insertPersonEmails(ctx, tx, wsID, id, in.Source, by, in.Emails); err != nil {
			return err
		}
		if err := insertPersonPhones(ctx, tx, wsID, id, in.Source, by, in.Phones); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "create", "person", id.UUID, nil, map[string]any{"full_name": in.FullName})
		if err != nil {
			return fmt.Errorf("audit person create: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "person.created", "person", id.UUID, map[string]any{"full_name": in.FullName}); err != nil {
			return fmt.Errorf("emit person.created: %w", err)
		}
		if err := match.recordIfReview(ctx, tx, id, in.FullName, in.Source, by); err != nil {
			return err
		}

		if out, err = readPerson(ctx, tx, id, storekit.LiveOnly, active); err != nil {
			return fmt.Errorf("read created person: %w", err)
		}
		return nil
	})
	return out, err
}

// GetPerson returns one person with child rows; archived rows resolve
// only under IncludeArchived (they stay fetchable by id after merge).
func (s *Store) GetPerson(ctx context.Context, id ids.PersonID, archived storekit.ArchivedFilter) (crmcontracts.Person, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return crmcontracts.Person{}, err
	}
	active, err := s.activeColumns(ctx, "person")
	if err != nil {
		return crmcontracts.Person{}, err
	}
	var out crmcontracts.Person
	err = s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, "person", id.UUID); err != nil {
			return err
		}
		out, err = readPerson(ctx, tx, id, archived, active)
		return err
	})
	return out, err
}

type UpdatePersonInput struct {
	FullName  *string
	FirstName *string
	LastName  *string
	Title     *string
	OwnerID   *ids.UserID
	Social    map[string]any
	Address   *crmcontracts.Address
	IfVersion *int64
	Source    string
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (customfields.go).
	CustomFields map[string]any
}

func (s *Store) UpdatePerson(ctx context.Context, id ids.PersonID, in UpdatePersonInput) (crmcontracts.Person, error) {
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return crmcontracts.Person{}, err
	}
	active, err := s.activeColumns(ctx, "person")
	if err != nil {
		return crmcontracts.Person{}, err
	}
	var out crmcontracts.Person
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", id.UUID); err != nil {
			return err
		}
		current, err := readPerson(ctx, tx, id, storekit.LiveOnly, active)
		if err != nil {
			return fmt.Errorf("read person before update: %w", err)
		}

		p := buildPersonPatch(current, in)
		storekit.SetCustomFieldPatch(p, active, in.CustomFields, current.AdditionalProperties)
		if in.Social != nil {
			// The relation replacement rides the person row's version
			// bump (updated_at below), so If-Match still guards it and
			// the audit row still records the transition.
			p.Set("updated_at", current.UpdatedAt, time.Now().UTC())
		}
		if p.Empty() {
			out = current
			return nil
		}

		if err := p.ApplyGuarded(ctx, tx, "person", id.UUID, in.IfVersion); err != nil {
			return fmt.Errorf("apply person patch: %w", err)
		}
		if in.Social != nil {
			if err := replacePersonSocial(ctx, tx, workspaceID(ctx), id, in.Social); err != nil {
				return err
			}
		}
		before, after := p.Before(), p.After()
		if in.Social != nil {
			before["social"] = current.Social
			after["social"] = in.Social
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "person", id.UUID, before, after)
		if err != nil {
			return fmt.Errorf("audit person update: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "person.updated", "person", id.UUID, after); err != nil {
			return fmt.Errorf("emit person.updated: %w", err)
		}
		if out, err = readPerson(ctx, tx, id, storekit.LiveOnly, active); err != nil {
			return fmt.Errorf("read updated person: %w", err)
		}
		return nil
	})
	return out, err
}

// buildPersonPatch stages only the fields the caller supplied, each
// diffed against the current row so the audit before/after captures the
// real change and an unchanged field is left out of the UPDATE.
func buildPersonPatch(current crmcontracts.Person, in UpdatePersonInput) *storekit.Patch {
	p := storekit.NewPatch()
	if in.FullName != nil {
		p.Set("full_name", current.FullName, *in.FullName)
	}
	if in.FirstName != nil {
		p.Set("first_name", current.FirstName, *in.FirstName)
	}
	if in.LastName != nil {
		p.Set("last_name", current.LastName, *in.LastName)
	}
	if in.Title != nil {
		p.Set("title", current.Title, *in.Title)
	}
	if in.OwnerID != nil {
		p.Set(ownerIDColumn, current.OwnerId, *in.OwnerID)
	}
	if in.Address != nil {
		cur := addressColumns(current.Address)
		p.Set("address_line1", cur.Line1, in.Address.Line1)
		p.Set("address_line2", cur.Line2, in.Address.Line2)
		p.Set("address_city", cur.City, in.Address.City)
		p.Set("address_region", cur.Region, in.Address.Region)
		p.Set("address_postal_code", cur.PostalCode, in.Address.PostalCode)
		p.Set("address_country", cur.Country, in.Address.Country)
	}
	return p
}

// ArchivePerson soft-deletes the person and cascades to its owned child
// rows and referencing edges in the same transaction (data-model §1.10).
func (s *Store) ArchivePerson(ctx context.Context, id ids.PersonID) (crmcontracts.Person, error) {
	if err := auth.Require(ctx, "person", principal.ActionDelete); err != nil {
		return crmcontracts.Person{}, err
	}
	active, err := s.activeColumns(ctx, "person")
	if err != nil {
		return crmcontracts.Person{}, err
	}
	var out crmcontracts.Person
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", id.UUID); err != nil {
			return err
		}
		// A liveness probe, not a wire read — no custom columns needed.
		if _, err := readPerson(ctx, tx, id, storekit.LiveOnly, nil); err != nil {
			return err
		}

		now := time.Now().UTC()
		for _, stmt := range []string{
			`UPDATE person SET archived_at = $2 WHERE id = $1 AND archived_at IS NULL`,
			`UPDATE person_email SET archived_at = $2 WHERE person_id = $1 AND archived_at IS NULL`,
			`UPDATE person_phone SET archived_at = $2 WHERE person_id = $1 AND archived_at IS NULL`,
			`UPDATE relationship SET archived_at = $2 WHERE person_id = $1 AND archived_at IS NULL`,
		} {
			if _, err := tx.Exec(ctx, stmt, id, now); err != nil {
				return err
			}
		}
		// Polymorphic membership/tag rows have no archived_at; the §1.10
		// cleanup rule removes them with the entity.
		if _, err := tx.Exec(ctx,
			`DELETE FROM list_member WHERE entity_type = 'person' AND entity_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM taggable WHERE entity_type = 'person' AND entity_id = $1`, id); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "archive", "person", id.UUID, nil, nil)
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "person.archived", "person", id.UUID, nil); err != nil {
			return err
		}
		out, err = readPerson(ctx, tx, id, storekit.IncludeArchived, active)
		return err
	})
	return out, err
}

// EnsurePersonByEmail resolves the live person who owns email, or
// creates one through the normal governed write path — the idempotent-
// on-email contract of the public capture surfaces (feedback/14): a
// returning booker never becomes a duplicate person.
func (s *Store) EnsurePersonByEmail(ctx context.Context, fullName, email, source string) (ids.UUID, error) {
	if err := auth.Require(ctx, "person", principal.ActionCreate); err != nil {
		return ids.Nil, err
	}
	lookup := func() (ids.UUID, bool, error) {
		var id ids.UUID
		found := false
		err := s.tx(ctx, func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `
				SELECT p.id FROM person p
				JOIN person_email e ON e.person_id = p.id
				WHERE lower(e.email) = lower($1) AND p.archived_at IS NULL
				ORDER BY p.created_at LIMIT 1`, email).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err == nil {
				found = true
			}
			return err
		})
		return id, found, err
	}

	if id, found, err := lookup(); err != nil || found {
		return id, err
	}
	created, err := s.CreatePerson(ctx, CreatePersonInput{
		FullName: fullName,
		Emails:   []PersonEmailInput{{Email: email, EmailType: "work", IsPrimary: true}},
		Source:   source,
	})
	if err == nil {
		return ids.UUID(created.Id), nil
	}
	// A concurrent capture of the same email won the race: its row IS
	// the idempotent answer.
	var dup *DuplicateEmailError
	if errors.As(err, &dup) {
		if id, found, lookupErr := lookup(); lookupErr == nil && found {
			return id, nil
		}
	}
	return ids.Nil, err
}
