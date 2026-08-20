// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"errors"
	"os"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The three conditions that decide whether a landed activity is read.
//
// The extraction lane was fully built and had never run once — transcript_read
// held zero rows — because the ONLY thing that enqueued it was somebody asking
// over REST, and nothing asked. A capability nobody can reach is not one.
func TestOnlyATranscriptWithABodyStartsAReading(t *testing.T) {
	body, empty := "00:00 Matthias: we agreed on a small first package", ""
	transcript, plaud := transcriptSourceSystem, "plaud"
	for name, tc := range map[string]struct {
		activity crmcontracts.Activity
		created  bool
		want     bool
	}{
		"a transcript with a body is read": {
			crmcontracts.Activity{SourceSystem: &transcript, Body: &body}, true, true,
		},
		// `plaud` is the honest name of where a recording came from, and it is
		// what a real session logged. It is not the marker, which is why the
		// record-fields notes now say which value is.
		"another source system is stored, not read": {
			crmcontracts.Activity{SourceSystem: &plaud, Body: &body}, true, false,
		},
		"a transcript with no body has nothing to read": {
			crmcontracts.Activity{SourceSystem: &transcript, Body: &empty}, true, false,
		},
		"no source system at all": {
			crmcontracts.Activity{Body: &body}, true, false,
		},
		// An idempotent replay must not queue a second reading of a transcript
		// already read. uq_transcript_read_inflight would refuse it anyway;
		// refusing here means no conflict error on a call that did nothing
		// wrong.
		"a replay of an existing activity is not re-read": {
			crmcontracts.Activity{SourceSystem: &transcript, Body: &body}, false, false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := startsAReading(tc.activity, tc.created); got != tc.want {
				t.Errorf("startsAReading = %v, want %v", got, tc.want)
			}
		})
	}
}

// A transcript too long to read still gets stored.
//
// The reading starts inside the activity's own transaction, so an error from
// it rolls the activity back. WithinReadingBounds refuses past 600 lines /
// 60,000 characters while an activity body may be 256 KiB — so without this,
// logging a long meeting would destroy the activity and report a complaint
// about a reading the caller never asked for.
func TestATranscriptTooLongToReadIsStillStored(t *testing.T) {
	if err := skipARefusedReading(&TranscriptTooLongError{Lines: 900, Chars: 90000}); err != nil {
		t.Fatalf("an over-long transcript failed the write with %v; it must only skip the reading", err)
	}
}

// Everything else still fails the write. A database fault means the activity
// is not safely written either, and swallowing it would report success over a
// row that is not there.
func TestAFailedReadingStillFailsTheWrite(t *testing.T) {
	boom := errors.New("connection reset")
	if err := skipARefusedReading(boom); !errors.Is(err, boom) {
		t.Fatalf("a database fault was swallowed: got %v, want %v", err, boom)
	}
}

// Every door that logs an activity also offers the reading.
//
// The first cut hooked LogActivity only, so POST /v1/activities and the
// extension core (which drive LogActivityTx) stored transcripts nothing ever
// read — the exact silence this feature exists to end, reintroduced on two of
// three doors. Both entry points route through logActivityAndReadTranscript
// now, and this test reads the source to say so: a future entry point that
// calls logActivityInTx directly is the regression, and it is invisible to any
// test that only exercises the doors that exist today.
func TestEveryActivityEntryPointOffersTheReading(t *testing.T) {
	src, err := os.ReadFile("activity.go")
	if err != nil {
		t.Fatalf("reading activity.go: %v", err)
	}
	body := string(src)
	// logActivityInTx is the write alone. Only its one wrapper may call it.
	callers := strings.Count(body, "logActivityInTx(ctx, tx, in)")
	if callers != 1 {
		t.Errorf("logActivityInTx has %d callers in activity.go, want exactly 1 "+
			"(logActivityAndReadTranscript). A door calling the write directly stores "+
			"transcripts nothing reads.", callers)
	}
	for _, door := range []string{"func (s *Store) LogActivity(", "func (s *Store) LogActivityTx("} {
		at := strings.Index(body, door)
		if at < 0 {
			t.Fatalf("%s is gone; this test needs updating with whatever replaced it", door)
		}
		if !strings.Contains(body[at:at+900], "logActivityAndReadTranscript") {
			t.Errorf("%s does not route through logActivityAndReadTranscript, "+
				"so a transcript arriving through it is never read", door)
		}
	}
}
