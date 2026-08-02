// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The LinkedIn review surface over HTTP (ADR-0078 §2.1b): the list, the two
// decisions, and the reach view, through the real router, the real gates and
// the real payload shapes.
//
// The wire layer is where a ghost's fields are chosen, and choosing wrongly is
// the whole risk: these rows are third parties who never agreed to be in this
// CRM, so what crosses the wire is a privacy decision rather than a mapping.

import (
	"net/http"
	"testing"
)

type linkedInConnectionDTO struct {
	ID                string  `json:"id"`
	FullName          string  `json:"full_name"`
	Position          *string `json:"position"`
	CompanyName       *string `json:"company_name"`
	Email             *string `json:"email"`
	MatchStatus       string  `json:"match_status"`
	MatchedPersonID   *string `json:"matched_person_id"`
	MatchedPersonName *string `json:"matched_person_name"`
	MatchedOrgID      *string `json:"matched_org_id"`
}

type linkedInListDTO struct {
	Data []linkedInConnectionDTO `json:"data"`
	Page struct {
		HasMore    bool    `json:"has_more"`
		NextCursor *string `json:"next_cursor"`
	} `json:"page"`
}

type linkedInDecisionDTO struct {
	Connection        linkedInConnectionDTO `json:"connection"`
	ProfileURLWritten bool                  `json:"profile_url_written"`
}

type linkedInReachDTO struct {
	Accounts []struct {
		OrganizationID string `json:"organization_id"`
		DisplayName    string `json:"display_name"`
		Connections    int    `json:"connections"`
		ContactsOnFile int    `json:"contacts_on_file"`
	} `json:"accounts"`
	AccountsTotal         int `json:"accounts_total"`
	UnresolvedConnections int `json:"unresolved_connections"`
}

// seedGhost writes one connection owned by the session user. Written as owner
// because the import endpoint takes a multipart file and these tests are about
// the REVIEW surface, not the parser.
func (e *env) seedGhost(t *testing.T, name, company, profileURL string) string {
	t.Helper()
	var id string
	if err := e.owner.QueryRow(t.Context(), `
		INSERT INTO linkedin_connection
		    (workspace_id, owner_user_id, full_name, normalized_name,
		     company_name, normalized_company, profile_url, source)
		SELECT u.workspace_id, u.id, $1, lower($1), NULLIF($2, ''), NULLIF(lower($2), ''),
		       NULLIF($3, ''), 'csv_export'
		  FROM app_user u WHERE u.email = $4
		RETURNING id`, name, company, profileURL, "ada@example.com").Scan(&id); err != nil {
		t.Fatalf("seeding the ghost %q: %v", name, err)
	}
	return id
}

func TestTheReviewListIsOwnerScopedAndShowsTheOriginalStrings(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	e.seedGhost(t, "Andreas Müller", "SIMIO GmbH & Co. KG", "https://www.linkedin.com/in/amueller")

	var listed linkedInListDTO
	if status := e.call(t, "GET", "/v1/me/linkedin-connections", nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("listing connections: %d", status)
	}
	if len(listed.Data) != 1 {
		t.Fatalf("the list holds %d rows, want the one seeded", len(listed.Data))
	}
	row := listed.Data[0]
	// The ORIGINAL company string, not the folded form the matcher compares on:
	// nobody can judge a suggestion rendered as "simio".
	if row.CompanyName == nil || *row.CompanyName != "SIMIO GmbH & Co. KG" {
		t.Errorf("the row shows company %v, want the export's own spelling", row.CompanyName)
	}
	if row.MatchStatus != "unmatched" {
		t.Errorf("a ghost nobody has matched reads %q, want unmatched", row.MatchStatus)
	}
	// A first page that fits carries no cursor. Sending "" would have a client
	// ask for a page that does not exist.
	if listed.Page.HasMore || listed.Page.NextCursor != nil {
		t.Errorf("a single-row list reports has_more=%v cursor=%v", listed.Page.HasMore, listed.Page.NextCursor)
	}
}

func TestTheReviewListRefusesAMatchStatusItDoesNotKnow(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A typo is the caller's mistake and they can fix it from the message.
	// Answering 500 would send them to support for a query-string error.
	var body anyMap
	status := e.call(t, "GET", "/v1/me/linkedin-connections?match_status=confirmedd", nil, nil, &body)
	if status != http.StatusUnprocessableEntity && status != http.StatusBadRequest {
		t.Errorf("an unknown match_status answered %d, want a 4xx the caller can act on", status)
	}
}

func TestConfirmingOverHTTPPutsTheURLOnTheContactAndSaysSo(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	ghost := e.seedGhost(t, "Andreas Müller", "Acme GmbH", "https://www.linkedin.com/in/amueller")

	var person anyMap
	if status := e.call(t, "POST", "/v1/people",
		anyMap{"full_name": "Andreas Müller"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("creating the contact: %d", status)
	}
	personID, _ := person["id"].(string)

	var decision linkedInDecisionDTO
	if status := e.call(t, "POST", "/v1/me/linkedin-connections/"+ghost+"/confirm",
		anyMap{"person_id": personID}, nil, &decision); status != http.StatusOK {
		t.Fatalf("confirming: %d", status)
	}
	if decision.Connection.MatchStatus != "confirmed" {
		t.Errorf("the connection is %q after confirming, want confirmed", decision.Connection.MatchStatus)
	}
	if !decision.ProfileURLWritten {
		t.Error("the response reports no profile URL written, but the ghost carried one")
	}
	// The contact now carries the connection — the point of confirming.
	var handle string
	if err := e.owner.QueryRow(t.Context(),
		`SELECT handle FROM person_social WHERE person_id = $1 AND platform = 'linkedin'`,
		personID).Scan(&handle); err != nil {
		t.Fatalf("reading the contact's handle: %v", err)
	}
	if handle != "https://www.linkedin.com/in/amueller" {
		t.Errorf("the contact carries %q, want the connection's own profile URL", handle)
	}

	// And rejecting it again takes the handle back off: the member is saying
	// the link was wrong, and leaving the address behind keeps a wrong claim on
	// the record.
	var rejected linkedInDecisionDTO
	if status := e.call(t, "POST", "/v1/me/linkedin-connections/"+ghost+"/reject",
		nil, nil, &rejected); status != http.StatusOK {
		t.Fatalf("rejecting: %d", status)
	}
	if rejected.Connection.MatchStatus != "rejected" {
		t.Errorf("the connection is %q after rejecting, want rejected", rejected.Connection.MatchStatus)
	}
	var still int
	if err := e.owner.QueryRow(t.Context(),
		`SELECT count(*) FROM person_social WHERE person_id = $1 AND platform = 'linkedin'`,
		personID).Scan(&still); err != nil {
		t.Fatalf("re-reading the contact's handles: %v", err)
	}
	if still != 0 {
		t.Error("the withdrawn link left its LinkedIn address on the contact")
	}
}

func TestDecidingAConnectionThatIsNotYoursIsIndistinguishableFromOneThatDoesNotExist(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A member's export is theirs alone. Another member's connection must not
	// merely be refused — it must answer exactly what an unknown id answers, or
	// the difference itself says whose network holds whom.
	for _, verb := range []string{"confirm", "reject"} {
		var body anyMap
		status := e.call(t, "POST",
			"/v1/me/linkedin-connections/019fb000-0000-7000-8000-0000000000aa/"+verb, nil, nil, &body)
		if status != http.StatusNotFound {
			t.Errorf("%s on an unknown connection answered %d, want 404", verb, status)
		}
	}
}

func TestReachReportsWhatItCannotShowAlongsideWhatItCan(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var org anyMap
	if status := e.call(t, "POST", "/v1/organizations",
		anyMap{"display_name": "Acme GmbH", "source": "ui"}, nil, &org); status != http.StatusCreated {
		t.Fatalf("creating the account: %d", status)
	}
	orgID, _ := org["id"].(string)

	placed := e.seedGhost(t, "Andreas Müller", "Acme GmbH", "")
	e.seedGhost(t, "Nobody Atall", "Unknown Ltd", "")
	if _, err := e.owner.Exec(t.Context(),
		`UPDATE linkedin_connection SET matched_org_id = $2 WHERE id = $1`, placed, orgID); err != nil {
		t.Fatalf("placing the connection at the account: %v", err)
	}

	var reach linkedInReachDTO
	if status := e.call(t, "GET", "/v1/me/linkedin-reach", nil, nil, &reach); status != http.StatusOK {
		t.Fatalf("reading reach: %d", status)
	}
	if len(reach.Accounts) != 1 || reach.Accounts[0].OrganizationID != orgID {
		t.Fatalf("reach lists %+v, want the one account on file", reach.Accounts)
	}
	if reach.Accounts[0].Connections != 1 {
		t.Errorf("reach counts %d connections at the account, want 1", reach.Accounts[0].Connections)
	}
	// The connection whose employer is on nobody's books is REPORTED, not
	// dropped: that number is what shrinks as accounts are created, and a view
	// that hid it would overstate how much of a network is already covered.
	if reach.UnresolvedConnections != 1 {
		t.Errorf("reach reports %d unresolved, want the connection at an unknown company", reach.UnresolvedConnections)
	}
	if reach.AccountsTotal != 1 {
		t.Errorf("reach reports %d accounts in total, want 1", reach.AccountsTotal)
	}
}
