// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build voicelive

package compose

// Manual live smoke (never CI): drives DeriveVoice + evaluation through the
// REAL routing config to prove the bound provider returns valid inference.
// Run: MARGINCE_AI_ROUTING=../../config/ai-routing.yaml GEMINI_API_KEY=... \
//   go test -tags voicelive ./internal/compose/ -run TestVoiceLiveSmoke -v -count=1

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestVoiceLiveSmoke(t *testing.T) {
	routing := os.Getenv("MARGINCE_AI_ROUTING")
	if routing == "" {
		t.Fatal("set MARGINCE_AI_ROUTING to the routing yaml (this smoke is manual-only)")
	}
	cfg, err := ai.LoadRoutingFile(routing)
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewLocalModelPath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	samples := []ai.VoiceSample{}
	filler := []string{
		"We ship on Monday, no excuses. The demo was rough, the feedback was blunt, and that is exactly why it was useful.",
		"I would rather see an honest partial result than a polished slide. Send me the numbers as they are.",
		"Der Punkt ist einfach: wenn der Kunde zweimal nachfragt, war die Antwort nicht klar genug. Kürzer schreiben.",
		"Stop optimizing the deck. The pilot decides this deal, and the pilot needs two engineers for a week.",
	}
	for i := 0; i < 12; i++ {
		text := filler[i%len(filler)] + " " + strings.Repeat("Wir bauen das Produkt so, dass ein Verkäufer es ohne Handbuch bedienen kann. ", 8)
		register := "email"
		if i%3 == 1 {
			register = "spoken"
		}
		samples = append(samples, ai.VoiceSample{
			ID: fmt.Sprintf("s-%d", i), Kind: "email", Register: register,
			Text: text, WordCount: ai.WordCount(text),
		})
	}
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	heldOut, buildSamples := splitVoiceHeldOut(samples, "live-smoke")
	artifact, err := ai.DeriveVoice(ctx, path.VoiceBuild, "Blunt, bilingual, allergic to filler.", "live-smoke", buildSamples)
	if err != nil {
		t.Fatalf("DeriveVoice against the live provider: %v", err)
	}
	t.Logf("model=%s thinking=%q moves=%d obsessions=%v",
		artifact.ModelName, artifact.Inference.ThinkingPattern, len(artifact.Inference.SignatureMoves), artifact.Inference.ObservedObsessions)
	result, err := evaluateVoiceCandidate(ctx, path.VoiceBuild, artifact, "Blunt, bilingual.", heldOut, nil)
	if err != nil {
		t.Fatalf("evaluation against the live provider: %v", err)
	}
	t.Logf("action=%s classification=%s eval=%v reasons=%v",
		result.Action, result.Classification, result.Evaluation["candidate_median_voice_score"], result.ReviewReasons)
	if artifact.Inference.ThinkingPattern == "" {
		t.Fatal("live provider produced an empty thinking pattern")
	}
}
