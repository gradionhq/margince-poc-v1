package datasource

import (
	"reflect"
	"testing"
)

// The v1 provider seam is frozen (interfaces.md §3, ADR-0017 §1): fork
// adapters implement it, so ANY change to the method set — adding,
// removing, or renaming — hard-breaks them on merge. A new verb goes on
// SystemOfRecordProviderV2 behind a capability probe. This test is the
// Go-interface-diff gate: it fails the build the moment the set drifts.
func TestSystemOfRecordProviderV1MethodSetIsFrozen(t *testing.T) {
	frozen := []string{
		"AdvanceDeal",
		"Archive",
		"Create",
		"Freshness",
		"ListFields",
		"ListObjects",
		"Merge",
		"PromoteLead",
		"Read",
		"RunReport",
		"Search",
		"StageSemantic",
		"Update",
	}

	typ := reflect.TypeOf((*SystemOfRecordProvider)(nil)).Elem()
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name) // reflect lists methods sorted
	}
	if !reflect.DeepEqual(got, frozen) {
		t.Fatalf("SystemOfRecordProvider method set drifted.\n got: %v\nwant: %v\nThe v1 seam is frozen — add post-v1 verbs on SystemOfRecordProviderV2 with a capability probe, never here.", got, frozen)
	}
}
