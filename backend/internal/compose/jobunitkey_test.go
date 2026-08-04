// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The census arm that holds a fan-out child to carrying the args key its
// dispatcher's declared unit names. The real wiring satisfies it, which is what
// TestJobCensusMatchesTheContract asserts — these drive the FINDINGS, against
// hand-built registrations, because an arm whose failure path never runs is
// indistinguishable from one that cannot fail.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// aFanOutChild answers one declared fan-out child kind whose unit is finer than
// a workspace, so the fixtures below bind a kind the contract really fans out
// per connection rather than one invented here.
func aFanOutChild(t *testing.T) string {
	t.Helper()
	for kind, unit := range jobs.FanOutUnits() {
		if unit == jobs.FanOutConnection {
			return kind
		}
	}
	t.Fatal("no kind is declared as fanned out per connection; this gate has nothing to check")
	return ""
}

// keylessArgs is a child that names its unit in a Go field the JSON encoder
// never writes under the key the metric groups on. It is the shape a
// name-level check passes and the query does not: the column would hold
// "connectionID", and args->>'connection_id' answers NULL for every row.
type keylessArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
	//nolint:tagliatelle // the wrong casing IS the fixture: this type exists to be a child whose column key is not the one the gauge groups on.
	ConnectionID ids.UUID `json:"connectionID"`
}

func (keylessArgs) Kind() string { return "keyless_probe" }

// selfEncodingArgs decides its own encoding, so its fields say nothing about
// what lands in the column.
type selfEncodingArgs struct {
	ConnectionID ids.UUID
}

func (selfEncodingArgs) Kind() string { return "self_encoding_probe" }

func (a selfEncodingArgs) MarshalJSON() ([]byte, error) {
	return json.Marshal(a.ConnectionID.String())
}

// censusWith builds a census whose whole registry is one kind, so a finding can
// only have come from that kind.
func censusWith(kind string, args river.JobArgs) *JobCensus {
	return &JobCensus{wired: map[string]wiredWorker{kind: {args: args}}}
}

func TestTheUnitKeyArmRefusesAChildThatDoesNotWriteTheKeyTheGaugeGroupsOn(t *testing.T) {
	kind := aFanOutChild(t)
	findings := censusWith(kind, keylessArgs{}).everyFanOutChildCarriesItsUnitKey()

	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(findings), findings)
	}
	if !strings.Contains(findings[0], "connection_id") {
		t.Errorf("the finding does not name the missing key, so a reader cannot act on it: %s", findings[0])
	}
	if !strings.Contains(findings[0], kind) {
		t.Errorf("the finding does not name the kind: %s", findings[0])
	}
}

// A type that encodes ITSELF is refused rather than read past: its fields
// govern nothing, so a walk that shrugged would report "no key missing" about
// a column it never looked at.
func TestTheUnitKeyArmRefusesAChildThatEncodesItself(t *testing.T) {
	kind := aFanOutChild(t)
	findings := censusWith(kind, selfEncodingArgs{}).everyFanOutChildCarriesItsUnitKey()

	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(findings), findings)
	}
	if !strings.Contains(findings[0], "JSON object") {
		t.Errorf("the finding does not say what is wrong with the encoding: %s", findings[0])
	}
}

// A kind the census never registered is the other arm's finding, not this
// one's — reporting it twice sends a reader chasing two symptoms of one cause.
func TestTheUnitKeyArmLeavesAnUnregisteredKindToTheWiringCheck(t *testing.T) {
	if findings := (&JobCensus{wired: map[string]wiredWorker{}}).everyFanOutChildCarriesItsUnitKey(); len(findings) != 0 {
		t.Errorf("an empty registry produced %d finding(s); every declared child is unwired there, which "+
			"everyDeclaredKindIsWiredAndBack already reports: %v", len(findings), findings)
	}
}

// argsJSONKeys is what the arm reads the column's shape from, and it answers
// from the ENCODER rather than from field names. A registration that recorded
// no args value at all has nothing to encode, and must say so rather than
// answer an empty key set that reads as "carries nothing".
func TestArgsJSONKeysRefusesARegistrationWithNoArgsValue(t *testing.T) {
	keys, err := argsJSONKeys(nil)
	if err == nil {
		t.Fatalf("a nil args value answered %v rather than refusing; an empty key set reads as a type that carries nothing", keys)
	}
}

func TestArgsJSONKeysAnswersTheEncodersOwnKeys(t *testing.T) {
	keys, err := argsJSONKeys(keylessArgs{})
	if err != nil {
		t.Fatalf("argsJSONKeys: %v", err)
	}
	// The Go field is ConnectionID; the column key is what the tag says. The
	// difference between the two is the whole reason this reads the encoder.
	want := []string{"connectionID", "workspace_id"}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Errorf("keys = %v, want %v — sorted, and taken from the tags rather than the field names", keys, want)
	}
}
