// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The three questions a confirm-first archive asks the ROUTED executor, and
// what each one answered before it could.
//
// GovernanceResolver's own doc promises that Guards "refuses, BEFORE anything
// is staged, what the executor would refuse afterwards — so a human's one-shot
// approval is never spent on a call that was never going to run." These pin
// that promise against an executor that is deliberately NOT the one the caller
// would have assumed: a provider archiving three types where native archives
// six, refusing a row the caller can read, and recording the version it was
// handed.

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

// narrowArchiver is a routed executor whose archive surface is NARROWER than
// the native provider's, which is the overlay case: three types, not six.
//
// refuse is what its stores would answer at execution; archivedAt records the
// input the write actually received, so a pin that never reaches it is visible
// as a nil rather than as a passing test.
type narrowArchiver struct {
	datasource.SystemOfRecordProvider
	refuse error
	types  []datasource.EntityType
	// heldElsewhere makes the record non-authoritative, so a case can put the
	// system-of-record refusal and the executor refusal in play AT ONCE — which
	// is the only way their order is observable.
	heldElsewhere bool
	archivedAt    *datasource.ArchiveInput
}

func (n *narrowArchiver) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{
		Ref: ref, Fields: json.RawMessage(`{"name":"Acme"}`), Version: 4,
		Freshness: datasource.FreshnessInfo{Authoritative: !n.heldElsewhere},
	}, nil
}

func (n *narrowArchiver) ArchivableTypes(context.Context) ([]datasource.EntityType, error) {
	return n.types, nil
}

func (n *narrowArchiver) RefuseArchive(context.Context, datasource.EntityRef) error { return n.refuse }

func (n *narrowArchiver) ArchiveAt(_ context.Context, in datasource.ArchiveInput) (datasource.EntityRef, error) {
	n.archivedAt = &in
	return in.Ref, nil
}

// v1Archiver answers the frozen seam and nothing else — a fork's own adapter,
// and the fallback case every probe below has to state rather than assume.
type v1Archiver struct {
	datasource.SystemOfRecordProvider
	archived *datasource.EntityRef
}

func (v *v1Archiver) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{
		Ref: ref, Fields: json.RawMessage(`{"name":"Acme"}`), Version: 4,
		Freshness: datasource.FreshnessInfo{Authoritative: true},
	}, nil
}

func (v *v1Archiver) Archive(_ context.Context, ref datasource.EntityRef) (datasource.EntityRef, error) {
	v.archived = &ref
	return ref, nil
}

func threeTypes() []datasource.EntityType {
	return []datasource.EntityType{
		datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityDeal,
	}
}

func archiveArgsJSON(t *testing.T, recordType string, id ids.UUID) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(archiveArgs{RecordType: recordType, ID: id})
	if err != nil {
		t.Fatalf("marshalling the tool's own argument shape: %v", err)
	}
	return raw
}

// A type the ROUTED executor does not archive is refused at staging, even
// though the native provider archives it.
//
// This is the whole of the overlay defect: archivableRecordTypes names what
// NATIVE archives, an overlay installation archives three of those six, and a
// staging that read the native list admitted `project` — so a human answered a
// question whose write was always going to refuse.
func TestStagingRefusesATypeTheRoutedExecutorDoesNotArchive(t *testing.T) {
	provider := &narrowArchiver{types: threeTypes()}
	tool := archiveRecord{p: provider}

	_, err := tool.StageInfo(context.Background(), archiveArgsJSON(t, "project", ids.NewV7()))

	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("staging a project against an executor that archives %v answered %v, want the "+
			"bad-arguments refusal — an approval for it could never be carried out", threeTypes(), err)
	}
	// One conjunct, not two. Guarding this on the message ALSO naming
	// "project" makes it vacuous in the direction it exists for: a refusal
	// that stopped naming both would fail the first half and pass the test
	// having asserted nothing.
	if !strings.Contains(err.Error(), "person") {
		t.Errorf("the refusal %q must name what this executor DOES archive: a model told only that its "+
			"call was wrong retries the same call", err)
	}
}

// The same type stages fine where the routed executor does archive it — the
// refusal above is about the executor, not about a list this package holds.
func TestStagingAdmitsATypeTheRoutedExecutorArchives(t *testing.T) {
	provider := &narrowArchiver{types: threeTypes()}
	tool := archiveRecord{p: provider}

	if _, err := tool.StageInfo(context.Background(), archiveArgsJSON(t, "person", ids.NewV7())); err != nil {
		t.Fatalf("staging a person against an executor that archives it answered %v, want it staged", err)
	}
}

// A provider that cannot carry an approved version is refused at STAGING, not
// after a human has answered.
//
// This is a deliberate behaviour choice for a fork whose adapter implements
// only the frozen v1 seam, and the alternative is worth naming: staging could
// admit and the write could run unpinned, which is what happened before this
// seam existed. It would also mean a human approving "archive this record at
// version 4" and the write landing on whatever the record became. Since
// archive_record is statically TierConfirmationRequired, every such archive
// carries a released pin — so admitting here would refuse EVERY archive on
// such an installation after the human answered, which is the defect this
// whole change is about, one layer along.
func TestStagingRefusesAProviderThatCannotCarryAnApprovedVersion(t *testing.T) {
	provider := &v1Archiver{}
	tool := archiveRecord{p: provider}

	_, err := tool.StageInfo(context.Background(), archiveArgsJSON(t, "person", ids.NewV7()))

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("staging a person against a v1-only provider answered %v, want the unsupported-by-SoR "+
			"refusal — the refusal must arrive before a human is asked, not after", err)
	}
	if provider.archived != nil {
		t.Error("the staging path performed the archive")
	}
}

// The type check still runs for a v1 provider, and still answers about the
// TYPE rather than about the seam version.
//
// Both refusals are BadArgs-vs-sentinel distinct, and a caller acts on them
// differently: one is a call to reword, the other an installation that cannot
// serve this verb at all.
func TestAV1ProviderStillRefusesAnUnarchivableTypeByName(t *testing.T) {
	tool := archiveRecord{p: &v1Archiver{}}

	_, err := tool.StageInfo(context.Background(), archiveArgsJSON(t, "tag", ids.NewV7()))

	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("staging a tag answered %v, want the bad-arguments refusal naming the archivable set", err)
	}
}

// Guards asks the executor for its OWN refusals, so an archive the store would
// refuse never reaches a human.
//
// The refusal here is ErrPermissionDenied because that is what the write
// probes answer: every archive store requires WRITE authority over the row,
// while staging refused only an unreadable one. A rep who may read a
// colleague's record passed the old guard and was refused after a human had
// already spent the approval.
func TestGuardsRefuseWhatTheExecutorsOwnProbesWouldRefuse(t *testing.T) {
	provider := &narrowArchiver{types: threeTypes(), refuse: apperrors.ErrPermissionDenied}
	call := NewArchiveCall(provider, ArchiveCommand{RecordType: "person", ID: ids.NewV7()})

	if err := call.Guards(context.Background()); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("guarding a readable-but-unwritable person answered %v, want the executor's own "+
			"refusal — otherwise the approval is spent on a call the store then refuses", err)
	}
}

// The system-of-record refusal still wins, and the order is the assertion.
//
// A record held elsewhere has no local authority to probe, so asking the
// executor about it would be a question with no true answer — and hoisting the
// probe ahead of refuseStagingElsewhere would replace a deliberate
// unsupported-by-SoR refusal with whatever the probe happened to say.
func TestTheHeldElsewhereRefusalStillWinsOverTheExecutorProbe(t *testing.T) {
	// BOTH refusals are armed, and they answer DIFFERENT sentinels. That is the
	// whole design of this case. A version using a stub whose executor probe
	// answers nil, or one answering the same unsupported-by-SoR sentinel, passes
	// with the two arms swapped — measured: inverting the order in Guards left
	// the earlier version of this test green, because only one arm could speak.
	provider := &narrowArchiver{
		types:         threeTypes(),
		heldElsewhere: true,
		refuse:        apperrors.ErrPermissionDenied,
	}
	call := NewArchiveCall(provider, ArchiveCommand{RecordType: "person", ID: ids.NewV7()})

	err := call.Guards(context.Background())

	if errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("the executor probe answered first (%v): a record held in another system of record "+
			"has no local authority to ask about, so probing it is a question with no true answer — "+
			"and the deliberate unsupported-by-SoR refusal is replaced by whatever the probe says", err)
	}
	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("guarding a mirrored person answered %v, want the unsupported-by-SoR refusal to keep "+
			"its place ahead of the executor probe", err)
	}
}

// A released approval's version rides INTO the archive.
//
// Redemption verifies the pin and commits; the archive then runs in a later
// transaction. Without the version travelling with it, a concurrent update in
// that window leaves the archive landing on a record nobody approved — and
// nothing anywhere reports that it did.
func TestAnApprovedArchiveCarriesTheApprovedVersionIntoTheWrite(t *testing.T) {
	provider := &narrowArchiver{types: threeTypes()}
	tool := archiveRecord{p: provider}
	ctx := withApprovalRedeemed(context.Background(), 4, true)

	if _, err := tool.Handle(ctx, archiveArgsJSON(t, "person", ids.NewV7())); err != nil {
		t.Fatalf("archiving under a released approval answered %v, want the write to run", err)
	}
	if provider.archivedAt == nil {
		t.Fatal("the archive never reached the executor")
	}
	if provider.archivedAt.IfVersion == nil {
		t.Fatal("the archive carried no version — the approval was granted against version 4 and the " +
			"write conditioned on nothing, so a concurrent update lands the archive on a version " +
			"nobody approved")
	}
	if *provider.archivedAt.IfVersion != 4 {
		t.Errorf("the archive carried version %d, want 4 — the pin must be the one the approval was "+
			"granted against, not a version read since", *provider.archivedAt.IfVersion)
	}
}

// An ordinary unapproved archive carries no pin, and that is the row lock's
// case rather than a missing guard.
func TestAnUnapprovedArchiveCarriesNoVersion(t *testing.T) {
	provider := &narrowArchiver{types: threeTypes()}
	tool := archiveRecord{p: provider}

	if _, err := tool.Handle(context.Background(), archiveArgsJSON(t, "person", ids.NewV7())); err != nil {
		t.Fatalf("archiving without an approval answered %v, want the write to run", err)
	}
	if provider.archivedAt == nil {
		t.Fatal("the archive never reached the executor")
	}
	if provider.archivedAt.IfVersion != nil {
		t.Errorf("an unapproved archive carried version %d — there was nothing to pin, and inventing one "+
			"turns an ordinary write into a skew failure", *provider.archivedAt.IfVersion)
	}
}

// A provider that cannot honour a pin is REFUSED rather than handed the
// unconditioned verb.
//
// Falling back there would spend the approval on precisely the write it was
// granted against something else — quietly, and only on the installations
// whose adapter happens not to answer the newer seam.
func TestAnApprovedArchiveIsRefusedByAProviderThatCannotPinIt(t *testing.T) {
	provider := &v1Archiver{}
	tool := archiveRecord{p: provider}
	ctx := withApprovalRedeemed(context.Background(), 4, true)

	_, err := tool.Handle(ctx, archiveArgsJSON(t, "person", ids.NewV7()))

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("archiving under a released approval against a v1 provider answered %v, want the "+
			"unsupported-by-SoR refusal", err)
	}
	if provider.archived != nil {
		t.Error("the unconditioned archive ran anyway — an approval granted against a version must not " +
			"be carried out by a write that ignores it")
	}
}
