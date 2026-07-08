// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// addressColumns destructures the contract's Address into the six
// person/organization columns; a nil address is six NULLs.
func addressColumns(a *crmcontracts.Address) crmcontracts.Address {
	if a == nil {
		return crmcontracts.Address{}
	}
	return *a
}

// addressOrNil collapses six all-NULL columns back to "no address" so
// the wire shape stays a null, not an empty object.
func addressOrNil(a crmcontracts.Address) *crmcontracts.Address {
	if a.Line1 == nil && a.Line2 == nil && a.City == nil && a.Region == nil &&
		a.PostalCode == nil && a.Country == nil {
		return nil
	}
	return &a
}

// replacePersonSocial makes the person_social relation mirror the given
// (platform → handle) map — the queryable form of what used to hide in
// a jsonb column. nil means "not supplied": existing rows stand.
func replacePersonSocial(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, personID ids.PersonID, social map[string]any) error {
	if social == nil {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM person_social WHERE person_id = $1`, personID); err != nil {
		return fmt.Errorf("clear person social rows: %w", err)
	}
	for platform, handle := range social {
		text := fmt.Sprintf("%v", handle)
		if strings.TrimSpace(platform) == "" || strings.TrimSpace(text) == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO person_social (workspace_id, person_id, platform, handle) VALUES ($1, $2, $3, $4)`,
			wsID, personID, platform, text); err != nil {
			return fmt.Errorf("insert person social row: %w", err)
		}
	}
	return nil
}

// parsePersonContacts is the parse-don't-validate seam for a person's
// contact rows: addresses normalize to the lowercased form the dedupe
// index compares (the SQL lower() below stays as defense in depth) and
// phones normalize to E.164 — making the schema's "E.164 normalized at
// write" contract true instead of documentary. Values are written back
// in place so everything downstream handles only normalized strings.
func parsePersonContacts(emails []PersonEmailInput, phones []PersonPhoneInput) error {
	for i, e := range emails {
		parsed, err := values.ParseEmail(e.Email)
		if err != nil {
			return err
		}
		emails[i].Email = parsed.String()
	}
	for i, p := range phones {
		parsed, err := values.ParsePhone(p.Phone)
		if err != nil {
			return err
		}
		phones[i].Phone = parsed.String()
	}
	return nil
}

// insertPersonEmails lands the person's emails; the unique index stays
// the structural guarantee under races, mapping uq_person_email_dedupe
// to the typed 409 (which omits existing_id — the aborted transaction
// cannot re-query) and two primary emails of one type to a plain conflict.
func insertPersonEmails(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, personID ids.PersonID, source, by string, emails []PersonEmailInput) error {
	for _, e := range emails {
		if _, err := tx.Exec(ctx,
			`INSERT INTO person_email (workspace_id, person_id, email, email_type, is_primary, position, source, captured_by)
			 VALUES ($1, $2, lower($3), $4, $5, $6, $7, $8)`,
			wsID, personID, e.Email, e.EmailType, e.IsPrimary, e.Position, source, by); err != nil {
			if name, ok := storekit.UniqueViolation(err); ok {
				if name == "uq_person_email_dedupe" {
					return &DuplicateEmailError{Email: e.Email}
				}
				return apperrors.ErrConflict
			}
			return fmt.Errorf("insert person email: %w", err)
		}
	}
	return nil
}

// insertPersonPhones lands the person's phone rows.
func insertPersonPhones(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, personID ids.PersonID, source, by string, phones []PersonPhoneInput) error {
	for _, p := range phones {
		if _, err := tx.Exec(ctx,
			`INSERT INTO person_phone (workspace_id, person_id, phone, phone_type, is_primary, position, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			wsID, personID, p.Phone, p.PhoneType, p.IsPrimary, p.Position, source, by); err != nil {
			return fmt.Errorf("insert person phone: %w", err)
		}
	}
	return nil
}

// ensurePersonEmailsUnclaimed is the dedupe pre-check, so the 409 can
// carry the existing id; the unique index remains the structural
// guarantee under races. The existing id is disclosed only when the
// caller could read that row; the conflict itself is still answered
// (existence-hiding survives the 409).
func ensurePersonEmailsUnclaimed(ctx context.Context, tx pgx.Tx, emails []PersonEmailInput) error {
	for _, e := range emails {
		var existing ids.PersonID
		err := tx.QueryRow(ctx,
			`SELECT person_id FROM person_email WHERE email = lower($1) AND archived_at IS NULL`,
			e.Email).Scan(&existing)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("probe email dedupe: %w", err)
		}
		dup := &DuplicateEmailError{Email: e.Email}
		visible, err := auth.VisibleTo(ctx, tx, "person", existing.UUID)
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

const personColumns = `id, workspace_id, full_name, first_name, last_name, title, owner_id,
	address_line1, address_line2, address_city, address_region, address_postal_code, address_country,
	merged_into_id, converted_from_lead_id, source, captured_by,
	version, created_at, updated_at, archived_at`

func readPerson(ctx context.Context, tx pgx.Tx, id ids.PersonID, archived storekit.ArchivedFilter) (crmcontracts.Person, error) {
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
	var addr crmcontracts.Address
	var version int64

	err := row.Scan(&id, &wsID, &p.FullName, &p.FirstName, &p.LastName, &p.Title, &ownerID,
		&addr.Line1, &addr.Line2, &addr.City, &addr.Region, &addr.PostalCode, &addr.Country,
		&mergedInto, &fromLead, &p.Source, &p.CapturedBy,
		&version, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if err != nil {
		return p, err
	}

	p.Id = openapi_types.UUID(id)
	p.WorkspaceId = openapi_types.UUID(wsID)
	p.OwnerId = uuidPtr(ownerID)
	p.MergedIntoId = uuidPtr(mergedInto)
	p.ConvertedFromLeadId = uuidPtr(fromLead)
	if a := addressOrNil(addr); a != nil {
		p.Address = a
	}
	p.Version = &version
	return p, nil
}

// attachPersonChildren loads emails + phones + social for a page in
// three queries, not 3N.
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
	if err := attachPersonEmails(ctx, tx, idx, personIDs); err != nil {
		return err
	}
	if err := attachPersonPhones(ctx, tx, idx, personIDs); err != nil {
		return err
	}
	return attachPersonSocial(ctx, tx, idx, personIDs)
}

func attachPersonEmails(ctx context.Context, tx pgx.Tx, idx map[openapi_types.UUID]*crmcontracts.Person, personIDs []ids.UUID) error {
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
	return rows.Err()
}

func attachPersonPhones(ctx context.Context, tx pgx.Tx, idx map[openapi_types.UUID]*crmcontracts.Person, personIDs []ids.UUID) error {
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

func attachPersonSocial(ctx context.Context, tx pgx.Tx, idx map[openapi_types.UUID]*crmcontracts.Person, personIDs []ids.UUID) error {
	// The wire keeps social as the (platform → handle) map; the relation
	// is the stored form.
	socialRows, err := tx.Query(ctx,
		`SELECT person_id, platform, handle FROM person_social WHERE person_id = ANY($1)
		 ORDER BY platform`, personIDs)
	if err != nil {
		return err
	}
	defer socialRows.Close()
	for socialRows.Next() {
		var personID ids.UUID
		var platform, handle string
		if err := socialRows.Scan(&personID, &platform, &handle); err != nil {
			return err
		}
		p := idx[openapi_types.UUID(personID)]
		if p.Social == nil {
			p.Social = &map[string]any{}
		}
		(*p.Social)[platform] = handle
	}
	return socialRows.Err()
}
