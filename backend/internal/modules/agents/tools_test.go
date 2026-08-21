// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import "testing"

// Every spec's InputSchema is rendered VERBATIM into the tool listing that
// rides every Surface-B prompt, so the indentation these literals are written
// with is paid for on every run — ~210 tokens across the core surface, out of
// a listing already held to two-thirds of the window.
//
// Compacting at the door rather than unindenting forty literals is what keeps
// both true at once: a schema stays readable where it is written and costs
// nothing where it is read. Without this, the next tool to be added has to buy
// its own room by tightening somebody else's copy.
func TestASchemaCostsNothingForBeingReadable(t *testing.T) {
	got := string(schema(`{"type":"object","properties":{
		"q":{"type":"string"}},
		"additionalProperties":false}`))
	want := `{"type":"object","properties":{"q":{"type":"string"}},"additionalProperties":false}`
	if got != want {
		t.Errorf("schema() rendered %s, want it compacted to %s", got, want)
	}
}

// A literal that is not JSON is left exactly as written. Silently rewriting it
// would hide the mistake from whichever test reads the schema next, which is
// the one place it would otherwise be caught.
func TestAMalformedSchemaIsLeftForTheTestThatReadsIt(t *testing.T) {
	broken := `{"type":"object",`
	if got := string(schema(broken)); got != broken {
		t.Errorf("schema() rewrote a malformed literal to %s, want it untouched", got)
	}
}
