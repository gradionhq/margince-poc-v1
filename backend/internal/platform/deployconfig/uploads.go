// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

// The uploads block of margince.yaml: how many bytes one request may carry on
// each route that accepts a file (OPS-CFG-12, DOC-PARAM-11).
//
// It sits in its own file for the same reason overlay_budget does — it is a
// section with arithmetic of its own, defaults to merge and bounds to enforce,
// rather than values to hand on.
//
// What this section does NOT decide: which routes may carry a file. That list
// is a source-level declaration in the composition layer, and it is the whole
// security property — several handlers decode the body with no bound of their
// own and two of those routes are unauthenticated, so a route that carries no
// file must stay unable to obtain the file bound by claiming a media type. An
// operator chooses the number; nobody chooses the list at run time.

import "fmt"

// Uploads is the operator's per-route ceilings, in decimal megabytes.
//
// Pointers, so an absent key and an explicit `0` are different facts: absent
// takes the compiled-in default, while `attachment_mb: 0` is a ceiling that
// refuses every upload and is far more likely a mistake than an intention. It
// fails at boot rather than at the first upload nobody can explain.
type Uploads struct {
	// AttachmentMB bounds POST /v1/attachments — the documents surface.
	AttachmentMB *int `yaml:"attachment_mb"`
	// CSVImportMB bounds POST /v1/imports/sources — the CSV import source.
	CSVImportMB *int `yaml:"csv_import_mb"`
	// LinkedInImportMB bounds POST /v1/me/linkedin-connections.
	LinkedInImportMB *int `yaml:"linkedin_import_mb"`
}

// UploadLimits is Uploads resolved: every ceiling present, in bytes. It is what
// the composition layer receives, so no consumer downstream reads a pointer or
// re-applies a default — the two places a default is applied are the two places
// they can disagree.
type UploadLimits struct {
	Attachment     int64
	CSVImport      int64
	LinkedInImport int64
}

// The compiled-in ceilings, in decimal MB. Decimal, not binary, because the
// config key, the server's refusal and the upload form's hint all state this
// number and must state the same one: `25 << 20` reads as "25 MB" in a sentence
// while admitting 26.2 million bytes, and the reader whose 25.5 MB file it
// refuses has been told the wrong thing by 4.8%.
//
// The import ceilings are the historical caps read the way their own refusal
// messages have always spelled them ("the 10 MB import cap"), which tightens
// each by 4.8% — the code moving to match the prose rather than the reverse.
const (
	defaultAttachmentMB     = 25
	defaultCSVImportMB      = 10
	defaultLinkedInImportMB = 8
)

// The range an operator may choose inside.
//
// The floor is 1 MB because below that the JSON bound already governs and an
// "upload" route that cannot carry a small PDF is a broken installation, not a
// tightened one. The ceiling is 100 MB because the parse spills to the api's
// own temp filesystem and the part is then handed to object storage in a single
// PUT; past this the answer is a presigned direct-to-storage upload, which is a
// different design and not a bigger number here.
const (
	minUploadMB = 1
	maxUploadMB = 100
)

// bytesPerMB is the decimal megabyte this whole section is denominated in.
const bytesPerMB = 1_000_000

// EffectiveUploads resolves the operator's block over the compiled-in
// defaults. validate() has already rejected anything out of range, so every
// value here is one an upload can actually ride.
func (c Config) EffectiveUploads() UploadLimits {
	return UploadLimits{
		Attachment:     megabytes(c.Uploads.AttachmentMB, defaultAttachmentMB),
		CSVImport:      megabytes(c.Uploads.CSVImportMB, defaultCSVImportMB),
		LinkedInImport: megabytes(c.Uploads.LinkedInImportMB, defaultLinkedInImportMB),
	}
}

func megabytes(configured *int, fallback int) int64 {
	if configured == nil {
		return int64(fallback) * bytesPerMB
	}
	return int64(*configured) * bytesPerMB
}

// validate refuses an out-of-range ceiling at boot, naming the key (OPS-CFG-2,
// fail-fast). A silent clamp would leave an operator who asked for 500 MB
// believing they got it, and the file would keep saying so.
func (u Uploads) validate() error {
	for _, k := range []struct {
		key   string
		value *int
	}{
		{"attachment_mb", u.AttachmentMB},
		{"csv_import_mb", u.CSVImportMB},
		{"linkedin_import_mb", u.LinkedInImportMB},
	} {
		if k.value == nil {
			continue
		}
		if *k.value < minUploadMB || *k.value > maxUploadMB {
			return fmt.Errorf("deployconfig: uploads.%s is %d MB, outside the %d–%d MB range this build accepts",
				k.key, *k.value, minUploadMB, maxUploadMB)
		}
	}
	return nil
}
