// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// Pack codes in this file use ISO user-assigned codes (z*) unique per
// test: the jurisdiction registry is process-global, so a code
// registered here stays registered for the test binary's lifetime.
type fakePack struct{ code jurisdiction.Code }

func (p fakePack) Code() jurisdiction.Code         { return p.code }
func (fakePack) Retention() jurisdiction.Retention { return nil }

func TestRegisterExtensionsAppliesDeclaredCapabilities(t *testing.T) {
	err := RegisterExtensions([]extension.Extension{{
		Name:          "reg-ok",
		Version:       "0.0.1",
		Jurisdictions: []jurisdiction.Pack{fakePack{code: "zx"}},
	}})
	if err != nil {
		t.Fatalf("RegisterExtensions: %v", err)
	}
	if _, ok := jurisdiction.For("zx"); !ok {
		t.Fatal("the declared pack did not reach the jurisdiction registry")
	}
}

func TestRegisterExtensionsRejectsAnInvalidUnitName(t *testing.T) {
	err := RegisterExtensions([]extension.Extension{{Name: "Bad_Name"}})
	if err == nil || !strings.Contains(err.Error(), "not a valid unit name") {
		t.Fatalf("err = %v, want the unit-name rejection", err)
	}
}

func TestRegisterExtensionsRejectsADuplicateUnit(t *testing.T) {
	err := RegisterExtensions([]extension.Extension{
		{Name: "twice", Version: "0.0.1"},
		{Name: "twice", Version: "0.0.2"},
	})
	if err == nil || !strings.Contains(err.Error(), "composed twice") {
		t.Fatalf("err = %v, want the duplicate-unit rejection", err)
	}
}

func TestRegisterExtensionsRejectsAMissingVersion(t *testing.T) {
	err := RegisterExtensions([]extension.Extension{{Name: "unversioned"}})
	if err == nil || !strings.Contains(err.Error(), "version is empty") {
		t.Fatalf("err = %v, want the missing-version rejection", err)
	}
}

func TestRegisterExtensionsPreflightsDuplicateJurisdictions(t *testing.T) {
	err := RegisterExtensions([]extension.Extension{
		{Name: "first", Version: "0.0.1", Jurisdictions: []jurisdiction.Pack{fakePack{code: "zv"}}},
		{Name: "second", Version: "0.0.1", Jurisdictions: []jurisdiction.Pack{fakePack{code: "zv"}}},
	})
	if err == nil || !strings.Contains(err.Error(), `both declare jurisdiction "zv"`) {
		t.Fatalf("err = %v, want the duplicate-jurisdiction rejection", err)
	}
	if _, ok := jurisdiction.For("zv"); ok {
		t.Fatal("a duplicate-declared pack landed although the set was rejected")
	}
}

// TestNoCapabilityAppliesWhenTheSetIsInvalid: validation and application
// are separate phases — a clean unit's capabilities must not land when a
// later unit fails validation, or a crash-looping process would leave
// half-registered state behind.
func TestNoCapabilityAppliesWhenTheSetIsInvalid(t *testing.T) {
	err := RegisterExtensions([]extension.Extension{
		{Name: "clean", Version: "0.0.1", Jurisdictions: []jurisdiction.Pack{fakePack{code: "zy"}}},
		{Name: "Invalid Name"},
	})
	if err == nil {
		t.Fatal("RegisterExtensions succeeded, want the invalid unit to abort it")
	}
	if _, ok := jurisdiction.For("zy"); ok {
		t.Fatal("the clean unit's pack landed although the composed set failed validation")
	}
}
