// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Store-level Voice DNA gates (B-E07.4): the platform row-scope clause
// over voice profiles — a voice profile and corpus are their owner's personal
// writing, so every other user reads absence from the normal profile surface —
// and the derived-rebuild write path: SetDerivedProfile
// persists the §B0.2 artifact from an ingested fixture corpus WITH a
// bumped profile_version while the human-authored personality_md
// survives untouched (the split that makes rebuilds safe).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// voiceRepPerms mirrors the seeded rep grant for the voice_profile
// object (posture: create/read/update, no delete,
// team scope).
var voiceRepPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"voice_profile": {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}

func TestPersonalVoiceProfileIsOwnerPrivate(t *testing.T) {
	e := Setup(t)
	voice := ai.NewVoiceStore(e.Pool)

	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, voiceRepPerms)
	created, err := voice.CreateProfile(owner, ai.CreateVoiceProfileInput{})
	if err != nil {
		t.Fatal(err)
	}

	// Team scope never widens personal voice content: teammate and outsider
	// both read absence, and neither sees it in the list.
	teammate := e.As(e.Rep2, []ids.UUID{e.Team1}, voiceRepPerms)
	if _, err := voice.GetProfile(teammate, created.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("teammate read → %v, want ErrNotFound", err)
	}
	outsider := e.As(e.Rep3, []ids.UUID{e.Team2}, voiceRepPerms)
	if _, err := voice.GetProfile(outsider, created.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("outsider read → %v, want ErrNotFound", err)
	}
	page, err := voice.ListProfiles(outsider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("outsider lists %d profiles, want 0", len(page.Items))
	}
	page, err = voice.ListProfiles(teammate, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("teammate lists %d profiles, want 0", len(page.Items))
	}

	// The corpus rides the same gate: an outsider cannot ingest into or
	// enumerate a foreign profile.
	if _, _, err := voice.ListSources(outsider, created.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("outsider manifest → %v, want ErrNotFound", err)
	}
	if _, _, err := voice.IngestSource(outsider, created.ID, ai.IngestSourceInput{
		Kind: "post", SourceLabel: "poison", Content: "not my voice",
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("outsider ingest → %v, want ErrNotFound", err)
	}

	// A personal profile's CONTENT is owner-only: a teammate — and even
	// an unbounded admin — may see it exists but can neither write in the
	// owner's voice nor browse their corpus manifest (403, not absence).
	for who, ctx := range map[string]context.Context{"teammate": teammate, "admin": e.Admin()} {
		if _, _, err := voice.IngestSource(ctx, created.ID, ai.IngestSourceInput{
			Kind: "post", SourceLabel: "poison", Content: "words in another's mouth",
		}); !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Fatalf("%s ingest into a personal profile → %v, want ErrPermissionDenied", who, err)
		}
		if _, err := voice.UpdateProfile(ctx, created.ID, "not their identity", nil, nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Fatalf("%s personality edit → %v, want ErrPermissionDenied", who, err)
		}
		if _, _, err := voice.ListSources(ctx, created.ID); !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Fatalf("%s manifest browse → %v, want ErrPermissionDenied", who, err)
		}
	}
	// The owner's own path is untouched.
	if _, _, err := voice.IngestSource(owner, created.ID, ai.IngestSourceInput{
		Kind: "post", SourceLabel: "mine", Content: "my own words, my own voice",
	}); err != nil {
		t.Fatalf("owner ingest: %v", err)
	}
}

// The fixture corpus the rebuild regression loads (the B-E07.4
// reusable-artifact DoD): one written post + one both-sided spoken
// transcript whose other party must contribute nothing.
const voiceFixturePost = `Most CRMs make reps type. Ours reads the meeting and drafts the follow-up; the rep just decides.`

const voiceFixtureVTT = `WEBVTT

1
00:00:01.000 --> 00:00:05.000
<v Ada Admin>I always open with the customer's number, not ours.</v>

2
00:00:05.500 --> 00:00:08.000
<v Klaus Kunde>That is unusual but it works on our board.</v>
`

// voiceFixtureArtifact stands in for the ported builder's output: the
// §B0.2 fixed-schema markdown the rebuild persists. The builder itself
// is B-E07.6; the schema contract is pinned here.
const voiceFixtureArtifact = `# Voice Profile — Ada Admin

## Identity
Direct operator; outcome-first.

## Stats snapshot
avg sentence 11 words · contractions high

## Signature moves
Opens with the customer's number.

## Structural patterns
Short paragraphs; one ask per message.

## Punctuation / emoji / POV
No emoji; first person singular.

## Vocabulary
ship, land, decide

## Anti-patterns (forbidden)
No "circling back", no exclamation marks.

## Register notes
Spoken register is looser than posts.

## Few-shot examples
### email
Kurz: das Angebot steht, Freitag entscheiden wir.
`

func TestVoiceDerivedRebuildVersionsArtifactAndPreservesIdentity(t *testing.T) {
	e := Setup(t)
	voice := ai.NewVoiceStore(e.Pool)
	owner := e.As(e.Rep1, []ids.UUID{e.Team1}, voiceRepPerms)

	created, err := voice.CreateProfile(owner, ai.CreateVoiceProfileInput{
		PersonalityMD: "Hand-written: dry humor stays.",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The fixture corpus ingests deterministically: registers tagged per
	// kind, the transcript speaker-filtered to the owner.
	post, _, err := voice.IngestSource(owner, created.ID, ai.IngestSourceInput{
		Kind: "post", SourceLabel: "post", SourceRef: "fixture-post", Content: voiceFixturePost,
	})
	if err != nil {
		t.Fatal(err)
	}
	call, summary, err := voice.IngestSource(owner, created.ID, ai.IngestSourceInput{
		Kind: "transcript", SourceLabel: "call", SourceRef: "fixture-call",
		Format: "vtt", SpeakerLabel: "Ada Admin", Content: voiceFixtureVTT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if post.Register != "written" || call.Register != "spoken" {
		t.Fatalf("registers = %s/%s, want written/spoken", post.Register, call.Register)
	}
	if call.WordCount != 9 {
		t.Fatalf("transcript word_count = %d, want the owner's 9 words only", call.WordCount)
	}
	if summary.TotalWords != post.WordCount+call.WordCount {
		t.Fatalf("meter %d ≠ %d + %d", summary.TotalWords, post.WordCount, call.WordCount)
	}

	// An empty rebuild is refused — a versioned nothing helps no one.
	if _, err := voice.SetDerivedProfile(owner, created.ID, "  ", nil); err == nil {
		t.Fatal("empty derived artifact accepted")
	}

	// First rebuild: artifact persisted, versioned, status ready — and
	// the human-authored half is byte-identical.
	modelRef := "style:v1"
	built, err := voice.SetDerivedProfile(owner, created.ID, voiceFixtureArtifact, &modelRef)
	if err != nil {
		t.Fatal(err)
	}
	if built.ProfileVersion != 1 || built.Status != "ready" {
		t.Fatalf("first rebuild → version %d status %s, want 1/ready", built.ProfileVersion, built.Status)
	}
	if !strings.Contains(built.VoiceProfileMD, "Anti-patterns (forbidden)") {
		t.Fatal("persisted artifact lost the §B0.2 anti-patterns section")
	}
	if built.PersonalityMD != "Hand-written: dry humor stays." {
		t.Fatalf("rebuild touched personality_md: %q", built.PersonalityMD)
	}

	// A second rebuild bumps the artifact version again.
	rebuilt, err := voice.SetDerivedProfile(owner, created.ID, voiceFixtureArtifact+"\nrev2\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.ProfileVersion != 2 || rebuilt.PersonalityMD != built.PersonalityMD {
		t.Fatalf("second rebuild → version %d personality %q", rebuilt.ProfileVersion, rebuilt.PersonalityMD)
	}
}
