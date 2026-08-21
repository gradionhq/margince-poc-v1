// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The archive resolver is the seam both doors ask, so its answers are pinned
// here rather than through either door: what it refuses, what it names, and
// what it does when the record seam has never heard of the type — the case
// only the REST door reaches, because the tool's schema cannot express it.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// A target the caller cannot see is refused BEFORE anything is staged. The
// archive itself would answer the same not-found once released, by which point
// a human has spent a one-shot authority on a call that was never going to run.
//
// Asked of Guards directly rather than through StageSubject: Subject reads the
// same row for its label and would refuse this too, so a whole-seam assertion
// passes whether or not the guard is there at all.
func TestArchiveGuardsRefuseATargetTheCallerCannotSee(t *testing.T) {
	call := NewArchiveCall(unreadableProvider{}, ArchiveCommand{RecordType: "person", ID: ids.NewV7()})

	if err := call.Guards(context.Background()); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("guarding an unreadable person answered %v, want the row-scope miss — a staged approval "+
			"for a record the caller cannot see is authority nobody asked for", err)
	}
}

// A target whose authority lives in another system of record is refused for the
// reason refuseStagingElsewhere states: the decidability probe and the version
// pin both read OUR tables, so the approval could never be released.
func TestArchiveGuardsRefuseATargetHeldElsewhere(t *testing.T) {
	call := NewArchiveCall(elsewhereProvider{}, ArchiveCommand{RecordType: "person", ID: ids.NewV7()})

	if err := call.Guards(context.Background()); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("guarding a mirrored person answered %v, want the unsupported-by-SoR refusal", err)
	}
}

// The staged subject is the record type and the id, and no version pin: the
// pin is taken server-side inside the staging transaction, and one supplied
// here would be discarded.
func TestArchiveSubjectNamesTheRecordAndSuppliesNoPin(t *testing.T) {
	id := ids.NewV7()
	provider := stubRecordProvider{rec: stagedRecord(datasource.EntityPerson, id, true)}
	call := NewArchiveCall(provider, ArchiveCommand{RecordType: "person", ID: id})

	info, err := StageSubject(context.Background(), call)
	if err != nil {
		t.Fatalf("staging a readable person answered %v, want it staged", err)
	}
	if info.TargetType != "person" || info.TargetID != id {
		t.Errorf("staged target = (%s,%s), want (person,%s) — the engine cannot pin or scope a target it was not given",
			info.TargetType, info.TargetID, id)
	}
	if info.TargetVersion != nil {
		t.Errorf("the resolver supplied target_version %d — the pin comes from the row inside the staging "+
			"transaction, and one passed here is discarded", *info.TargetVersion)
	}
	// stagedRecord names itself "Acme"; the human reading the inbox is told
	// which record disappears, not only which id.
	if !strings.Contains(info.Summary, "Acme") {
		t.Errorf("staged summary %q does not name the record — an id alone tells an approver nothing about "+
			"what they are archiving", info.Summary)
	}
}

// A type the record seam has never heard of still stages. Six of the twelve
// archivable types are archived by their own module rather than through the
// seam, and the seam's refusals do not describe them — asking it would answer
// "not served here" and refuse an ordinary archive. The tool's schema is what
// is narrow, not the operation.
func TestArchiveStagesATypeTheRecordSeamDoesNotServe(t *testing.T) {
	id := ids.NewV7()
	// A provider that fails every read, so a resolver that consulted the seam
	// for a tag would be caught here rather than passing on a lenient stub.
	call := NewArchiveCall(unreadableProvider{}, ArchiveCommand{RecordType: "tag", ID: id})

	info, err := StageSubject(context.Background(), call)
	if err != nil {
		t.Fatalf("staging a tag archive answered %v, want it staged — DELETE /v1/tags/{id} is a governed "+
			"operation whose target the seam simply does not serve", err)
	}
	if info.TargetType != "tag" || info.TargetID != id {
		t.Errorf("staged target = (%s,%s), want (tag,%s)", info.TargetType, info.TargetID, id)
	}
	if !strings.Contains(info.Summary, "tag") || !strings.Contains(info.Summary, id.String()) {
		t.Errorf("staged summary %q must name both the type and the id — together they are all the inbox "+
			"has for a target the seam cannot label", info.Summary)
	}
}

// archivesWhatNativeDoes is the RecordArchiverV2 half a stub embeds to stand
// for a provider that CAN carry an approved version.
//
// A stub that omits it is a fork's v1-only adapter, which staging refuses on
// purpose — so leaving it off is a choice a suite makes deliberately (see
// v1Archiver in archiveauthority_test.go), never an omission that quietly
// turns a staging assertion into a refusal one.
type archivesWhatNativeDoes struct{}

func (archivesWhatNativeDoes) ArchivableTypes(context.Context) ([]datasource.EntityType, error) {
	return []datasource.EntityType{
		datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityDeal,
		datasource.EntityProject, datasource.EntityRelationship, datasource.EntityActivity,
	}, nil
}

func (archivesWhatNativeDoes) RefuseArchive(context.Context, datasource.EntityRef) error { return nil }

func (archivesWhatNativeDoes) ArchiveAt(_ context.Context, in datasource.ArchiveInput) (datasource.EntityRef, error) {
	return in.Ref, nil
}

// countingProvider answers every read from the same authoritative record and
// counts how many times it was asked.
type countingProvider struct {
	datasource.SystemOfRecordProvider
	archivesWhatNativeDoes
	reads int
}

func (c *countingProvider) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	c.reads++
	return datasource.Record{
		Ref: ref, Fields: json.RawMessage(`{"name":"Acme"}`), Version: 4,
		Freshness: datasource.FreshnessInfo{Authoritative: true},
	}, nil
}

// Both questions are answered from ONE read of the target.
//
// Not an efficiency assertion. Two reads are two rows: the guard would judge
// the authority of one and the subject would describe the other, and a record
// archived or re-pointed between them makes the approval a human sees disagree
// with the authority that admitted it.
func TestBothGovernanceQuestionsAreAnsweredFromOneRead(t *testing.T) {
	provider := &countingProvider{}
	call := NewArchiveCall(provider, ArchiveCommand{RecordType: "person", ID: ids.NewV7()})

	if _, err := StageSubject(context.Background(), call); err != nil {
		t.Fatalf("staging a readable person answered %v, want it staged", err)
	}
	if provider.reads != 1 {
		t.Errorf("the resolver read its target %d times, want 1 — the guard and the subject must describe "+
			"the same row, not two readings of one id", provider.reads)
	}
}

// A resolver asked about a second target reads THAT target.
//
// Nothing outside this package can reach a resolver — NewArchiveCall binds one
// to its command and hands back the call — so this reaches past the constructor
// deliberately: the memo's key is what makes the binding a belt rather than the
// only thing between two callers and a shared read.
func TestAResolverAskedAboutASecondTargetReadsIt(t *testing.T) {
	provider := &countingProvider{}
	resolver := &archiveResolver{records: provider}
	ctx := context.Background()

	first, err := resolver.Subject(ctx, ArchiveCommand{RecordType: "person", ID: ids.NewV7()})
	if err != nil {
		t.Fatalf("the first subject answered %v", err)
	}
	second, err := resolver.Subject(ctx, ArchiveCommand{RecordType: "person", ID: ids.NewV7()})
	if err != nil {
		t.Fatalf("the second subject answered %v", err)
	}

	if provider.reads != 2 {
		t.Errorf("two targets cost %d reads, want 2 — a remembered row must not answer for a record it "+
			"was not read for", provider.reads)
	}
	if first.TargetID == second.TargetID {
		t.Error("both subjects named the same target id")
	}
}
