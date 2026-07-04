// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestFakeClientScriptedAndDeterministic(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeClient().Script(`{"answer":"scripted"}`)

	req := model.Request{Model: "cheap", Messages: []model.Message{{Role: "user", Content: "summarize"}}}
	first, err := fake.Complete(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != `{"answer":"scripted"}` {
		t.Fatalf("scripted response not returned: %q", first.Text)
	}

	// Queue exhausted: the fallback is a pure function of the payload.
	second, err := fake.Complete(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	third, err := fake.Complete(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != third.Text {
		t.Fatalf("fallback not deterministic: %q vs %q", second.Text, third.Text)
	}
	if second.InputTokens == 0 || second.OutputTokens == 0 {
		t.Fatalf("token accounting missing: %+v", second)
	}
}

func TestFakeClientRunsStripperAndRecordsPayload(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeClient()
	_, err := fake.Complete(ctx, model.Request{
		Model:          "cheap",
		Messages:       []model.Message{{Role: "user", Content: "the key is sk-ant-api03-FAKEFAKEFAKEfakefakefake0000"}},
		SecretStripper: NewSecretStripper(),
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected one recorded call, got %d", len(calls))
	}
	if strings.Contains(string(calls[0].Payload), "sk-ant-api03-FAKE") {
		t.Fatalf("secret reached the recorded outbound payload: %s", calls[0].Payload)
	}
	if calls[0].Report.Findings == 0 {
		t.Fatal("strip report empty though a secret was present")
	}
	if !json.Valid(calls[0].Payload) {
		t.Fatalf("recorded payload is not valid JSON: %s", calls[0].Payload)
	}
}

func TestFakeClientEmbedDeterministicUnitVectors(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeClient()
	res, err := fake.Embed(ctx, model.EmbedRequest{Inputs: []string{"alpha", "alpha", "beta"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dims != fakeEmbedDims || len(res.Vectors) != 3 {
		t.Fatalf("unexpected shape: dims=%d n=%d", res.Dims, len(res.Vectors))
	}
	if !equalVectors(res.Vectors[0], res.Vectors[1]) {
		t.Fatal("same text produced different vectors")
	}
	if equalVectors(res.Vectors[0], res.Vectors[2]) {
		t.Fatal("different texts produced the same vector")
	}
	var norm float64
	for _, v := range res.Vectors[0] {
		norm += float64(v) * float64(v)
	}
	if math.Abs(norm-1) > 1e-3 {
		t.Fatalf("vector not unit length: %f", norm)
	}
}

func TestFakeClientStreamReassemblesText(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeClient().Script("a scripted stream of more than sixteen bytes")
	stream, err := fake.Stream(ctx, model.Request{Model: "cheap"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	var got strings.Builder
	for {
		chunk, ok, err := stream.Next(ctx)
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got.WriteString(chunk)
	}
	if got.String() != "a scripted stream of more than sixteen bytes" {
		t.Fatalf("stream reassembly mismatch: %q", got.String())
	}
}

func equalVectors(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
