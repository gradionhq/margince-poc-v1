// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"strings"
	"testing"
)

func TestNameValidate(t *testing.T) {
	for _, valid := range []Name{"de", "crm-hello", "a2-b3-c4"} {
		if err := valid.Validate(); err != nil {
			t.Errorf("Name(%q).Validate() = %v, want nil", valid, err)
		}
	}
	if long := Name(strings.Repeat("a", 33)); long.Validate() == nil {
		t.Error("a 33-character name passed validation — SQL identifiers derived from it would risk 63-byte truncation collisions")
	}
	if atCap := Name(strings.Repeat("a", 32)); atCap.Validate() != nil {
		t.Error("a 32-character name must pass — that is the documented cap")
	}
	for _, invalid := range []Name{"", "Bad_Name", "-foo", "foo-", "foo--bar", "über", "a b"} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("Name(%q).Validate() = nil, want the grammar rejection", invalid)
		}
	}
}

func TestOverlongNameErrorStatesTheCurrentBudget(t *testing.T) {
	// The error an author actually reads must name the real budget. It is
	// derived from maxNameLength and the ext_ prefix, so assert the string
	// rather than the arithmetic — arithmetic against its own constants can
	// never fail, and would pin nothing.
	err := Name(strings.Repeat("a", maxNameLength+1)).Validate()
	if err == nil {
		t.Fatal("an over-long name validated")
	}
	if !strings.Contains(err.Error(), "ext_") {
		t.Errorf("error does not mention the ext_ prefix: %v", err)
	}
	if !strings.Contains(err.Error(), "26") {
		t.Errorf("error does not state the 26-byte table-suffix budget: %v", err)
	}
}

func TestVersionValidate(t *testing.T) {
	for _, valid := range []Version{"0.1.0", "1.0.0-rc.1", "2026-07-22"} {
		if err := valid.Validate(); err != nil {
			t.Errorf("Version(%q).Validate() = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []Version{"", " 0.1.0", "0.1.0 ", "0.1\n0"} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("Version(%q).Validate() = nil, want the rejection", invalid)
		}
	}
}
