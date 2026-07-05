// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

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
	ExistingID ids.UUID
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
	OwnerID   *ids.UUID
	Social    map[string]any
	Emails    []PersonEmailInput
	Phones    []PersonPhoneInput
	Source    string
}

// CreatePerson inserts the person + child rows + audit + event atomically.
// The email dedupe unique index turns a duplicate into the 409 contract.
func (s *Store) CreatePerson(ctx context.Context, in CreatePersonInput) (crmcontracts.Person, error) {
	if err := auth.Require(ctx, "person", principal.ActionCreate); err != nil {
		return crmcontracts.Person{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Person{}, err
	}

	var out crmcontracts.Person
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := ensurePersonEmailsUnclaimed(ctx, tx, in.Emails); err != nil {
			return err
		}

		wsID := storekit.MustWorkspace(ctx)
		id := ids.NewV7()
		_, err := tx.Exec(ctx,
			`INSERT INTO person (id, workspace_id, full_name, first_name, last_name, title, owner_id, social, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, coalesce($8, '{}'::jsonb), $9, $10)`,
			id, wsID, in.FullName, in.FirstName, in.LastName, in.Title, in.OwnerID, storekit.JSONArg(in.Social), in.Source, by)
		if err != nil {
			return err
		}

		for _, e := range in.Emails {
			if _, err := tx.Exec(ctx,
				`INSERT INTO person_email (workspace_id, person_id, email, email_type, is_primary, position, source, captured_by)
				 VALUES ($1, $2, lower($3), $4, $5, $6, $7, $8)`,
				wsID, id, e.Email, e.EmailType, e.IsPrimary, e.Position, in.Source, by); err != nil {
				if name, ok := storekit.UniqueViolation(err); ok {
					if name == "uq_person_email_dedupe" {
						// Race with a concurrent create: the transaction is
						// aborted, so no id re-query is possible; the 409
						// omits existing_id.
						return &DuplicateEmailError{Email: e.Email}
					}
					return apperrors.ErrConflict // e.g. two primary emails of one type
				}
				return err
			}
		}
		for _, p := range in.Phones {
			if _, err := tx.Exec(ctx,
				`INSERT INTO person_phone (workspace_id, person_id, phone, phone_type, is_primary, position, source, captured_by)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				wsID, id, p.Phone, p.PhoneType, p.IsPrimary, p.Position, in.Source, by); err != nil {
				return err
			}
		}

		auditID, err := storekit.Audit(ctx, tx, "create", "person", id, nil, map[string]any{"full_name": in.FullName})
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "person.created", "person", id, map[string]any{"full_name": in.FullName}); err != nil {
			return err
		}

		out, err = readPerson(ctx, tx, id, storekit.LiveOnly)
		return err
	})
	return out, err
}

// ensurePersonEmailsUnclaimed is the dedupe pre-check, so the 409 can
// carry the existing id; the unique index remains the structural
// guarantee under races. The existing id is disclosed only when the
// caller could read that row; the conflict itself is still answered
// (existence-hiding survives the 409).
func ensurePersonEmailsUnclaimed(ctx context.Context, tx pgx.Tx, emails []PersonEmailInput) error {
	for _, e := range emails {
		var existing ids.UUID
		err := tx.QueryRow(ctx,
			`SELECT person_id FROM person_email WHERE email = lower($1) AND archived_at IS NULL`,
			e.Email).Scan(&existing)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		dup := &DuplicateEmailError{Email: e.Email}
		visible, err := auth.VisibleTo(ctx, tx, "person", existing)
		if err != nil {
			return err
		}
		if visible {
			dup.ExistingID = existing
		}
		return dup
	}
	return nil
}

// GetPerson returns one person with child rows; archived rows resolve
// only under IncludeArchived (they stay fetchable by id after merge).
func (s *Store) GetPerson(ctx context.Context, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Person, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return crmcontracts.Person{}, err
	}
	var out crmcontracts.Person
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, "person", id); err != nil {
			return err
		}
		out, err = readPerson(ctx, tx, id, archived)
		return err
	})
	return out, err
}

type ListPeopleInput struct {
	Cursor          *string
	Limit           *int
	Query           *string
	OwnerID         *ids.UUID
	IncludeArchived bool
}

func (s *Store) ListPeople(ctx context.Context, in ListPeopleInput) ([]crmcontracts.Person, storekit.Page, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClauseFor(ctx, "person", "", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, storekit.SQLf("search_tsv @@ plainto_tsquery('simple', $%d)", arg(*in.Query)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, storekit.SQLf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var people []crmcontracts.Person
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+personColumns+` FROM person WHERE `+strings.Join(where, " AND ")+
				storekit.SQLf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			p, err := scanPerson(rows)
			if err != nil {
				return err
			}
			people = append(people, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		if len(people) > limit {
			people = people[:limit]
			last := people[len(people)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return attachPersonChildren(ctx, tx, people)
	})
	if people == nil {
		people = []crmcontracts.Person{}
	}
	return people, page, err
}

type UpdatePersonInput struct {
	FullName  *string
	FirstName *string
	LastName  *string
	Title     *string
	OwnerID   *ids.UUID
	Social    map[string]any
	IfVersion *int64
	Source    string
}

func (s *Store) UpdatePerson(ctx context.Context, id ids.UUID, in UpdatePersonInput) (crmcontracts.Person, error) {
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return crmcontracts.Person{}, err
	}
	var out crmcontracts.Person
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", id); err != nil {
			return err
		}
		current, err := readPerson(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}

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
			p.Set("owner_id", current.OwnerId, *in.OwnerID)
		}
		if in.Social != nil {
			p.Set("social", current.Social, storekit.JSONArg(in.Social))
		}
		if p.Empty() {
			out = current
			return nil
		}

		if err := p.Apply(ctx, tx, "person", id, in.IfVersion); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "person", id, p.Before(), p.After())
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "person.updated", "person", id, p.After()); err != nil {
			return err
		}
		out, err = readPerson(ctx, tx, id, storekit.LiveOnly)
		return err
	})
	return out, err
}

// ArchivePerson soft-deletes the person and cascades to its owned child
// rows and referencing edges in the same transaction (data-model §1.10).
func (s *Store) ArchivePerson(ctx context.Context, id ids.UUID) (crmcontracts.Person, error) {
	if err := auth.Require(ctx, "person", principal.ActionDelete); err != nil {
		return crmcontracts.Person{}, err
	}
	var out crmcontracts.Person
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", id); err != nil {
			return err
		}
		if _, err := readPerson(ctx, tx, id, storekit.LiveOnly); err != nil {
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

		auditID, err := storekit.Audit(ctx, tx, "archive", "person", id, nil, nil)
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "person.archived", "person", id, nil); err != nil {
			return err
		}
		out, err = readPerson(ctx, tx, id, storekit.IncludeArchived)
		return err
	})
	return out, err
}

const personColumns = `id, workspace_id, full_name, first_name, last_name, title, owner_id,
	social, merged_into_id, converted_from_lead_id, source, captured_by,
	version, created_at, updated_at, archived_at`

func readPerson(ctx context.Context, tx pgx.Tx, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Person, error) {
	q := `SELECT ` + personColumns + ` FROM person WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	row := tx.QueryRow(ctx, q, id)
	p, err := scanPerson(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Person{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.Person{}, err
	}

	people := []crmcontracts.Person{p}
	if err := attachPersonChildren(ctx, tx, people); err != nil {
		return crmcontracts.Person{}, err
	}
	return people[0], nil
}

func scanPerson(row pgx.Row) (crmcontracts.Person, error) {
	var p crmcontracts.Person
	var id, wsID ids.UUID
	var ownerID, mergedInto, fromLead *ids.UUID
	var social map[string]any
	var version int64

	err := row.Scan(&id, &wsID, &p.FullName, &p.FirstName, &p.LastName, &p.Title, &ownerID,
		&social, &mergedInto, &fromLead, &p.Source, &p.CapturedBy,
		&version, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if err != nil {
		return p, err
	}

	p.Id = openapi_types.UUID(id)
	p.WorkspaceId = openapi_types.UUID(wsID)
	p.OwnerId = uuidPtr(ownerID)
	p.MergedIntoId = uuidPtr(mergedInto)
	p.ConvertedFromLeadId = uuidPtr(fromLead)
	if social != nil {
		p.Social = &social
	}
	p.Version = &version
	return p, nil
}

// attachPersonChildren loads emails + phones for a page in two queries,
// not 2N.
func attachPersonChildren(ctx context.Context, tx pgx.Tx, people []crmcontracts.Person) error {
	if len(people) == 0 {
		return nil
	}
	idx := make(map[openapi_types.UUID]*crmcontracts.Person, len(people))
	personIDs := make([]ids.UUID, len(people))
	for i := range people {
		idx[people[i].Id] = &people[i]
		personIDs[i] = ids.UUID(people[i].Id)
	}

	rows, err := tx.Query(ctx,
		`SELECT person_id, id, email, email_type, is_primary, position, source, captured_by
		 FROM person_email WHERE person_id = ANY($1) AND archived_at IS NULL
		 ORDER BY position, created_at`, personIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var personID, emailID ids.UUID
		var e crmcontracts.PersonEmail
		var email string
		if err := rows.Scan(&personID, &emailID, &email, &e.EmailType, &e.IsPrimary, &e.Position, &e.Source, &e.CapturedBy); err != nil {
			return err
		}
		e.Id = openapi_types.UUID(emailID)
		e.Email = openapi_types.Email(email)
		p := idx[openapi_types.UUID(personID)]
		if p.Emails == nil {
			p.Emails = &[]crmcontracts.PersonEmail{}
		}
		*p.Emails = append(*p.Emails, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	phoneRows, err := tx.Query(ctx,
		`SELECT person_id, id, phone, phone_type, is_primary, position, source, captured_by
		 FROM person_phone WHERE person_id = ANY($1) AND archived_at IS NULL
		 ORDER BY position, created_at`, personIDs)
	if err != nil {
		return err
	}
	defer phoneRows.Close()
	for phoneRows.Next() {
		var personID, phoneID ids.UUID
		var ph crmcontracts.PersonPhone
		if err := phoneRows.Scan(&personID, &phoneID, &ph.Phone, &ph.PhoneType, &ph.IsPrimary, &ph.Position, &ph.Source, &ph.CapturedBy); err != nil {
			return err
		}
		ph.Id = openapi_types.UUID(phoneID)
		p := idx[openapi_types.UUID(personID)]
		if p.Phones == nil {
			p.Phones = &[]crmcontracts.PersonPhone{}
		}
		*p.Phones = append(*p.Phones, ph)
	}
	return phoneRows.Err()
}
