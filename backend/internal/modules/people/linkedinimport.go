// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Importing a LinkedIn Connections.csv as GHOSTS (CG-DDL-2 / ADR-0078 §2.1b).
//
// Every LinkedIn member can export their own connections without any API
// approval, which is why this path exists first: it works today, and the
// Member Data Portability API becomes a second writer onto the same rows once
// LinkedIn approves a developer app.
//
// The imported rows are NOT people. They never reach search, lists, the people
// screens, or the assistant's record tools, and nothing can write to them.
// That is the safety property the whole feature rests on: an export is a list
// of third parties who never agreed to be in anyone's CRM, and turning three
// thousand of them into contacts would be a consent problem and a data-quality
// catastrophe at the same time. They exist to answer one question — does
// anyone here already know someone at this company.

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// LinkedInImportResult reports what one import did, in the terms a human
// asked to trust it would check: how many rows the file held, how many are now
// stored, and how many could not be read. Skipped rows are counted rather than
// silently dropped — an import that quietly ignored half a file while
// reporting success is worse than one that fails.
type LinkedInImportResult struct {
	Rows     int
	Imported int
	Skipped  int
}

// The header fields this importer reads, named so the alias table and the
// row reader cannot disagree about a key by a typo.
const (
	csvFirst     = "first"
	csvLast      = "last"
	csvEmail     = "email"
	csvCompany   = "company"
	csvPosition  = "position"
	csvConnected = "connected"
	csvURL       = "url"
)

// linkedInHeaderAliases maps the column names LinkedIn has shipped over the
// years onto the fields this importer needs. The export format has changed
// more than once and differs by locale, so the header is READ rather than
// assumed positional — a positional parser silently imports the wrong column
// the first time LinkedIn reorders anything.
var linkedInHeaderAliases = map[string]string{
	"first name":     csvFirst,
	"vorname":        csvFirst,
	"last name":      csvLast,
	"nachname":       csvLast,
	"email address":  csvEmail,
	"e-mail-adresse": csvEmail,
	"company":        csvCompany,
	"unternehmen":    csvCompany,
	"position":       csvPosition,
	"connected on":   csvConnected,
	"verbunden am":   csvConnected,
	// The connection's own profile address. It is the ONE field a confirmed
	// match copies onto a contact, so an export whose header this table does
	// not recognize leaves every confirmation unable to write anything.
	"url":        csvURL,
	"profil-url": csvURL,
}

// ImportLinkedInConnections reads a Connections.csv for ONE user and upserts
// the ghosts it describes.
//
// The owner is the authenticated caller, never a field in the file: a LinkedIn
// network is personal, and letting a request name whose network it is would
// let anyone attribute a stranger's connections to a colleague.
func (s *Store) ImportLinkedInConnections(ctx context.Context, r io.Reader) (LinkedInImportResult, error) {
	if err := auth.Require(ctx, "person", principal.ActionCreate); err != nil {
		return LinkedInImportResult{}, err
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID == ids.Nil {
		return LinkedInImportResult{}, fmt.Errorf("people: a LinkedIn import belongs to a person: %w", apperrors.ErrPermissionDenied)
	}
	rows, err := parseLinkedInCSV(r)
	if err != nil {
		return LinkedInImportResult{}, err
	}
	out := LinkedInImportResult{Rows: len(rows.parsed) + rows.skipped, Skipped: rows.skipped}

	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Repair the stored keys BEFORE upserting. normalized_company is a
		// derived part of the natural key, so rows written under an older
		// normalizer no longer collide with what this import computes — and
		// inserting against a stale key is exactly how a re-import duplicates
		// a whole network. Doing it here rather than trusting the hourly sweep
		// makes the import self-healing: it cannot depend on a background pass
		// having happened to run first.
		if _, err := renormalizeGhostKeysTx(ctx, tx, actor.UserID); err != nil {
			return err
		}
		for _, row := range rows.parsed {
			// Counted only when a row was actually stored. An erased subject on
			// the suppression list is refused, and reporting them as imported
			// would tell a member their file landed data this system
			// deliberately destroyed.
			stored, err := upsertGhost(ctx, tx, actor.UserID, row)
			if err != nil {
				return err
			}
			if stored {
				out.Imported++
			}
		}
		// The write shape, once for the ACT and not once per row. An export is
		// thousands of connections; a per-row audit would bury every other
		// entry in the log and a per-row event would do the same to the stream.
		// The auditable fact is that this member imported their network, and
		// how much of the file was usable.
		auditID, err := storekit.Audit(ctx, tx, "import", "user", actor.UserID, nil, map[string]any{
			"rows": out.Rows, "imported": out.Imported, "skipped": out.Skipped,
		})
		if err != nil {
			return err
		}
		// No connection is NAMED in either the audit row or the event: the
		// imported rows are third parties who never consented to being in this
		// CRM, and recording their names here would defeat the invisibility
		// that is the whole safety property of a ghost.
		return storekit.EmitEvent(ctx, tx, auditID, actor.UserID,
			crmcontracts.PublicEventLinkedinNetworkImported{
				Rows: out.Rows, Imported: out.Imported, Skipped: out.Skipped,
			})
	})
	if err != nil {
		return LinkedInImportResult{}, err
	}
	return out, nil
}

// LinkedInFormatError is a file this importer cannot read at all, as opposed
// to a file with some unusable rows — those are counted and skipped. It is
// typed so the transport can answer 422 rather than 500: a wrong file is a
// user mistake, and telling them "internal error" sends them to support for
// something they can fix themselves.
type LinkedInFormatError struct{ Reason string }

func (e *LinkedInFormatError) Error() string {
	return "people: unreadable LinkedIn export: " + e.Reason
}

// linkedInRow is one connection, normalized.
type linkedInRow struct {
	fullName    string
	normalized  string
	position    string
	company     string
	normCompany string
	email       string
	profileURL  string
	connectedOn *time.Time
}

type linkedInParse struct {
	parsed  []linkedInRow
	skipped int
}

// parseLinkedInCSV reads the export, tolerating the preamble LinkedIn puts
// above the header ("Notes:" and a blank line or three) by scanning for the
// first row that looks like a header rather than assuming line 1.
func parseLinkedInCSV(r io.Reader) (linkedInParse, error) {
	reader := csv.NewReader(r)
	// The export is ragged: the notes preamble has fewer fields than the data,
	// and some rows omit trailing empties. Fixed-field checking would reject
	// a file that is otherwise perfectly readable.
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	var out linkedInParse
	var index map[string]int
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, fmt.Errorf("people: reading the LinkedIn export: %w", err)
		}
		if index == nil {
			if found := headerIndex(record); found != nil {
				index = found
			}
			continue
		}
		row, ok := linkedInRowFrom(record, index)
		if !ok {
			// A row with no usable name identifies nobody. Counted, not
			// silently dropped.
			out.skipped++
			continue
		}
		out.parsed = append(out.parsed, row)
	}
	if index == nil {
		return out, &LinkedInFormatError{Reason: "no recognizable LinkedIn header row — export the file from LinkedIn without editing it"}
	}
	return out, nil
}

// headerIndex recognizes the header by its CONTENT, and only accepts one that
// carries a name column — the export's preamble lines would otherwise pass as
// a header made of one stray cell.
func headerIndex(record []string) map[string]int {
	index := map[string]int{}
	for i, cell := range record {
		if field, known := linkedInHeaderAliases[strings.ToLower(strings.TrimSpace(cell))]; known {
			index[field] = i
		}
	}
	if _, named := index[csvFirst]; !named {
		if _, named := index[csvLast]; !named {
			return nil
		}
	}
	return index
}

// linkedInRowFrom reads one data row through the header index.
func linkedInRowFrom(record []string, index map[string]int) (linkedInRow, bool) {
	at := func(field string) string {
		i, ok := index[field]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	name := strings.TrimSpace(at(csvFirst) + " " + at(csvLast))
	if name == "" {
		return linkedInRow{}, false
	}
	row := linkedInRow{
		fullName:   name,
		normalized: normalizeName(name),
		position:   at(csvPosition),
		company:    at(csvCompany),
		// NormalizeOrgName, not normalizeName: it strips the legal suffix,
		// so a connection at "Acme GmbH" matches the account stored as "Acme".
		// Through the LinkedIn cleaner first, because the field is a free-text
		// headline on LinkedIn and not a company name.
		normCompany: NormalizeOrgName(cleanLinkedInCompany(at(csvCompany))),
		email:       normalizeEmail(at(csvEmail)),
		profileURL:  at(csvURL),
	}
	// LinkedIn has shipped at least three date formats across locales and
	// years. An unparseable date is not a reason to lose the connection — it
	// only weakens the fallback dedupe key.
	for _, layout := range []string{"02 Jan 2006", "2 Jan 2006", "2006-01-02", "01/02/2006"} {
		if when, err := time.Parse(layout, at(csvConnected)); err == nil {
			row.connectedOn = &when
			break
		}
	}
	return row, true
}

// upsertGhost writes one connection, idempotently. Re-importing a refreshed
// export must update rows rather than duplicate them — people re-export
// regularly, and a second import that doubled everyone's network would make
// the reach counts meaningless.
// upsertGhost writes one connection and reports whether a row was actually
// stored. The bool matters: a subject on the erasure suppression list is
// skipped silently, and counting that as "imported" tells a member their file
// landed rows it deliberately refused.
func upsertGhost(ctx context.Context, tx pgx.Tx, owner ids.UUID, row linkedInRow) (bool, error) {
	// An erased subject must not walk back in through a colleague's next
	// export. Erasure hashes the addresses it destroyed onto the suppression
	// list precisely so re-ingestion cannot resurrect them, and an import that
	// did not consult it would undo an Art. 17 request with a file upload.
	//
	// The check is on the ADDRESS, which is the only identifier a ghost shares
	// with the suppression list; a name-only row carries nothing to match and
	// is imported, which is the same limit every address-keyed suppression in
	// this codebase has.
	if row.email != "" {
		var suppressed bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM erasure_suppression
			                WHERE kind = 'email' AND value_hash = $1)`,
			storekit.SuppressionHash(row.email)).Scan(&suppressed); err != nil {
			return false, fmt.Errorf("people: checking the erasure suppression list: %w", err)
		}
		if suppressed {
			return false, nil
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO linkedin_connection
		    (workspace_id, owner_user_id, full_name, normalized_name, position,
		     company_name, normalized_company, connected_on, email, profile_url,
		     source, synced_at)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7, NULLIF($8, ''),
	        NULLIF($9, ''), 'csv_export', now())
		ON CONFLICT (workspace_id, owner_user_id, normalized_name,
		             coalesce(normalized_company, ''), coalesce(connected_on, 'epoch'::date))
		  WHERE provider_member_ref IS NULL
		DO UPDATE SET
		    full_name  = EXCLUDED.full_name,
		    position   = EXCLUDED.position,
		    company_name = EXCLUDED.company_name,
		    email      = coalesce(EXCLUDED.email, linkedin_connection.email),
		    -- A later export wins on the URL: a member who changed their
		    -- vanity address is reachable at the new one, and the old is dead.
		    profile_url = coalesce(EXCLUDED.profile_url, linkedin_connection.profile_url),
		    synced_at  = now(),
		    updated_at = now(),
		    -- A re-import revives a connection an earlier export had dropped.
		    tombstoned_at = NULL`,
		owner, row.fullName, row.normalized, row.position,
		row.company, row.normCompany, row.connectedOn, row.email, row.profileURL)
	if err != nil {
		return false, fmt.Errorf("people: storing a LinkedIn connection: %w", err)
	}
	return true, nil
}

// cleanLinkedInCompany strips what LinkedIn's company field carries that a
// company name does not.
//
// The field is edited by the member, not chosen from a register, so it arrives
// as a marketing headline: ".NFQ | Digital Creatives", "tagtu | Result-Driven
// Business Travel". Everything after the first separator is a tagline, and a
// leading dot or bullet is styling. Handled HERE rather than inside
// NormalizeOrgName because this is LinkedIn's junk, not a property of company
// names — the account dedupe must not start splitting customer names on
// punctuation because one importer needed it to.
func cleanLinkedInCompany(s string) string {
	for _, sep := range []string{"|", "•", "·", "—", "–"} {
		if cut, _, found := strings.Cut(s, sep); found {
			s = cut
		}
	}
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ".-·•*"))
	return strings.TrimSuffix(s, ".")
}

// webTLDs are the domain endings a company writes into its own name —
// "Wortfilter.de" is the account stored as "Wortfilter". Only stripped when the
// name is a single token, so "Booking.com Partners" keeps its shape and a firm
// genuinely called "X.Y Consulting" is untouched.
var webTLDs = []string{".de", ".com", ".io", ".ai", ".net", ".org", ".co", ".eu", ".at", ".ch"}

// orgMatchKeys are the keys ONE company string may be looked up under, in
// descending order of how much it claims.
//
// The exact key always comes first and is the only one the account dedupe uses.
// The rest are LinkedIn-side fallbacks for the ways a member writes their
// employer that a register never would, each one narrow enough to be checked
// against a real example that failed:
//
//	"Wortfilter.de"                 → wortfilter      (the account's own name)
//	"The Sentry"                    → thesentry       (spacing)
//	"pinops consumer research"      → pinops          (a description trailing the name)
//	"SIMIO GmbH & Co. KG"           → simio           (already exact after the strip)
//
// A fallback is a LOOKUP key, never a claim: the caller only accepts one when
// it resolves to exactly one account, so "NFQ Technologies" still reaches
// nothing while two accounts are called Nfq.
func orgMatchKeys(company string) []string {
	exact := NormalizeOrgName(cleanLinkedInCompany(company))
	if exact == "" {
		return nil
	}
	keys := []string{exact}
	seen := map[string]bool{exact: true}
	add := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	fields := strings.Fields(exact)
	if len(fields) == 1 {
		for _, tld := range webTLDs {
			if trimmed := strings.TrimSuffix(exact, tld); trimmed != exact {
				add(trimmed)
				break
			}
		}
	}
	// Spacing is not identity: "The Sentry" and "Thesentry" are one account.
	add(strings.ReplaceAll(exact, " ", ""))
	// A leading name followed by what the company DOES. Two tokens minimum on
	// the survivor would be safer still, but one is what the real misses need
	// ("pinops consumer research", "brickfox Multichannel eCommerce") and the
	// unambiguity requirement is what keeps it honest.
	if len(fields) > 1 {
		add(fields[0])
		add(strings.Join(fields[:len(fields)-1], " "))
	}
	return keys
}
