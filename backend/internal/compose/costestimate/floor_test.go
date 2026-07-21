// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// The floor is the cold-start estimate most first-connect previews land on, so
// its per-unit shape must be deterministic and non-zero for every priced task —
// a zero floor would silently under-quote the primary scenario.
func TestWorkShapeFloorIsDeterministicAndNonZeroPerTask(t *testing.T) {
	for _, task := range backfillTasks {
		first := workShapeFloor(task)
		second := workShapeFloor(task)
		if first != second {
			t.Fatalf("workShapeFloor(%s) not deterministic: %+v vs %+v", task, first, second)
		}
		if first.TokensIn <= 0 {
			t.Fatalf("workShapeFloor(%s).TokensIn = %d, want > 0 (an input-anchored floor)", task, first.TokensIn)
		}
	}

	// Embeddings are input-only — no output tokens, no cache — matching the
	// embed lane's actual call shape.
	if out := workShapeFloor(ai.TaskEmbeddings).TokensOut; out != 0 {
		t.Fatalf("embeddings floor TokensOut = %d, want 0 (input-only lane)", out)
	}

	// An unknown task carries no floor rather than a fabricated one.
	if got := workShapeFloor(ai.TaskSummarize); got != (ai.Usage{}) {
		t.Fatalf("workShapeFloor(summarize) = %+v, want the zero Usage (not a backfill task)", got)
	}
}

func TestUnitsFloorTracksTheYieldlessRatios(t *testing.T) {
	const scanned = 100

	// classify fires ≈ once per scanned message at connect.
	if got := unitsFloor(ai.TaskCaptureClassify, scanned); got != scanned {
		t.Fatalf("unitsFloor(classify, %d) = %d, want %d (captured ≈ scanned)", scanned, got, scanned)
	}

	// embeddings cold-start floor counts MESSAGE-embeds only — captured ≈ scanned
	// at connect. Person/org embeds are omitted from the floor on purpose: the
	// floor prices every embed unit at a full email (embedItemTokens), so folding
	// in expected persons would charge each name-sized person embed as a full
	// email — a per-person overquote on the cheapest input-only lane. The observed
	// path still counts them via yields; only the floor omits them.
	if got := unitsFloor(ai.TaskEmbeddings, scanned); got != scanned {
		t.Fatalf("unitsFloor(embeddings, %d) = %d, want %d (message-embeds only; person embeds omitted from the floor)", scanned, got, scanned)
	}

	// enrich fires once per expected new correspondent — scanned × the one
	// honest density constant (referenced, never a magic number here).
	wantEnrich := int64(float64(scanned) * defaultPersonsPerMsg)
	if wantEnrich <= 0 {
		t.Fatalf("test fixture bug: expected a positive enrich floor at scanned=%d", scanned)
	}
	if got := unitsFloor(ai.TaskEnrich, scanned); got != wantEnrich {
		t.Fatalf("unitsFloor(enrich, %d) = %d, want %d (scanned × defaultPersonsPerMsg)", scanned, got, wantEnrich)
	}
}
