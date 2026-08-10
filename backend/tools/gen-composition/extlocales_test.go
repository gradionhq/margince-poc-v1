// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two units cannot claim one key, and the NAMESPACE rule is what makes that
// true — it refuses first, so the collision branch behind it is unreachable
// through any real tree. That is worth a test rather than a note, because the
// property a reader cares about is "no last-wins merge decides which copy
// renders", and it is the prefix rule that delivers it.
//
// The collision check stays in the code as the fail-closed second answer: it
// costs one map lookup, and it is what would still hold if the prefix rule were
// ever loosened. It is deliberately not asserted here, because a test can only
// reach it by constructing input the derivation cannot produce.
func TestOneKeyCannotBeClaimedByTwoUnits(t *testing.T) {
	_, err := mergeUnitLocales([]unitLocale{
		{Unit: "alpha", Locale: "en", Keys: map[string]string{"extAlpha.title": "A"}},
		{Unit: "beta", Locale: "en", Keys: map[string]string{"extAlpha.title": "B"}},
	})
	if err == nil || !strings.Contains(err.Error(), "outside its namespace") {
		t.Fatalf("err = %v, want beta refused for claiming alpha's namespace", err)
	}
}

// And the same unit supplying one key across locales is the ORDINARY case, not
// a collision — the claim is per (locale, key, unit).
func TestOneUnitMayClaimTheSameKeyInEveryLocale(t *testing.T) {
	merged, err := mergeUnitLocales([]unitLocale{
		{Unit: "demo", Locale: "en", Keys: map[string]string{"extDemo.title": "Notes"}},
		{Unit: "demo", Locale: "de", Keys: map[string]string{"extDemo.title": "Notizen"}},
		{Unit: "demo", Locale: "vi", Keys: map[string]string{"extDemo.title": "Ghi chú"}},
	})
	if err != nil {
		t.Fatalf("one unit translating its own key was refused: %v", err)
	}
	if len(merged) != 3 {
		t.Fatalf("merged %d locale(s), want 3: %#v", len(merged), merged)
	}
}

// A unit rewriting `nav.people` would change core copy from a directory, which
// is not a capability this tier grants.
func TestMergeUnitLocalesRefusesAKeyOutsideTheUnitNamespace(t *testing.T) {
	_, err := mergeUnitLocales([]unitLocale{
		{Unit: "crm-demo", Locale: "en", Keys: map[string]string{"nav.people": "Hijacked"}},
	})
	if err == nil || !strings.Contains(err.Error(), "outside its namespace") {
		t.Fatalf("err = %v, want the namespace refusal", err)
	}
}

func TestLocaleKeyPrefixIsDerivedFromTheUnitName(t *testing.T) {
	for unit, want := range map[string]string{
		"crm-demo": "extCrmDemo.",
		"de":       "extDe.",
		"yogi":     "extYogi.",
	} {
		if got := localeKeyPrefix(unit); got != want {
			t.Errorf("localeKeyPrefix(%q) = %q, want %q", unit, got, want)
		}
	}
}

func TestMergeUnitLocalesAcceptsNamespacedKeys(t *testing.T) {
	merged, err := mergeUnitLocales([]unitLocale{
		{Unit: "crm-demo", Locale: "en", Keys: map[string]string{"extCrmDemo.title": "Notes"}},
		{Unit: "crm-demo", Locale: "de", Keys: map[string]string{"extCrmDemo.title": "Notizen"}},
	})
	if err != nil {
		t.Fatalf("namespaced keys were refused: %v", err)
	}
	if merged["de"]["extCrmDemo.title"] != "Notizen" {
		t.Fatalf("merged = %#v", merged)
	}
}

// A locale the installation does not ship, and a unit that supplies one
// language and not another: both would render a blank screen for some reader,
// which is worse than a build that stops.
func TestCollectUnitLocalesRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"a locale this installation does not ship": {
			files: map[string]string{"fr.json": `{"extDemo.a":"a"}`},
			want:  "does not ship",
		},
		"a language supplied for some locales only": {
			files: map[string]string{"en.json": `{"extDemo.a":"a"}`},
			want:  "ships no de.json",
		},
		"a file that is not JSON": {
			files: map[string]string{
				"en.json": `{`, "de.json": `{}`, "vi.json": `{}`,
			},
			want: "flat object of string to string",
		},
		"a file that is not a locale": {
			files: map[string]string{"README.md": "notes"},
			want:  "is not a <locale>.json file",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			layer := filepath.Join(dir, frontendLayer, localesLayer)
			if err := os.MkdirAll(layer, 0o755); err != nil {
				t.Fatal(err)
			}
			for file, body := range tc.files {
				if err := os.WriteFile(filepath.Join(layer, file), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := collectUnitLocales("demo", dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// A unit with no copy of its own is the common case and composes nothing.
func TestCollectUnitLocalesAbsentIsNotAnError(t *testing.T) {
	got, err := collectUnitLocales("demo", t.TempDir())
	if err != nil || got != nil {
		t.Fatalf("got %#v, %v", got, err)
	}
}

// The empty tree reproduces the committed stub, like every other artifact.
func TestExtLocalesGenVanillaIsTheCommittedStub(t *testing.T) {
	stub, err := os.ReadFile(filepath.Join("..", "..", "..", frontendLocalesVanillaStub))
	if err != nil {
		t.Fatal(err)
	}
	if got := extLocalesGen(nil); !bytes.Equal(got, stub) {
		t.Fatalf("the vanilla copy overlay drifted from the committed stub:\n%s", got)
	}
}

// The list of locales a unit may supply is the catalogue's list. Two spellings
// of one fact drift, and the drift would be a unit shipping copy no locale
// switch can select — or being refused for a locale the product ships.
func TestComposedLocalesMatchTheCatalogue(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend/src/i18n/index.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	marker := "export const LOCALES = ["
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatal("frontend/src/i18n/index.tsx no longer declares LOCALES — this gate cannot read the catalogue")
	}
	declared := src[i+len(marker) : i+len(marker)+strings.Index(src[i+len(marker):], "]")]
	for _, locale := range composedLocales {
		if !strings.Contains(declared, `"`+locale+`"`) {
			t.Errorf("composedLocales has %q and the catalogue does not (%s)", locale, declared)
		}
	}
	if got, want := strings.Count(declared, `"`)/2, len(composedLocales); got != want {
		t.Errorf("the catalogue ships %d locale(s) and composedLocales lists %d — a unit would be refused a locale the product has, or allowed one it does not", got, want)
	}
}
