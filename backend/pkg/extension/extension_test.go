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

func TestNameNamespace(t *testing.T) {
	for _, tc := range []struct {
		name Name
		want string
	}{
		{name: "foo-1", want: "ext_foo_1"},
		{name: "de", want: "ext_de"},
		{name: "a2-b3-c4", want: "ext_a2_b3_c4"},
	} {
		got, err := tc.name.Namespace()
		if err != nil || got != tc.want {
			t.Errorf("Name(%q).Namespace() = %q, %v — want %q", tc.name, got, err, tc.want)
		}
	}
	// The derived namespace is an unquoted SQL identifier; every shape one
	// cannot hold is already outside the name grammar, so Namespace refuses
	// exactly what Validate refuses and nothing more.
	for _, invalid := range []Name{"", "Bad_Name", "-foo", "foo.bar", `foo"bar`, Name(strings.Repeat("a", 33))} {
		if got, err := invalid.Namespace(); err == nil {
			t.Errorf("Name(%q).Namespace() = %q, want the grammar rejection", invalid, got)
		}
	}
}

func TestNamespaceLeavesTheDocumentedSuffixBudget(t *testing.T) {
	// ext_ + a 32-character name + the joining underscore is 37 bytes, which
	// is what leaves 26 for the table suffix inside PostgreSQL's 63.
	ns, err := Name(strings.Repeat("a", maxNameLength)).Namespace()
	if err != nil {
		t.Fatal(err)
	}
	if got := 63 - len(ns) - len("_"); got != 26 {
		t.Fatalf("the longest namespace leaves %d bytes for a table suffix, want the documented 26", got)
	}
}
