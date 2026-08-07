// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
)

// WriteJSON answers a stub provider's HTTP request with a JSON body. Both this
// package and the capture suite package stand up fake provider endpoints —
// delivery here, Gmail and Calendar there.
//
//craft:ignore naked-any the parameter is handed straight to json.Encoder.Encode, whose own signature is any; a stub answers whatever shape the endpoint it fakes returns, and there is no narrower type that admits all of them
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	//craft:ignore swallowed-errors test stub write; an encode failure surfaces as the client-side decode error the assertion checks
	_ = json.NewEncoder(w).Encode(v)
}
