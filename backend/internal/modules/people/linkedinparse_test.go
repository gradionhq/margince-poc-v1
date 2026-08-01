// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Parsing the LinkedIn export. This is the code most likely to meet a file
// nobody anticipated: LinkedIn has reordered and localized the format more
// than once, users edit it in spreadsheets, and the preamble changes.
//
// The failure mode that matters is not a crash — it is a parser that reads the
// wrong column and imports plausible garbage without complaining.

import (
	"strings"
	"testing"
)

func parse(t *testing.T, csv string) linkedInParse {
	t.Helper()
	got, err := parseLinkedInCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	return got
}

func TestThePreambleIsNotMistakenForAHeader(t *testing.T) {
	// LinkedIn puts a Notes block and blank lines above the real header. A
	// parser that took line 1 would read the note as column names and then
	// find no fields it recognizes.
	got := parse(t, `Notes:
"When exporting your connection data, you may notice that some of the email addresses are missing."

First Name,Last Name,URL,Email Address,Company,Position,Connected On
Dana,Buyer,https://x,dana@acme.test,Acme GmbH,CTO,15 Mar 2024
`)
	if len(got.parsed) != 1 {
		t.Fatalf("parsed %d rows, want 1 — the preamble was read as data or as the header", len(got.parsed))
	}
	if got.parsed[0].email != "dana@acme.test" || got.parsed[0].company != "Acme GmbH" {
		t.Errorf("columns landed wrong: %+v", got.parsed[0])
	}
}

func TestTheGermanExportParses(t *testing.T) {
	// The same file from a German account. Reading the header by CONTENT is
	// what makes this work; a positional parser would silently mis-assign.
	got := parse(t, `Vorname,Nachname,E-Mail-Adresse,Unternehmen,Position,Verbunden am
Andreas,Müller,andreas@acme.test,Acme GmbH,Leiter IT,02 Feb 2023
`)
	if len(got.parsed) != 1 {
		t.Fatalf("parsed %d rows from the German export, want 1", len(got.parsed))
	}
	row := got.parsed[0]
	if row.fullName != "Andreas Müller" || row.email != "andreas@acme.test" {
		t.Errorf("German columns landed wrong: %+v", row)
	}
	// The accent folds for matching, and ß would fold to ss by the same path.
	if row.normalized != "andreas muller" {
		t.Errorf("normalized name = %q, want the accent-folded form", row.normalized)
	}
	// The company key strips the legal suffix so it reaches an account stored
	// as "Acme".
	if row.normCompany != "acme" {
		t.Errorf("normalized company = %q, want the suffix stripped", row.normCompany)
	}
}

func TestARowWithNoNameIsCountedNotDropped(t *testing.T) {
	// A file half-ignored under a success message is worse than a refusal.
	got := parse(t, `First Name,Last Name,Email Address,Company,Position,Connected On
Dana,Buyer,dana@acme.test,Acme,CTO,15 Mar 2024
,,nobody@x.test,Ghost Ltd,Founder,01 Jan 2020
`)
	if len(got.parsed) != 1 {
		t.Errorf("parsed %d rows, want 1", len(got.parsed))
	}
	if got.skipped != 1 {
		t.Errorf("skipped count = %d, want 1 — an unusable row must be reported, not silently lost", got.skipped)
	}
}

func TestRaggedRowsAndMissingTrailingFieldsSurvive(t *testing.T) {
	// Real exports omit trailing empties and quote inconsistently. Rejecting
	// the file over that would refuse an import that is otherwise readable.
	got := parse(t, `First Name,Last Name,Email Address,Company,Position,Connected On
Dana,Buyer,dana@acme.test,Acme
Pat,Counter
`)
	if len(got.parsed) != 2 {
		t.Fatalf("parsed %d rows, want 2 — a short row is still a connection", len(got.parsed))
	}
	if got.parsed[1].fullName != "Pat Counter" {
		t.Errorf("the short row lost its name: %+v", got.parsed[1])
	}
}

func TestTheConnectedDateToleratesEveryShippedFormat(t *testing.T) {
	// LinkedIn has used several. An unparseable date must not lose the
	// connection — it only weakens the fallback dedupe key.
	for _, when := range []string{"15 Mar 2024", "2 Feb 2023", "2024-03-15", "03/15/2024"} {
		got := parse(t, "First Name,Last Name,Connected On\nDana,Buyer,"+when+"\n")
		if len(got.parsed) != 1 {
			t.Fatalf("date %q lost the row", when)
		}
		if got.parsed[0].connectedOn == nil {
			t.Errorf("date %q did not parse", when)
		}
	}
	// And an unreadable one keeps the connection.
	got := parse(t, "First Name,Last Name,Connected On\nDana,Buyer,sometime last spring\n")
	if len(got.parsed) != 1 || got.parsed[0].connectedOn != nil {
		t.Errorf("an unreadable date lost the connection or invented one: %+v", got.parsed)
	}
}

func TestAFileThatIsNotAnExportIsRefusedWithAReason(t *testing.T) {
	// Picking the wrong file is a user mistake a sentence can fix; answering
	// "internal error" sends them to support instead.
	_, err := parseLinkedInCSV(strings.NewReader("id,amount\n1,20\n"))
	var format *LinkedInFormatError
	if !asFormatError(err, &format) {
		t.Fatalf("a non-export parsed or failed as something else: %v", err)
	}
	if format.Reason == "" {
		t.Error("the refusal carries no reason the user could act on")
	}
}

// asFormatError is errors.As, kept local so the test reads as one assertion.
func asFormatError(err error, target **LinkedInFormatError) bool {
	if e, ok := err.(*LinkedInFormatError); ok {
		*target = e
		return true
	}
	return false
}
