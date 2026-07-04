//go:build integration

package compose

// The MCP comms + intent surface rides the same store paths as HTTP:
// drafting proposes over the anchor's context, availability answers
// slots, an unconsented send refuses at the gate, and catch_me_up_on
// returns the evidence-stamped assembled picture.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

func TestCommsAdapterSharesTheGovernedPaths(t *testing.T) {
	e := setupAuthz(t)
	adapter := commsAdapter{
		store: activities.NewStore(e.pool),
		gate:  consent.NewGate(consent.NewStore(e.pool)),
	}
	ctx := e.as(e.rep1, []ids.UUID{e.team1}, schedulerPerms)

	anchorID := ids.NewV7()
	err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'email', 'Pricing question', now(), 'manual', 'human:x')`, anchorID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	subject, body, err := adapter.DraftEmail(ctx, anchorID, "confirm the discount")
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Re: Pricing question" || body == "" {
		t.Fatalf("draft = %q / %q", subject, body)
	}

	// The consent default is deny: sending through the MCP seam refuses
	// exactly like the HTTP transport would.
	_, err = adapter.SendEmail(ctx, anchorID, agents.SendEmailArgs{
		To: []string{"nobody@example.test"}, Subject: "s", Body: "b",
		ConsentPurpose: "marketing_email",
	})
	if !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("unconsented MCP send → %v, want ErrConsentNotGranted", err)
	}

	from := time.Date(2026, 7, 7, 8, 0, 0, 0, time.UTC)
	raw, err := adapter.Availability(ctx, nil, from, from.Add(10*time.Hour), 60)
	if err != nil {
		t.Fatal(err)
	}
	var avail struct {
		Slots []struct {
			Start time.Time `json:"start"`
		} `json:"slots"`
	}
	if err := json.Unmarshal(raw, &avail); err != nil || len(avail.Slots) == 0 {
		t.Fatalf("availability over the seam: %v (%s)", err, raw)
	}

	booked, err := adapter.BookMeeting(ctx, agents.BookMeetingArgs{
		Start: avail.Slots[0].Start, End: avail.Slots[0].Start.Add(time.Hour), Subject: "Demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	var meeting struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(booked, &meeting); err != nil || meeting.Kind != "meeting" {
		t.Fatalf("booking over the seam: %v (%s)", err, booked)
	}
}

func TestIntentToolsReturnTheAssembledPicture(t *testing.T) {
	e := setupAuthz(t)
	target := e.seedPerson(t, "Briefing Target", &e.rep1)
	retriever := search.NewRetriever(search.NewStore(e.pool), nil)
	ctx := e.as(e.rep1, []ids.UUID{e.team1}, schedulerPerms)

	assembled, err := retriever.AssembleContext(ctx,
		datasource.EntityRef{Type: datasource.EntityPerson, ID: target},
		retrieval.AssembleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := assembledJSONForTest(assembled)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Anchor   map[string]any `json:"anchor"`
		Sections []struct {
			Name  string `json:"name"`
			Items []struct {
				Summary  string           `json:"summary"`
				Evidence []map[string]any `json:"evidence"`
			} `json:"items"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sections) == 0 || out.Sections[0].Name != "profile" {
		t.Fatalf("assembled picture lacks the profile section: %s", raw)
	}
	for _, section := range out.Sections {
		for _, item := range section.Items {
			if len(item.Evidence) == 0 {
				t.Fatalf("item %q carries no evidence — the no-guess gate needs it", item.Summary)
			}
		}
	}
}

// assembledJSONForTest reaches the agents module's wire rendering; the
// alias keeps the test honest to the exact shape the tool returns.
func assembledJSONForTest(assembled retrieval.Context) (json.RawMessage, error) {
	return agents.AssembledContextJSON(assembled)
}
