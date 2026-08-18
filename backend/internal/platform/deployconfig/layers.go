// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

// The file layer of OPS-CFG-1 is two files, not one.
//
// A deployment posture changes a handful of keys and shares the rest. Before
// this, the two ways to express that were both bad: keep a whole second copy of
// the configuration per posture and let them drift, or have a script append
// text to the file at boot — which is what `make dev` did to arm the data
// reset, so the dev stack's most destructive capability was turned on by a
// shell heredoc that no review ever saw as a diff.
//
// So the base file states what the installation IS, and an overlay beside it
// states what THIS POSTURE changes:
//
//	config/margince.yaml       every installation
//	config/margince.dev.yaml   what a dev stack does differently
//
// Within the file layer the overlay wins; the whole file layer still sits
// between compiled defaults and the environment, exactly where OPS-CFG-1 puts
// it. The full order, unchanged by this file:
//
//	compiled defaults -> margince.yaml -> margince.<posture>.yaml -> env -> flags
//
// How a key merges is legible from the YAML in front of the operator, which is
// the property that makes a two-file layer safe to reason about:
//
//	a scalar   the overlay's value replaces the base's
//	a mapping  merges key by key, so an overlay adds a provider without
//	           restating the ones the base already names
//	a list     replaces entirely, because half a list is not a list — an
//	           overlay naming [SEK] means SEK, not "USD, GBP, CHF and SEK"
//
// The consequence worth knowing: an overlay can add a mapping key and can
// change one, but cannot REMOVE one. A posture that must not have a key removes
// it from the base and adds it to the postures that want it.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// Load reads the deployment configuration for a posture: the base file, then
// the overlay that posture names, validated once over the merged result.
//
// Neither file has to exist. A missing base is how an already-bootstrapped
// installation runs on defaults alone; a missing overlay is the ordinary case
// for every posture that needs no changes. What is NOT tolerated is a file that
// exists and is wrong — an unreadable path, a typo'd key, a value out of range
// — because a configuration file that half-applied is worse than one that
// stopped the boot.
func Load(path string, env runtimeenv.Environment) (Config, error) {
	// Version is the compiled-defaults layer, so the layering has no
	// special case for "no file at all" — an installation with neither
	// file and one with an empty base reach validate() the same way.
	cfg := Config{Version: 1}
	for _, layer := range []string{path, OverlayPath(path, env)} {
		if err := applyLayer(layer, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyLayer decodes one file over what the earlier layers left, naming the
// file in any failure. With two candidate files an unqualified "unknown field
// mcp_enabled" would send an operator to the wrong one.
func applyLayer(path string, cfg *Config) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator's own --config path, and the overlay derived from it
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deployconfig: reading %s: %w", path, err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	// A file holding only comments decodes to EOF rather than to nothing,
	// and that is a real file an operator writes: an overlay committed as a
	// placeholder for the posture, with the keys still commented out.
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("deployconfig: %s: %w", path, err)
	}
	return nil
}

// OverlayPath names the posture's overlay: the base path with the posture
// inserted before its extension, so `config/margince.yaml` under MARGINCE_ENV=dev
// names `config/margince.dev.yaml`.
//
// Derived rather than configured, and derived for EVERY posture including
// production. A second flag for the overlay path would let the two disagree,
// and a production installation that wants the split gets it by the same rule
// the dev stack does instead of by an exception it has to discover.
func OverlayPath(path string, env runtimeenv.Environment) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "." + string(env) + ext
}
