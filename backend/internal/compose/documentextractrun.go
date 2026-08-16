// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What one reading of one document does, from claiming the run record to
// closing it.
//
// The division of labour with documentextract.go is: that file owns the
// question put to the model and what may come back, this one owns the run — the
// claim, the lane, and the three outcomes a rep can be shown. Keeping the
// outcomes here is deliberate, because the difference between them is the
// product: "still reading", "read it and it states none of them", and "could
// not read it" must never collapse into one another (RD-AC-N-2).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// maxDocumentBytes bounds what may be sent as an input part.
//
// It is derived from the wire, not chosen for roundness: inline bytes are
// base64 on every carrying adapter, which costs four bytes for every three, so
// this becomes ~10.7 MB of request body. Both adapters that carry documents
// today accept a request an order of magnitude under their vendors' ~20 MB
// ceiling at that size, leaving the prompt and the schema room that does not
// have to be counted per call.
//
// DOC-PARAM-4 lets a 25 MB file be ATTACHED, which is a different question from
// what one model call carries. A file above this is refused as a reading rather
// than truncated: half a scanned contract read confidently is worse than no
// reading at all, because nothing on the panel would say which half it saw.
const maxDocumentBytes = 8 << 20

// maxDocumentTextChars bounds the text lane, matching what a transcript reading
// addresses (activities.MaxReadableTranscriptChars) — the same question of how
// much prose one call can hold steadily, asked of the same models.
const maxDocumentTextChars = activities.MaxReadableTranscriptChars

// documentTextMIMEs are the media types whose bytes ARE their text, so no
// parser stands between the file and the model.
//
// This is the lane the survey of other BYOK systems calls the
// provider-independent one, reached without any of their machinery: a file that
// is already text needs no OCR engine, no document-intelligence service and no
// vision-capable binding, and its quotes can be checked against the document
// verbatim (RD-AC-N-4) — which the bytes lane can never do.
var documentTextMIMEs = []string{
	"text/plain", "text/csv", "text/markdown", "text/html", "application/json", "message/rfc822",
}

// DocumentExtractor reads an attached document for the deal facts it states.
type DocumentExtractor struct {
	pool  *pgxpool.Pool
	brain documentCompleter
	log   *slog.Logger
}

// documentCompleter is the completion seam for a lane whose input may be a
// document: it answers what it can CARRY as well as how to call it.
//
// The ordinary completer seam is not enough here, and the reason is the one the
// LiteLLM-style capability registry exists for. A lane that learns its binding
// is text-only by sending the bytes and being refused has already written a
// failed attempt into the operator's own call trace — for a configuration that
// is not failing, merely text-only. Asking first is what lets "this binding
// cannot read images" be reported as the plain fact it is.
type documentCompleter interface {
	completer
	// AttachmentMIMEs is what a caller may hand this lane, in
	// model.CarriesMIME's spelling. Empty means documents cannot go to it.
	AttachmentMIMEs() []string
}

// NewDocumentExtractor builds the engine over the pool and one model lane.
func NewDocumentExtractor(pool *pgxpool.Pool, brain documentCompleter, log *slog.Logger) *DocumentExtractor {
	return &DocumentExtractor{pool: pool, brain: brain, log: log}
}

// documentReadStore is the slice of the activities store one reading drives.
// Named so the run can be tested against the real store or a double without
// either standing in for the whole module.
type documentReadStore interface {
	BeginExtractionRead(ctx context.Context, readID ids.UUID, reclaimAfter time.Duration) (activities.ExtractionRead, error)
	FinishExtractionRead(ctx context.Context, readID ids.UUID, outcome activities.ExtractionReadOutcome) error
	ReleaseExtractionRead(ctx context.Context, readID ids.UUID, claimedAt time.Time) error
	OpenAttachment(ctx context.Context, id ids.UUID) (crmcontracts.Attachment, io.ReadCloser, error)
}

// Read performs one reading: claim the run, put the document to the model on
// whichever lane its type and the binding allow, and close the run with what
// happened.
//
// It returns an error only for a fault the JOB should retry — the model lane
// being down, the database being unreachable. A reading that legitimately could
// not be used closes the run and returns nil, because retrying would ask the
// same question of the same document and get the same answer.
func (d *DocumentExtractor) Read(ctx context.Context, store documentReadStore, readID, attachmentID ids.UUID) error {
	claim, err := store.BeginExtractionRead(ctx, readID, activities.ExtractionReadLease)
	if err != nil {
		return err
	}
	// The claim's own start time travels with every write below, so each one is
	// scoped to THIS attempt: a lease that expired mid-call has already handed
	// the reading to somebody else, and this worker must not close or re-queue
	// what it no longer holds.
	claimedAt := claimStart(claim)

	src, detail, err := d.sourceFor(ctx, store, attachmentID)
	if err != nil {
		if terminal, msg := unreadableDocument(err); terminal {
			return d.fail(ctx, store, readID, claimedAt, msg)
		}
		// Anything else — the database unreachable, the object store down — is
		// the JOB's to retry. Closing the reading here would turn a blip into a
		// permanent verdict the rep has to notice and undo.
		return d.retryable(ctx, store, readID, claimedAt, err)
	}
	if detail != "" {
		// COULD NOT read it, which is not the same answer as read it and it
		// states none of them — and `done` is what the panel renders as the
		// second. A file this installation's model cannot carry, one with no
		// content type, one too large: each is a true "could not read", and each
		// is something an operator can change, so each also earns the retry
		// affordance only `failed` offers. No model call was made either way.
		return d.fail(ctx, store, readID, claimedAt, detail)
	}
	fields, err := d.ask(ctx, src)
	if err != nil {
		if errors.Is(err, errRefusedDocument) {
			d.log.WarnContext(ctx, "document reading refused",
				"attachment_extraction_id", readID, "attachment_id", attachmentID, "reason", err)
			return d.fail(ctx, store, readID, claimedAt,
				"the model's reading of this document could not be used; the document is unchanged and can be read again")
		}
		return d.retryable(ctx, store, readID, claimedAt, err)
	}
	return store.FinishExtractionRead(ctx, readID, activities.ExtractionReadOutcome{
		Status:    activities.ExtractionReadDone,
		Detail:    emptyReadingDetail(fields),
		Fields:    fields,
		ClaimedAt: claimedAt,
	})
}

// claimStart is the claimed row's own start time. It is set by the claim that
// just succeeded, so a zero here would mean the store returned a row it did not
// claim — worth failing loudly on rather than writing a zero timestamp into
// every subsequent CAS, where it would match nothing and look like a race.
func claimStart(claim activities.ExtractionRead) time.Time {
	if claim.StartedAt == nil {
		return time.Time{}
	}
	return *claim.StartedAt
}

// retryable hands the reading back before handing the job back.
//
// Returning the error alone would leave the row `running` with nobody working
// it: River retries inside the lease, the re-claim is refused as somebody
// else's, and the retry then reports success — a reading stranded live forever,
// rendering as "reading…" with nothing for a rep to press.
func (d *DocumentExtractor) retryable(
	ctx context.Context, store documentReadStore, readID ids.UUID, claimedAt time.Time, cause error,
) error {
	if err := store.ReleaseExtractionRead(ctx, readID, claimedAt); err != nil {
		d.log.WarnContext(ctx, "could not release a reading being retried",
			"attachment_extraction_id", readID, "error", err)
	}
	return cause
}

// emptyReadingDetail says why a completed reading offered nothing, and says
// nothing when it offered something.
//
// A reading that grounded no field is a correct and common answer — plenty of
// documents state none of the four — but an empty panel that does not explain
// itself reads as a broken feature, which is the one thing this whole shape
// exists to prevent.
func emptyReadingDetail(fields []extraction.ExtractedField) string {
	for _, f := range fields {
		if !f.Omitted {
			return ""
		}
	}
	return "this document states none of the deal fields clearly enough to offer one"
}

// sourceFor decides which lane this document takes, and reads it.
//
// It answers one of three ways, and the middle one is the point: a source to
// read, or a detail saying this installation cannot read this document, or an
// error. The detail is not a failure — the document is intact and the binding
// is working, they simply do not meet.
func (d *DocumentExtractor) sourceFor(
	ctx context.Context, store documentReadStore, attachmentID ids.UUID,
) (documentSource, string, error) {
	meta, body, err := store.OpenAttachment(ctx, attachmentID)
	if err != nil {
		return documentSource{}, "", err
	}
	defer func() {
		if err := body.Close(); err != nil {
			d.log.WarnContext(ctx, "closing document body", "attachment_id", attachmentID, "error", err)
		}
	}()
	// ContentType is optional on the row; a document that never declared one
	// takes neither lane, which is the honest answer — the alternative is
	// sniffing bytes here, a second content-type authority beside DOC-PARAM-9's.
	mime := ""
	if meta.ContentType != nil {
		mime = strings.ToLower(strings.TrimSpace(*meta.ContentType))
	}
	// One byte past the bound is what distinguishes "exactly at the limit" from
	// "truncated to the limit"; without it a document of exactly maxDocumentBytes
	// and one of a gigabyte both arrive as a full buffer and look identical.
	bytes, err := io.ReadAll(io.LimitReader(body, maxDocumentBytes+1))
	if err != nil {
		return documentSource{}, "", fmt.Errorf("read document bytes: %w", err)
	}
	if len(bytes) > maxDocumentBytes {
		return documentSource{}, fmt.Sprintf(
			"this document is larger than the %d MB one reading carries; a reading of part of it could not say which part it saw",
			maxDocumentBytes>>20), nil
	}
	if model.CarriesMIME(documentTextMIMEs, mime) {
		src, detail := d.textSource(meta, bytes)
		return src, detail, nil
	}
	if model.CarriesMIME(d.brain.AttachmentMIMEs(), mime) {
		return documentSource{
			Part:     model.Attachment{MIME: mime, Bytes: bytes, Name: meta.Filename},
			Filename: meta.Filename,
		}, "", nil
	}
	if mime == "" {
		return documentSource{}, "this document declares no content type, so nothing can say how to read it", nil
	}
	return documentSource{}, fmt.Sprintf(
		"this installation's model cannot read a %s document; a file whose text can be read directly, or a model bound to carry documents, would be read",
		mime), nil
}

// textSource takes the text lane, where the document's bytes are its text.
func (d *DocumentExtractor) textSource(meta crmcontracts.Attachment, raw []byte) (documentSource, string) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return documentSource{}, "this document carries no text to read"
	}
	if len(text) > maxDocumentTextChars {
		return documentSource{}, fmt.Sprintf(
			"this document is %d characters, and one reading addresses at most %d",
			len(text), maxDocumentTextChars)
	}
	return documentSource{Text: text, Filename: meta.Filename}, ""
}

// ask puts one document to the model and returns what it may act on.
//
// The validator runs on the shipping path, not only in the certification lane.
// A site whose corpus grades a validator production never calls is certifying a
// standard the product does not hold itself to — the reply that would have been
// refused in the lane reaches a deal instead.
func (d *DocumentExtractor) ask(ctx context.Context, src documentSource) ([]extraction.ExtractedField, error) {
	req := documentExtractRequest(src)
	validate := documentShapeValid(src)
	var resp model.Response
	var err error
	if structured, ok := d.brain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, validate)
	} else {
		resp, err = d.brain.Complete(ctx, req)
	}
	if err != nil {
		if errors.Is(err, model.ErrAttachmentUnsupported) {
			// The binding declared it carries this type and then refused it on
			// the wire. That is the two halves of one declaration disagreeing,
			// which is a configuration fault rather than a fault of this
			// document — so it is refused, not retried.
			return nil, fmt.Errorf("%w: the model refused a document type its binding declares it carries: %w",
				errRefusedDocument, err)
		}
		if errors.Is(err, ai.ErrOutputRejected) {
			return nil, fmt.Errorf("%w: %w", errRefusedDocument, err)
		}
		return nil, err
	}
	// Validated again even when CompleteValidated already ran it: a bare
	// completer (the offline fake, a role wired without the structured lane)
	// does not, and this is the only floor those paths have.
	if err := validate(resp.Text); err != nil {
		return nil, fmt.Errorf("%w: %w", errRefusedDocument, err)
	}
	return readDocumentFields(resp.Text)
}

// unreadableDocument separates a refusal a rep can act on from a fault the job
// should retry, and answers with the message the rep is shown.
//
// Only the typed refusals reach status_detail. A raw err.Error() there would
// put a driver string ("failed to connect to host=…") in front of a rep on the
// one field this feature exists to make readable, and would settle a transient
// blip as a permanent verdict.
func unreadableDocument(err error) (terminal bool, detail string) {
	if errors.Is(err, apperrors.ErrNotFound) {
		return true, "this document is no longer available to read"
	}
	if errors.Is(err, activities.ErrBlobstoreUnconfigured) {
		return true, "this installation stores no document bytes, so there is nothing to read"
	}
	return false, ""
}

// fail closes the run with a reason a rep can act on. A failure to record the
// failure is returned, so a run cannot be left claimed and silent.
func (d *DocumentExtractor) fail(
	ctx context.Context, store documentReadStore, readID ids.UUID, claimedAt time.Time, detail string,
) error {
	return store.FinishExtractionRead(ctx, readID, activities.ExtractionReadOutcome{
		Status: activities.ExtractionReadFailed, Detail: detail, ClaimedAt: claimedAt,
	})
}
