// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig_test

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

// The compiled-in ceilings, restated here on purpose: this is the test that
// notices a default moving, and a test that reads the constant it is checking
// agrees with any value at all.
const (
	defaultAttachmentBytes = 25_000_000
	defaultCSVImportBytes  = 10_000_000
	defaultLinkedInBytes   = 8_000_000
)

func parseUploads(t *testing.T, body string) (deployconfig.UploadLimits, error) {
	t.Helper()
	cfg, err := deployconfig.Parse([]byte("version: 1\n" + body))
	if err != nil {
		return deployconfig.UploadLimits{}, err
	}
	return cfg.EffectiveUploads(), nil
}

func TestAFileWithNoUploadsSectionGetsTheCompiledCeilings(t *testing.T) {
	limits, err := parseUploads(t, "")
	if err != nil {
		t.Fatalf("a minimal file was refused: %v", err)
	}
	want := deployconfig.UploadLimits{
		Attachment:     defaultAttachmentBytes,
		CSVImport:      defaultCSVImportBytes,
		LinkedInImport: defaultLinkedInBytes,
	}
	if limits != want {
		t.Errorf("unconfigured limits are %+v, want %+v — an installation that "+
			"never wrote the section must still be bounded, and generously enough "+
			"to upload the paper the product is about", limits, want)
	}
}

func TestOneConfiguredCeilingLeavesTheOthersAlone(t *testing.T) {
	// The mistake this catches is a section that replaces the whole block:
	// naming one route would then silently zero the two beside it, and a zero
	// ceiling refuses every upload on a route nobody was thinking about.
	limits, err := parseUploads(t, "uploads:\n  attachment_mb: 40\n")
	if err != nil {
		t.Fatalf("a partial uploads section was refused: %v", err)
	}
	if limits.Attachment != 40_000_000 {
		t.Errorf("attachment ceiling is %d, want the configured 40 MB", limits.Attachment)
	}
	if limits.CSVImport != defaultCSVImportBytes || limits.LinkedInImport != defaultLinkedInBytes {
		t.Errorf("configuring one route changed the others: %+v", limits)
	}
}

func TestEveryRouteIsSeparatelyConfigurable(t *testing.T) {
	limits, err := parseUploads(t, `uploads:
  attachment_mb: 50
  csv_import_mb: 20
  linkedin_import_mb: 3
`)
	if err != nil {
		t.Fatalf("a full uploads section was refused: %v", err)
	}
	want := deployconfig.UploadLimits{
		Attachment: 50_000_000, CSVImport: 20_000_000, LinkedInImport: 3_000_000,
	}
	if limits != want {
		t.Errorf("limits are %+v, want %+v — three keys that resolve to the "+
			"same place are one key with two spellings", limits, want)
	}
}

// Decimal, not binary. Every surface that states this number — the key here,
// the server's refusal, the upload form's hint — must state the same one, and
// `25 << 20` reads as "25 MB" while admitting 26.2 million bytes.
func TestAMegabyteIsAMillionBytes(t *testing.T) {
	limits, err := parseUploads(t, "uploads:\n  attachment_mb: 1\n")
	if err != nil {
		t.Fatalf("a 1 MB ceiling was refused: %v", err)
	}
	if limits.Attachment != 1_000_000 {
		t.Errorf("1 MB resolved to %d bytes, want 1000000 — a binary megabyte "+
			"here would over-state every limit this installation reports by 4.8%%",
			limits.Attachment)
	}
}

func TestAnUnusableCeilingIsRefusedAtBootByName(t *testing.T) {
	// Fail-fast (OPS-CFG-2). A silent clamp would leave an operator who asked
	// for 500 MB believing they got it, with the file still saying so.
	for _, tc := range []struct {
		name string
		body string
		key  string
	}{
		{"past the range", "uploads:\n  attachment_mb: 500\n", "attachment_mb"},
		{"negative", "uploads:\n  csv_import_mb: -1\n", "csv_import_mb"},
		{
			// Told apart from an absent key on purpose: zero is a ceiling that
			// refuses every upload, which is far likelier a mistake than an
			// intention, and it would otherwise read as "use the default".
			"explicitly zero", "uploads:\n  linkedin_import_mb: 0\n", "linkedin_import_mb",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseUploads(t, tc.body)
			if err == nil {
				t.Fatal("the value was accepted — an installation boots with a " +
					"ceiling nobody can use and nothing says so")
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("the refusal %q does not name the key at fault", err)
			}
		})
	}
}

func TestATypoInTheUploadsSectionIsABootError(t *testing.T) {
	// Strict decoding, like every other section: `attachment_mp: 40` must not
	// read as "the operator said nothing about attachments".
	if _, err := parseUploads(t, "uploads:\n  attachment_mp: 40\n"); err == nil {
		t.Fatal("an unknown uploads key was ignored rather than refused")
	}
}
