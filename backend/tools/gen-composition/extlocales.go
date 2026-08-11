// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// A unit's copy, merged into the one catalogue the SPA looks up.
//
// Copy left in core would keep removing a unit a two-place operation while
// looking like it had fixed one — the screen would travel with the unit and its
// strings would not. Merging rather than giving each unit its own lookup keeps
// the properties the core catalogue already has: one place a translator works,
// one missing-key gate, one `useT`.
//
// JSON, not TypeScript. The generator would otherwise have to parse TS to read
// a unit's strings, and a declaration this tier reads is inert data everywhere
// else — the contract fragments are YAML, the manifest is JSON, and this is the
// same posture rather than a new one.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// localesLayer is the directory inside a unit's frontend package holding its
// copy, one file per locale.
const localesLayer = "i18n"

// composedLocales are the locales the core catalogue ships, and the only ones a
// unit may supply. It is written out rather than discovered from the unit tree
// on purpose: a unit inventing `fr.json` would silently ship copy no locale
// switch can ever select, and the honest answer to that is a refusal rather
// than a file nobody reads.
//
// Keep in step with frontend/src/i18n/index.tsx's LOCALES. The two lists are
// held together by TestComposedLocalesMatchTheCatalogue.
var composedLocales = []string{"en", "de", "vi"}

// unitLocale is one unit's copy for one locale.
type unitLocale struct {
	Unit   string
	Locale string
	Keys   map[string]string
}

// collectUnitLocales reads a unit's i18n layer. Absent is fine: a unit whose
// screen needs no copy of its own, or which ships no screen at all, supplies
// none.
//
// A locale the core does not ship is refused rather than ignored, and so is a
// locale the unit shipped for one language and not another — a screen that
// renders in English and blanks in German is worse than one that does not build.
func collectUnitLocales(name, dir string) ([]unitLocale, error) {
	layer := filepath.Join(dir, frontendLayer, localesLayer)
	entries, err := os.ReadDir(layer)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	supplied := map[string]bool{}
	var out []unitLocale
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			return nil, fmt.Errorf("extensions/%s: %s/%s/%s is not a <locale>.json file — a unit's copy is one flat JSON object per locale",
				name, frontendLayer, localesLayer, e.Name())
		}
		locale := strings.TrimSuffix(e.Name(), ".json")
		if !contains(composedLocales, locale) {
			return nil, fmt.Errorf("extensions/%s: %s/%s/%s names locale %q, which this installation does not ship (%s) — its copy could never be selected",
				name, frontendLayer, localesLayer, e.Name(), locale, strings.Join(composedLocales, ", "))
		}
		raw, err := os.ReadFile(filepath.Join(layer, e.Name())) // #nosec G304 -- the unit tree this composer was pointed at
		if err != nil {
			return nil, err
		}
		var keys map[string]string
		if err := json.Unmarshal(raw, &keys); err != nil {
			return nil, fmt.Errorf("extensions/%s: %s/%s/%s: %w — a locale file is a flat object of string to string",
				name, frontendLayer, localesLayer, e.Name(), err)
		}
		supplied[locale] = true
		out = append(out, unitLocale{Unit: name, Locale: locale, Keys: keys})
	}
	for _, locale := range composedLocales {
		if len(out) > 0 && !supplied[locale] {
			return nil, fmt.Errorf("extensions/%s: %s/%s ships no %s.json — a unit supplies every locale the installation does, or a reader of one language gets a blank screen",
				name, frontendLayer, localesLayer, locale)
		}
	}
	return out, nil
}

// localeKeyPrefix is the token a unit's every key must begin with, derived from
// its own name: crm-demo → extCrmDemo. — the same namespacing its tables, its
// RBAC objects and its job kinds carry, in the catalogue's camelCase spelling.
//
// It shares unitCamel with screenIdent, and it needs that function's
// injectivity even more than the screen registry does: two units whose names
// mapped to one prefix would share ONE copy namespace, and copy keys are
// strings, so nothing would fail to compile — mergeUnitLocales would report
// `foo-1` and `foo1` as fighting over a key each of them owns, or, if only one
// of them declared it, hand a unit the other's strings.
func localeKeyPrefix(unit string) string {
	return "ext" + unitCamel(unit) + "."
}

// mergeUnitLocales folds every unit's copy into one map per locale, refusing
// the two shapes a silent merge would resolve arbitrarily.
func mergeUnitLocales(locales []unitLocale) (map[string]map[string]string, error) {
	merged := map[string]map[string]string{}
	claimedBy := map[string]string{}
	for _, l := range locales {
		prefix := localeKeyPrefix(l.Unit)
		for _, key := range sortedKeys(l.Keys) {
			// A unit rewriting `nav.people` would change core copy from a
			// directory, which is not a capability this tier grants.
			if !strings.HasPrefix(key, prefix) {
				return nil, fmt.Errorf("extensions/%s: %s/%s/%s.json declares key %q, which is outside its namespace — a unit's keys begin with %q",
					l.Unit, frontendLayer, localesLayer, l.Locale, key, prefix)
			}
			// Two units cannot claim one key. The catalogue is one flat
			// namespace, so a last-wins merge would make which copy renders
			// depend on the order the filesystem was read in.
			id := l.Locale + "\x00" + key
			if prev, taken := claimedBy[id]; taken && prev != l.Unit {
				return nil, fmt.Errorf("copy key %q (%s) is claimed by both %s and %s — one key, one owner",
					key, l.Locale, prev, l.Unit)
			}
			claimedBy[id] = l.Unit
			if merged[l.Locale] == nil {
				merged[l.Locale] = map[string]string{}
			}
			merged[l.Locale][key] = l.Keys[key]
		}
	}
	return merged, nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
