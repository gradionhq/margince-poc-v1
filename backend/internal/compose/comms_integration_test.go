// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

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
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

type integrationReplyBrain struct {
	response string
	err      error
}

func (b integrationReplyBrain) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: b.response}, b.err
}

// recordingStager stands in for the delivery machinery so these suites can
// assert what the GOVERNED decision did: a send only reaches staging once the
// gates have let it through, and what it hands over is what would be
// transmitted.
type recordingStager struct{ staged []activities.DeliveryRequest }

func (s *recordingStager) StageTx(_ context.Context, _ pgx.Tx, in activities.DeliveryRequest) error {
	s.staged = append(s.staged, in)
	return nil
}

// StageChannelTx makes this stub the whole DeliveryMachinery seam. It records
// nothing: these suites stage mail, and a channel delivery arriving here would be
// a shape-branch defect rather than a case to absorb quietly.
func (s *recordingStager) StageChannelTx(_ context.Context, _ pgx.Tx, in activities.ChannelDeliveryRequest) error {
	return fmt.Errorf("recordingStager: a mail suite staged a channel delivery to %s", in.Recipient.Provider)
}

// assertStaged pins how many deliveries reached the machinery — the only
// way to tell "the gate refused" from "the gate let it through and the send
// went nowhere".
func assertStaged(t *testing.T, stager *recordingStager, want int, when string) {
	t.Helper()
	if len(stager.staged) != want {
		t.Fatalf("%s staged %d deliveries, want %d", when, len(stager.staged), want)
	}
}

func TestCommsAdapterSharesTheGovernedPaths(t *testing.T) {
	e := integration.Setup(t)
	stager := &recordingStager{}
	adapter := commsAdapter{
		store:  activities.NewStore(e.Pool),
		gate:   consent.NewGate(consent.NewStore(e.Pool)),
		stager: stager,
	}
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.SchedulerPerms)

	anchorID := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
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
	assertModelAndFallbackDrafts(ctx, t, adapter, anchorID)

	// The consent default is deny: sending through the MCP seam refuses
	// exactly like the HTTP transport would.
	_, err = adapter.SendEmail(ctx, anchorID, agents.SendEmailArgs{
		To: []string{"nobody@example.test"}, Subject: "s", Body: "b",
		ConsentPurpose: "marketing_email",
	})
	if !errors.Is(err, apperrors.ErrConsentNotGranted) {
		t.Fatalf("unconsented MCP send → %v, want ErrConsentNotGranted", err)
	}
	assertStaged(t, stager, 0, "the suppressed MCP send")

	from := time.Date(2026, 7, 7, 8, 0, 0, 0, time.UTC)
	avail, err := adapter.Availability(ctx, nil, from, from.Add(10*time.Hour), 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(avail.Slots) == 0 {
		t.Fatalf("availability over the seam returned no slots: %+v", avail)
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

func assertModelAndFallbackDrafts(ctx context.Context, t *testing.T, adapter commsAdapter, anchorID ids.UUID) {
	t.Helper()
	adapter.draft = replyDrafter{
		brain: integrationReplyBrain{response: `{"subject":"Re: Your pricing question","body":"The discount is confirmed for review."}`},
		store: adapter.store,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	subject, body, err := adapter.DraftEmail(ctx, anchorID, "confirm the discount")
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Re: Your pricing question" || body != "The discount is confirmed for review." {
		t.Fatalf("model draft = %q / %q", subject, body)
	}

	adapter.draft = replyDrafter{
		brain: integrationReplyBrain{err: errors.New("provider unavailable")},
		store: adapter.store,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	subject, body, err = adapter.DraftEmail(ctx, anchorID, "confirm the discount")
	if err != nil {
		t.Fatalf("fallback draft: %v", err)
	}
	if subject != "Re: Pricing question" || !strings.Contains(body, "confirm the discount") {
		t.Fatalf("fallback draft = %q / %q", subject, body)
	}
}

func TestIntentToolsReturnTheAssembledPicture(t *testing.T) {
	e := integration.Setup(t)
	target := e.SeedPerson(t, "Briefing Target", &e.Rep1)
	retriever := search.NewRetriever(search.NewStore(e.Pool), nil)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.SchedulerPerms)

	assembled, err := retriever.AssembleContext(ctx,
		datasource.EntityRef{Type: datasource.EntityPerson, ID: target},
		retrieval.AssembleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := assembledJSONForTest(ctx, assembled)
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
func assembledJSONForTest(ctx context.Context, assembled retrieval.Context) (json.RawMessage, error) {
	return agents.AssembledContextJSON(ctx, assembled)
}
