// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The trust ladder reads WHO said it, off captured_by.
//
// It used to read activity.source, which stopped meaning "a human typed this"
// the moment agent tool calls began writing source=manual like every other
// first-party write. This test is the pin: it is written against captured_by
// values, so keying the ladder back onto a channel word fails it.
func TestTheTrustLadderReadsTheWriterNotTheChannel(t *testing.T) {
	human := "human:" + ids.NewV7().String()
	agent := "agent:" + ids.NewV7().String()

	for _, c := range []struct {
		name       string
		capturedBy string
		want       float64
	}{
		{"a person said it", human, trustHumanStatement},
		{"an agent wrote it", agent, trustAgentWrite},
		{"a connector brought it in", "connector:gmail", trustCapturedExternal},
		{"nobody is named", "", trustCapturedExternal},
		{"the old channel word, which names no writer", "manual", trustCapturedExternal},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := trustOfWriter(c.capturedBy); got != c.want {
				t.Errorf("trustOfWriter(%q) = %v, want %v", c.capturedBy, got, c.want)
			}
		})
	}
}

// Two notes of the same age rank by who wrote them: the human statement first.
//
// This is the property case 6 rests on — asking what happened with a client
// must surface what a person recorded ahead of what an agent inferred.
func TestAHumanNoteOutranksAnAgentNoteOfTheSameAge(t *testing.T) {
	now := time.Now()
	when := now.Add(-72 * time.Hour)
	humanScore := rankScore(0, when, "human:"+ids.NewV7().String(), now)
	agentScore := rankScore(0, when, "agent:"+ids.NewV7().String(), now)
	if humanScore <= agentScore {
		t.Fatalf("the human note scored %v and the agent note %v; the human one must rank higher",
			humanScore, agentScore)
	}
}

// Recency still outweighs trust, which is the §10.7.2 weighting and not an
// accident: a captured email from this morning is more use than a human note
// from last quarter, and the ladder is a tie-break between comparable ages
// rather than an override.
func TestFreshCapturedContentStillBeatsAStaleHumanNote(t *testing.T) {
	now := time.Now()
	fresh := rankScore(0, now, "connector:gmail", now)
	stale := rankScore(0, now.Add(-365*24*time.Hour), "human:"+ids.NewV7().String(), now)
	if fresh <= stale {
		t.Fatalf("fresh captured content scored %v and a year-old human note %v; recency must still win",
			fresh, stale)
	}
}
