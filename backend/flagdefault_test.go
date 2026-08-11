// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// No string flag takes its default straight from the environment.
//
// `flag` renders a non-empty default as `(default "…")`, so a value wired into a
// flag's default reaches the usage text — printed on `-h` and on ANY parse error,
// including a single mistyped argument. What these binaries read from the
// environment is DSNs (the app role's and, via MARGINCE_SCHEMA_DSN, the owner
// role's), OAuth client secrets, the connector-state HMAC key, the webhook sealing
// key and the /metrics bearer token. On CI that stderr is a public build log; in a
// container it is the pod's log stream.
//
// internal/platform/cliflags is the shape that avoids it: register the LITERAL
// default, resolve the environment after Parse. Each role additionally asserts its
// own usage output carries no environment value — but that check can only seed the
// variables it can SEE, so a flag reverting to a direct read would drop out of its
// scope entirely. This gate closes that: it bans the shape itself, over every
// binary, derived from the tree rather than a list of the three that exist today.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A flag default read straight from the environment, in the two registration
// shapes the tree uses: `fs.StringVar(&target, "name", DEFAULT, …)` and
// `fs.String("name", DEFAULT, …)`. Both spellings of the read are covered — the
// bare os.Getenv and the literal-fallback envOr helper.
//
// Anchored on the registration on purpose. An unanchored `, os.Getenv(` also
// matches a POST-parse read, which is the shape this gate is steering code
// towards — cmd/migrate's `cmp.Or(flagValue, os.Getenv(…), os.Getenv(…))` is
// correct and must not be reported.
var directEnvFlagDefault = []*regexp.Regexp{
	regexp.MustCompile(`fs\.StringVar\(&[\w.]+,\s*"[\w-]+",\s*(?:os\.Getenv|envOr)\("(MARGINCE_\w+)"`),
	regexp.MustCompile(`fs\.String\(\s*"[\w-]+",\s*(?:os\.Getenv|envOr)\("(MARGINCE_\w+)"`),
}

func TestNoStringFlagDefaultsToAnEnvironmentValue(t *testing.T) {
	roots := []string{"cmd", "internal/compose"}
	checked := 0

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, readErr := os.ReadFile(filepath.Clean(path))
			if readErr != nil {
				return readErr
			}
			checked++
			for _, re := range directEnvFlagDefault {
				for _, m := range re.FindAllStringSubmatch(string(src), -1) {
					t.Errorf("%s registers a flag whose default is read straight from %s. "+
						"flag echoes a non-empty default in its usage output, so a mistyped "+
						"argument prints that value to stderr — a public build log on CI, the "+
						"pod's log stream in a container. Register the literal default with "+
						"internal/platform/cliflags and resolve the environment after Parse.",
						path, m[1])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	// A sweep that read nothing would pass exactly like a clean tree.
	if checked == 0 {
		t.Fatal("no Go files were read, so this gate compared nothing")
	}
	t.Logf("swept %d files", checked)
}
