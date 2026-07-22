// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import "testing"

func TestNameValidate(t *testing.T) {
	for _, valid := range []Name{"de", "crm-hello", "a2-b3-c4"} {
		if err := valid.Validate(); err != nil {
			t.Errorf("Name(%q).Validate() = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []Name{"", "Bad_Name", "-foo", "foo-", "foo--bar", "über", "a b"} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("Name(%q).Validate() = nil, want the grammar rejection", invalid)
		}
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
