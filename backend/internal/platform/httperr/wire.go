package httperr

// Shared transport mechanics for module handlers: request decode, JSON
// response writing, and the If-Match optimistic-concurrency header —
// wire concerns every module transport spells identically.

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Decode parses the request body, answering the validation problem shape
// on malformed JSON. Returns false when the response has been written.
func Decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		Write(w, r, Validation("body", "malformed_json", err.Error()))
		return false
	}
	return true
}

// WriteJSON writes a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// IfMatchVersion reads the optional If-Match row version (data-model
// §1.3a: a bare integer, not a quoted ETag). Malformed input is a client
// error, not last-write-wins.
func IfMatchVersion(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		return nil, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		Write(w, r, Validation("If-Match", "malformed_if_match", "If-Match carries the last-seen integer version"))
		return nil, false
	}
	return &v, true
}
