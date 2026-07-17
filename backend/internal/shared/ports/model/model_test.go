// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package model

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRequestCarriesAdditiveProviderFields(t *testing.T) {
	req := Request{
		ProviderOptions: map[string]json.RawMessage{"openai": json.RawMessage(`{"reasoning_effort":"low"}`)},
		Attachments:     []Attachment{{MIME: "application/pdf", Bytes: []byte("x"), Name: "contract.pdf"}},
	}
	if len(req.ProviderOptions) != 1 || len(req.Attachments) != 1 {
		t.Fatalf("additive request fields not carried: %+v", req)
	}
	if req.Attachments[0].MIME != "application/pdf" {
		t.Fatalf("attachment MIME lost: %+v", req.Attachments[0])
	}
}

func TestResponseCarriesFlatTokenCountersAndMetadata(t *testing.T) {
	resp := Response{
		InputTokens: 10, OutputTokens: 5, CachedTokens: 3, ReasoningTokens: 7,
		ProviderMetadata: map[string]json.RawMessage{"openai": json.RawMessage(`{"response_id":"r1"}`)},
	}
	if resp.CachedTokens != 3 || resp.ReasoningTokens != 7 || len(resp.ProviderMetadata) != 1 {
		t.Fatalf("additive response fields not carried: %+v", resp)
	}
}

func TestErrAttachmentUnsupportedIsADistinctCapabilityError(t *testing.T) {
	if errors.Is(ErrAttachmentUnsupported, ErrEmbeddingsUnsupported) {
		t.Fatal("ErrAttachmentUnsupported must be distinct from ErrEmbeddingsUnsupported")
	}
	wrapped := errors.New("ai: openai_compatible: " + ErrAttachmentUnsupported.Error())
	_ = wrapped
	if ErrAttachmentUnsupported == nil {
		t.Fatal("ErrAttachmentUnsupported must be defined")
	}
}
