// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The two commands one tool serves through TWO contract operations
// (gradionhq/margince-poc-v1#928 task 7): merge_records is mergePerson and
// mergeOrganization, and enrich is scrapeCompany and deepReadCompany. Both are
// where the seam has to carry meaning rather than shape.
//
// A merge is the only command here that names TWO records, and which is which
// is the whole of it — one row survives and one is archived into it. The REST
// route says so in two places at once: POST /v1/people/{id}/merge merges the
// ROUTED person INTO the body's `target_id`, so the routed id is the record
// merged FROM and the approval binds to the survivor named in the body. The
// route walk this seam replaces reads the routed id and gets the wrong half.
//
// An enrich is the same tool at two depths, and the depth is the difference
// between reading one page and crawling a whole site. It is carried as a TYPE
// whose two values each decoder sets STRUCTURALLY — never parsed from a string
// on the REST door — so there is no string for a mistranslation to hide in: a
// descriptor that synthesized "site" for scrapeCompany would satisfy every
// schema and describe a whole-site crawl to a human approving a one-page read.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// MergeCommand is one merge, whichever door asked for it.
//
// SourceID is the record merged FROM — archived and redirected to the
// survivor — and TargetID is the survivor everything relinks to. Both are
// operands and both belong here: each is read separately, each carries its own
// refusal, and the summary names both, so a command holding one of them could
// only describe half of what happens.
type MergeCommand struct {
	RecordType string
	SourceID   ids.UUID
	TargetID   ids.UUID
}

// NewMergeCall binds one merge to the resolver that answers for it, reading
// both halves through the record seam the merge itself writes through.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewMergeCall(records datasource.SystemOfRecordProvider, cmd MergeCommand) GovernedCall {
	return bind[MergeCommand](&mergeResolver{records: records}, cmd)
}

type mergeResolver struct {
	records datasource.SystemOfRecordProvider
	// survivor and source are the two rows both questions rest on, remembered
	// for the reason anchoredRecord (command.go) remembers one: Guards refuses
	// on what the reads found and Subject pins and names it, and two readings
	// are two moments those answers are free to describe differently. A pair
	// rather than two anchoredRecords because the record TYPE is the command's,
	// not the resolver's — a merge is person-to-person or org-to-org, and the
	// type arrives with the call.
	survivor datasource.Record
	source   datasource.Record
	read     bool
}

// errNothingToMerge refuses a merge of a record into itself: there is no
// survivor and no archived half, so nothing an approval could describe.
var errNothingToMerge = errors.New("source and target must differ")

// mergeableTypeError refuses a record type that has no merge verb.
func mergeableTypeError(recordType string) error {
	return &BadArgsError{
		Cause:    fmt.Errorf("record_type %q cannot be merged", recordType),
		Guidance: "mergeable types are " + strings.Join(mergeableTypeNames(), ", "),
	}
}

// halves reads both records the merge touches, once, in the order their
// refusals are reported.
func (r *mergeResolver) halves(ctx context.Context, cmd MergeCommand) (survivor, source datasource.Record, err error) {
	if r.read {
		return r.survivor, r.source, nil
	}
	if !mergeableTypes[cmd.RecordType] {
		return datasource.Record{}, datasource.Record{}, mergeableTypeError(cmd.RecordType)
	}
	if cmd.SourceID == cmd.TargetID {
		return datasource.Record{}, datasource.Record{}, &BadArgsError{Cause: errNothingToMerge}
	}
	entity := datasource.EntityType(cmd.RecordType)
	survivor, err = r.records.Read(ctx, datasource.EntityRef{Type: entity, ID: cmd.TargetID})
	if err != nil {
		return datasource.Record{}, datasource.Record{}, err
	}
	source, err = r.records.Read(ctx, datasource.EntityRef{Type: entity, ID: cmd.SourceID})
	if err != nil {
		return datasource.Record{}, datasource.Record{}, err
	}
	r.survivor, r.source, r.read = survivor, source, true
	return survivor, source, nil
}

// Subject pins the SURVIVOR's version: the human's yes is a judgment about
// merging into B as it is now, so if B changes before redemption the approval
// no longer covers it (version skew, re-stage). The summary names both halves,
// because a merge that named only the survivor would ask a human to release a
// change without saying what disappears into it.
func (r *mergeResolver) Subject(ctx context.Context, cmd MergeCommand) (StageInfo, error) {
	survivor, source, err := r.halves(ctx, cmd)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    cmd.RecordType,
		TargetID:      cmd.TargetID,
		TargetVersion: &survivor.Version,
		Summary: fmt.Sprintf("Merge %s %s into %s",
			cmd.RecordType, recordLabel(source), recordLabel(survivor)),
	}, nil
}

// Guards refuses a record type with no merge verb, a merge of a record into
// itself, either half the caller cannot see, and either half whose authority
// lives in another system of record.
//
// EVERY record this change touches, not just the pinned one, and named so a
// human reading the refusal knows which half blocked it. Validating only the
// survivor leaves the other half unguarded: the merge archives and relinks the
// source, and an externally-held source under a locally-authoritative survivor
// is still a change no approval could release.
func (r *mergeResolver) Guards(ctx context.Context, cmd MergeCommand) error {
	survivor, source, err := r.halves(ctx, cmd)
	if err != nil {
		return err
	}
	if err := refuseStagingElsewhere(survivor); err != nil {
		return fmt.Errorf("the record being merged INTO: %w", err)
	}
	if err := refuseStagingElsewhere(source); err != nil {
		return fmt.Errorf("the record being merged FROM: %w", err)
	}
	return nil
}

// EnrichCommand is one site read, whichever door asked for it.
//
// Depth is EnrichDepth rather than a string on purpose: the two contract
// operations behind this one verb differ ONLY in it, and each door sets it
// structurally — the tool from its own `depth` argument's closed vocabulary,
// the REST door from which of its two routes was taken. A string here would be
// a place for scrapeCompany to be described to a human as a whole-site crawl.
//
// URL is the caller's override for the organization's own domain, empty when
// they named none.
type EnrichCommand struct {
	OrganizationID ids.UUID
	URL            string
	Depth          EnrichDepth
}

// NewEnrichCall binds one site read to the resolver that answers for it,
// reading the organization through the record seam.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewEnrichCall(records datasource.SystemOfRecordProvider, cmd EnrichCommand) GovernedCall {
	return bind[EnrichCommand](&enrichResolver{
		organization: anchoredRecord{records: records, entityType: datasource.EntityOrganization},
	}, cmd)
}

type enrichResolver struct {
	organization anchoredRecord
}

// Subject names the ORGANIZATION the approval binds to, pins its version, and
// says WHICH read is being released — a human approving a whole-site crawl
// must not read a line that says "page".
func (r *enrichResolver) Subject(ctx context.Context, cmd EnrichCommand) (StageInfo, error) {
	rec, err := r.organization.row(ctx, cmd.OrganizationID)
	if err != nil {
		return StageInfo{}, err
	}
	target := "its own domain"
	if cmd.URL != "" {
		target = cmd.URL
	}
	return StageInfo{
		TargetType:    string(datasource.EntityOrganization),
		TargetID:      cmd.OrganizationID,
		TargetVersion: &rec.Version,
		Summary: fmt.Sprintf("Read %s from %s and propose enrichment of %s",
			cmd.Depth, target, recordLabel(rec)),
	}, nil
}

// Guards refuses an override URL the fetch could never take, then the
// organization itself. It does not admit the DEPTH: the field's type is the
// admission, so there is no invalid value for a check to answer.
func (r *enrichResolver) Guards(ctx context.Context, cmd EnrichCommand) error {
	if err := requireEnrichURL(cmd.URL); err != nil {
		return err
	}
	return r.organization.refuse(ctx, cmd.OrganizationID)
}

// requireEnrichURL admits the override target: the same admission the REST
// route applies before it fetches, so a scheme-less or hostless target is a bad
// argument rather than a thin page. An empty override is not a target at all —
// the organization's own domain is read instead — so it passes.
//
// One function for both doors: the staging path asks it through Guards above
// and the execution path through readEnrichArgs, so a URL refused before a
// human is asked cannot be a URL the approved retry fetches.
func requireEnrichURL(raw string) error {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return &BadArgsError{Cause: fmt.Errorf("url %q must be an absolute http(s) URL", raw)}
	}
	return nil
}
