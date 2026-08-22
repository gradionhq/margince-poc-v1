// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The fourteen single-purpose resolvers' own answers (commandcomms.go,
// commandlinked.go, commandlifecycle.go, commandrecord.go, commandauto.go).
//
// The TOOL door's own specs are the proof the move preserved behaviour — they
// were written against StageInfo and pass unchanged now that StageInfo
// delegates. What is pinned HERE is what only the resolver can answer: that
// each refusal a tool used to make is reachable through the command a REST
// request also builds, that a command's questions rest on ONE reading of the
// record, and — for enrich and merge, the two commands where the seam has to
// carry meaning rather than shape — that the meaning survives the crossing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// tallyingProvider serves the records it was given, by ref, and counts the
// reads — so a resolver that asks twice for the row both its questions rest on
// fails here. countingProvider (command_test.go) answers every ref with one
// canned record, which cannot express a merge's two halves or a missing row.
type tallyingProvider struct {
	datasource.SystemOfRecordProvider
	archivesWhatNativeDoes
	records map[datasource.EntityRef]datasource.Record
	reads   int
}

func (p *tallyingProvider) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	p.reads++
	rec, ok := p.records[ref]
	if !ok {
		return datasource.Record{}, apperrors.ErrNotFound
	}
	return rec, nil
}

// oneRecord builds a tallying provider holding a single authoritative row.
func oneRecord(entity datasource.EntityType, id ids.UUID, fields string, version int64) *tallyingProvider {
	ref := datasource.EntityRef{Type: entity, ID: id}
	return &tallyingProvider{records: map[datasource.EntityRef]datasource.Record{
		ref: nativeRecord(datasource.Record{Ref: ref, Fields: json.RawMessage(fields), Version: version}),
	}}
}

// channelProviderSet answers the send_message resolver's kind question from a
// fixed vocabulary — the shape compose's own adapter has, without the store
// behind it.
type channelProviderSet map[string]bool

func (s channelProviderSet) IsChannelKind(kind string) bool { return kind == "message" }

// CanSendOnProvider is the set's real question since the kind stopped naming a
// transport: the map now holds PROVIDERS this installation can reply on.
func (s channelProviderSet) CanSendOnProvider(provider string) bool { return s[provider] }

// Every command that pins a version answers both of its questions from ONE
// reading of the record. Two readings are two moments, and the authority
// judgment would then be about a different state of the row than the sentence
// a human reads and the version the approval pins.
func TestEachAnchoredCommandReadsItsRecordOnce(t *testing.T) {
	id, stage := ids.NewV7(), ids.NewV7()
	cases := []struct {
		name     string
		provider *tallyingProvider
		call     func(*tallyingProvider) GovernedCall
	}{
		{
			"send_email",
			oneRecord(datasource.EntityActivity, id, `{"kind":"email"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewSendEmailCall(p, SendEmailCommand{ActivityID: id, To: []string{"a@example.test"}})
			},
		},
		{
			"send_message",
			oneRecord(datasource.EntityActivity, id, `{"kind":"message","channel_provider":"telegram"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewSendMessageCall(p, channelProviderSet{"telegram": true},
					SendMessageCommand{ActivityID: id, Body: "hello"})
			},
		},
		{
			"promote_lead",
			oneRecord(datasource.EntityLead, id, `{"full_name":"Ada"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewPromoteLeadCall(p, PromoteLeadCommand{LeadID: id, Trigger: "inbound_reply"})
			},
		},
		{
			"disqualify_lead",
			oneRecord(datasource.EntityLead, id, `{"full_name":"Ada"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewDisqualifyLeadCall(p, DisqualifyLeadCommand{LeadID: id})
			},
		},
		{
			"advance_project_phase",
			oneRecord(datasource.EntityProject, id, `{"name":"Rollout"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewAdvanceProjectPhaseCall(p, AdvanceProjectPhaseCommand{ProjectID: id, ToPhase: "pursuing"})
			},
		},
		{
			"advance_deal",
			oneRecord(datasource.EntityDeal, id, `{"name":"Acme","stage_id":"`+stage.String()+`"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewAdvanceDealCall(p, fixedStages{semantic: stageSemanticOpen},
					AdvanceDealCommand{DealID: id, ToStageID: stage})
			},
		},
		{
			"enrich",
			oneRecord(datasource.EntityOrganization, id, `{"name":"Acme"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewEnrichCall(p, EnrichCommand{OrganizationID: id, Depth: EnrichDepthPage})
			},
		},
		{
			"draft_email",
			oneRecord(datasource.EntityActivity, id, `{"kind":"email"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewDraftEmailCall(p, DraftEmailCommand{ActivityID: id})
			},
		},
		{
			"relink_activity",
			oneRecord(datasource.EntityActivity, id, `{"kind":"email"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewRelinkActivityCall(p, RelinkActivityCommand{
					ActivityID: id, EntityType: "deal", EntityID: ids.NewV7(),
				})
			},
		},
		{
			// A batch card binds to the DESTINATION: the one record the decision
			// is about, read once for the refusal and the pin alike.
			"relink_activities",
			oneRecord(datasource.EntityProject, id, `{"name":"Rollout"}`, 3),
			func(p *tallyingProvider) GovernedCall {
				return NewRelinkActivitiesCall(p, RelinkActivitiesCommand{
					ActivityIDs: []ids.UUID{ids.NewV7(), ids.NewV7()}, EntityType: "project", EntityID: id,
				})
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := StageSubject(context.Background(), c.call(c.provider))
			if err != nil {
				t.Fatalf("staging a readable, authoritative record answered %v", err)
			}
			if c.provider.reads != 1 {
				t.Errorf("read the record %d times, want 1 — Guards and Subject must rest on one moment, "+
					"or the approval pins a version the refusal never judged", c.provider.reads)
			}
			if info.TargetVersion == nil || *info.TargetVersion != 3 {
				t.Errorf("TargetVersion = %v, want the version the read returned", info.TargetVersion)
			}
		})
	}
}

// Every refusal these commands moved off the tool door, asked through the
// COMMAND — which is the door-agnostic form a REST request now reaches too.
// Each is a call the executor would refuse anyway, so reaching it from there
// instead would spend a human's one-shot approval on the way past.
func TestTheSinglePurposeGuardsRefuseWhatExecutionWouldRefuse(t *testing.T) {
	id, other := ids.NewV7(), ids.NewV7()
	link := RecordLink{EntityType: "organization", EntityID: other}
	cases := []struct {
		name  string
		call  GovernedCall
		names string
	}{
		{
			"a mail send with no addressee",
			NewSendEmailCall(oneRecord(datasource.EntityActivity, id, `{}`, 1), SendEmailCommand{ActivityID: id}),
			"`to`",
		},
		{
			"a channel reply with a whitespace-only body",
			NewSendMessageCall(oneRecord(datasource.EntityActivity, id, `{"kind":"message","channel_provider":"telegram"}`, 1),
				channelProviderSet{"telegram": true}, SendMessageCommand{ActivityID: id, Body: "   "}),
			"empty or whitespace-only",
		},
		{
			"a channel reply on an anchor that is not a channel conversation",
			NewSendMessageCall(oneRecord(datasource.EntityActivity, id, `{"kind":"email"}`, 1),
				channelProviderSet{"telegram": true}, SendMessageCommand{ActivityID: id, Body: "hello"}),
			"not a messaging-channel conversation",
		},
		{
			"an account-started send with no addressee",
			NewSendAccountEmailCall(oneRecord(datasource.EntityOrganization, other, `{}`, 1),
				SendAccountEmailCommand{Links: []RecordLink{link}}),
			"`to`",
		},
		{
			"an account-started send filed under nothing",
			NewSendAccountEmailCall(oneRecord(datasource.EntityOrganization, other, `{}`, 1),
				SendAccountEmailCommand{To: []string{"a@example.test"}}),
			"`links`",
		},
		{
			"a booking whose end does not follow its start",
			NewBookMeetingCall(oneRecord(datasource.EntityOrganization, other, `{}`, 1), BookMeetingCommand{
				Start: time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC),
				End:   time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
				Links: []RecordLink{link},
			}),
			"does not follow",
		},
		{
			"a booking attached to nothing",
			NewBookMeetingCall(oneRecord(datasource.EntityOrganization, other, `{}`, 1), BookMeetingCommand{
				Start: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
				End:   time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC),
			}),
			"`links`",
		},
		{
			"a booking over the per-call link cap",
			NewBookMeetingCall(oneRecord(datasource.EntityOrganization, other, `{}`, 1), BookMeetingCommand{
				Start: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
				End:   time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC),
				Links: distinctLinks(maxRecordLinks + 1),
			}),
			"at most",
		},
		{
			"a promotion on a trigger that is not genuine engagement",
			NewPromoteLeadCall(oneRecord(datasource.EntityLead, id, `{}`, 1),
				PromoteLeadCommand{LeadID: id, Trigger: "cold_outbound_no_reply"}),
			"not genuine engagement",
		},
		{
			"a phase outside the ladder",
			NewAdvanceProjectPhaseCall(oneRecord(datasource.EntityProject, id, `{}`, 1),
				AdvanceProjectPhaseCommand{ProjectID: id, ToPhase: "shipped"}),
			"is not a project phase",
		},
		{
			"a closure with no reason",
			NewAdvanceProjectPhaseCall(oneRecord(datasource.EntityProject, id, `{}`, 1),
				AdvanceProjectPhaseCommand{ProjectID: id, ToPhase: projectPhaseClosed}),
			"reason is required",
		},
		{
			"a merge of a record into itself",
			NewMergeCall(oneRecord(datasource.EntityPerson, id, `{}`, 1),
				MergeCommand{RecordType: "person", SourceID: id, TargetID: id}),
			"must differ",
		},
		{
			"a merge of a type with no merge verb",
			NewMergeCall(oneRecord(datasource.EntityDeal, id, `{}`, 1),
				MergeCommand{RecordType: "deal", SourceID: id, TargetID: other}),
			"cannot be merged",
		},
		{
			"an enrich of a target that is not an absolute http(s) URL",
			NewEnrichCall(oneRecord(datasource.EntityOrganization, id, `{}`, 1),
				EnrichCommand{OrganizationID: id, URL: "acme.test/about", Depth: EnrichDepthPage}),
			"absolute http(s) URL",
		},
		{
			"a relink onto a type that is not a link target",
			NewRelinkActivityCall(oneRecord(datasource.EntityActivity, id, `{}`, 1),
				RelinkActivityCommand{ActivityID: id, EntityType: "invoice", EntityID: other}),
			"is not a link target",
		},
		{
			"a named-set relink with no ids",
			NewRelinkActivitiesCall(oneRecord(datasource.EntityProject, other, `{}`, 1),
				RelinkActivitiesCommand{EntityType: "project", EntityID: other}),
			"between 1 and 500",
		},
		{
			// The thread form never stages: a thread key is re-read at
			// redemption, so the rows a human released would not be the rows
			// moved. The refusal sends the caller to relink_activities.
			"a thread relink onto a destination that needs a human",
			NewRelinkThreadCall(oneRecord(datasource.EntityProject, other, `{}`, 1),
				RelinkThreadCommand{ThreadKey: "thread:x", EntityType: "project", EntityID: other}),
			"relink_activities",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := StageSubject(context.Background(), c.call)
			if err == nil {
				t.Fatal("staged a call the executor refuses; a human's yes would be spent discovering that")
			}
			var bad *BadArgsError
			if !errors.As(err, &bad) {
				t.Fatalf("err = %v (%T), want a BadArgsError — a refusal the caller can act on", err, err)
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Errorf("err = %q, want it to name %q", err, c.names)
			}
		})
	}
}

// distinctLinks builds n links to distinct organizations, so only the CAP can
// refuse them — a repeated id would be deduplicated instead and prove nothing.
func distinctLinks(n int) []RecordLink {
	links := make([]RecordLink, 0, n)
	for range n {
		links = append(links, RecordLink{EntityType: "organization", EntityID: ids.NewV7()})
	}
	return links
}

// A merge names two records and the approval belongs on the SURVIVOR: the
// human's yes is a judgment about merging into B as it is now, so B's version
// is what redemption re-checks. Naming the archived half instead would pin a
// row that is about to stop existing and describe the merge backwards.
func TestAMergeStagesTheSurvivorAndNamesBothHalves(t *testing.T) {
	source, survivor := ids.NewV7(), ids.NewV7()
	sourceRef := datasource.EntityRef{Type: datasource.EntityPerson, ID: source}
	survivorRef := datasource.EntityRef{Type: datasource.EntityPerson, ID: survivor}
	p := &tallyingProvider{records: map[datasource.EntityRef]datasource.Record{
		sourceRef:   nativeRecord(datasource.Record{Ref: sourceRef, Fields: json.RawMessage(`{"full_name":"Ada Old"}`), Version: 2}),
		survivorRef: nativeRecord(datasource.Record{Ref: survivorRef, Fields: json.RawMessage(`{"full_name":"Ada New"}`), Version: 7}),
	}}

	info, err := StageSubject(context.Background(), NewMergeCall(p, MergeCommand{
		RecordType: "person", SourceID: source, TargetID: survivor,
	}))
	if err != nil {
		t.Fatalf("staging a well-formed merge answered %v", err)
	}

	if info.TargetID != survivor {
		t.Errorf("staged target %s, want the SURVIVOR %s — an approval bound to the archived half pins a "+
			"row the merge is about to retire", info.TargetID, survivor)
	}
	if info.TargetVersion == nil || *info.TargetVersion != 7 {
		t.Errorf("TargetVersion = %v, want the survivor's 7", info.TargetVersion)
	}
	if !strings.Contains(info.Summary, "Ada Old") || !strings.Contains(info.Summary, "Ada New") {
		t.Errorf("summary %q names one half of the merge; a human reading it must see what disappears "+
			"as well as what survives", info.Summary)
	}
	// Both halves, so the refusal below has something to refuse ON.
	if p.reads != 2 {
		t.Errorf("read %d records, want both halves of the merge", p.reads)
	}
}

// Both halves carry the refusal, not only the pinned one: the merge archives
// and relinks the source, so an externally-held source under a
// locally-authoritative survivor is still a change no approval could release.
func TestAMergeRefusesEitherHalfHeldElsewhere(t *testing.T) {
	local, external := ids.NewV7(), ids.NewV7()
	localRef := datasource.EntityRef{Type: datasource.EntityPerson, ID: local}
	externalRef := datasource.EntityRef{Type: datasource.EntityPerson, ID: external}
	// The external half is deliberately unstamped: its authority lives away.
	records := map[datasource.EntityRef]datasource.Record{
		localRef:    nativeRecord(datasource.Record{Ref: localRef, Fields: json.RawMessage(`{}`)}),
		externalRef: {Ref: externalRef, Fields: json.RawMessage(`{}`)},
	}
	for _, c := range []struct {
		name           string
		source, target ids.UUID
		names          string
	}{
		{"the survivor is held elsewhere", local, external, "merged INTO"},
		{"the source is held elsewhere", external, local, "merged FROM"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := StageSubject(context.Background(), NewMergeCall(
				&tallyingProvider{records: records},
				MergeCommand{RecordType: "person", SourceID: c.source, TargetID: c.target}))
			if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
				t.Fatalf("err = %v, want ErrUnsupportedBySoR", err)
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Errorf("err = %q, want it to name which half blocked the merge", err)
			}
		})
	}
}

// enrich is ONE verb behind TWO contract operations, and the depth is the
// whole of the difference: a page read and a whole-site crawl. The commands
// must differ structurally in it, and the sentence a human approves must say
// which — approving a site crawl off a line that reads "page" is the failure
// this typing exists to make unreachable.
func TestTheTwoEnrichDepthsAreDistinctCommandsWithDistinctSummaries(t *testing.T) {
	org := ids.NewV7()
	page := EnrichCommand{OrganizationID: org, Depth: EnrichDepthPage}
	site := EnrichCommand{OrganizationID: org, Depth: EnrichDepthSite}

	if page == site {
		t.Fatal("the two enrich operations build the same command; one of them would then be described " +
			"and guarded as the other")
	}

	summaries := map[EnrichDepth]string{}
	for _, cmd := range []EnrichCommand{page, site} {
		info, err := StageSubject(context.Background(),
			NewEnrichCall(oneRecord(datasource.EntityOrganization, org, `{"name":"Acme"}`, 1), cmd))
		if err != nil {
			t.Fatalf("staging depth %q answered %v", cmd.Depth, err)
		}
		summaries[cmd.Depth] = info.Summary
	}

	if summaries[EnrichDepthPage] == summaries[EnrichDepthSite] {
		t.Fatalf("both depths stage the sentence %q — a human approving a whole-site crawl would read the "+
			"same line as one approving a single page", summaries[EnrichDepthPage])
	}
	for depth, summary := range summaries {
		if !strings.Contains(summary, string(depth)) {
			t.Errorf("the %q summary is %q, which does not say which read it is", depth, summary)
		}
	}
}

// The two commands that name no record at all say so with an EMPTY target
// rather than a zero id under a record type. stagedTarget (compose) refuses a
// concrete id with no type, and the approvals surface probes a target's row
// scope by its type — so a type with no id is the shape a create stages, and
// neither is what a report run is.
func TestTheCommandsThatNameNoRowStageNoTarget(t *testing.T) {
	report, err := StageSubject(context.Background(), NewRunReportCall(RunReportCommand{Report: "deals-by-stage"}))
	if err != nil {
		t.Fatalf("staging a report run answered %v", err)
	}
	if report.TargetType != "" || !report.TargetID.IsZero() {
		t.Errorf("a report run staged target (%q,%s); a report is an aggregate over rows the caller's own "+
			"scope already bounds, and names no row an approval could bind to",
			report.TargetType, report.TargetID)
	}
	if !strings.Contains(report.Summary, "deals-by-stage") {
		t.Errorf("summary %q does not name which report is being released", report.Summary)
	}

	logged, err := StageSubject(context.Background(),
		NewLogActivityCall(LogActivityCommand{Fields: json.RawMessage(`{"kind":"note","body":"hi"}`)}))
	if err != nil {
		t.Fatalf("staging a logged activity answered %v", err)
	}
	if logged.TargetType != string(datasource.EntityActivity) {
		t.Errorf("TargetType = %q, want %q", logged.TargetType, datasource.EntityActivity)
	}
	if !logged.TargetID.IsZero() {
		t.Errorf("TargetID = %s; the activity does not exist yet, and naming one makes the approvals "+
			"surface probe and pin a record that is not there", logged.TargetID)
	}
}

// A resolver asked about a second call reads THAT call.
//
// Nothing outside this package can reach a resolver — the New…Call
// constructors bind one to its command and hand back the call — so this
// reaches past the constructor deliberately, the same way
// TestAResolverAskedAboutASecondTargetReadsIt does for the archive: the memo's
// key is what makes the binding a belt rather than the only thing between two
// callers and a shared read. The two memos this task adds carry their own keys
// for that reason, and this is what holds them to it.
func TestASecondCallOnTheSameResolverIsReadAfresh(t *testing.T) {
	t.Run("a merge's two halves", func(t *testing.T) {
		p := &tallyingProvider{records: map[datasource.EntityRef]datasource.Record{}}
		resolver := &mergeResolver{records: p}
		ctx := context.Background()

		first, second := mergeFixture(p), mergeFixture(p)
		if _, err := resolver.Subject(ctx, first); err != nil {
			t.Fatalf("the first subject answered %v", err)
		}
		got, err := resolver.Subject(ctx, second)
		if err != nil {
			t.Fatalf("the second subject answered %v", err)
		}

		if p.reads != 4 {
			t.Errorf("two merges cost %d reads, want 4 — a remembered pair must not answer for a merge it "+
				"was not read for", p.reads)
		}
		if got.TargetID != second.TargetID {
			t.Errorf("the second subject named %s, want the second merge's own survivor %s",
				got.TargetID, second.TargetID)
		}
	})

	t.Run("a call's named links", func(t *testing.T) {
		p := &tallyingProvider{records: map[datasource.EntityRef]datasource.Record{}}
		links := &namedLinks{records: p}
		ctx := context.Background()

		first := []RecordLink{{EntityType: string(datasource.EntityOrganization), EntityID: linkFixture(p)}}
		second := []RecordLink{{EntityType: string(datasource.EntityOrganization), EntityID: linkFixture(p)}}
		if _, _, err := links.stageable(ctx, first); err != nil {
			t.Fatalf("the first read answered %v", err)
		}
		got, _, err := links.stageable(ctx, second)
		if err != nil {
			t.Fatalf("the second read answered %v", err)
		}

		if p.reads != 2 {
			t.Errorf("two link lists cost %d reads, want 2 — a remembered list must not answer for links it "+
				"was not read for", p.reads)
		}
		if len(got) != 1 || got[0] != second[0] {
			t.Errorf("the second read answered %v, want the second list's own link %v", got, second)
		}
	})
}

// mergeFixture adds an authoritative source and survivor to p and returns the
// merge that names them.
func mergeFixture(p *tallyingProvider) MergeCommand {
	source, survivor := ids.NewV7(), ids.NewV7()
	for _, id := range []ids.UUID{source, survivor} {
		ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: id}
		p.records[ref] = nativeRecord(datasource.Record{Ref: ref, Fields: json.RawMessage(`{}`)})
	}
	return MergeCommand{RecordType: "person", SourceID: source, TargetID: survivor}
}

// linkFixture adds one authoritative organization to p and returns its id.
func linkFixture(p *tallyingProvider) ids.UUID {
	id := ids.NewV7()
	ref := datasource.EntityRef{Type: datasource.EntityOrganization, ID: id}
	p.records[ref] = nativeRecord(datasource.Record{Ref: ref, Fields: json.RawMessage(`{}`)})
	return id
}

// The archive TOOL refuses a record type its own write path cannot express,
// before any approval is minted.
//
// The seam's resolver deliberately stands down for such a type — six of the
// twelve archivable types have no seam row and are archived by their own
// module, which is a real REST archive (archiveResolver.Guards, command.go).
// That makes the question this door's alone, exactly as create_record's
// ServesRecordType refusal is: this verb archives ONLY through
// datasource.SystemOfRecordProvider.Archive, and the surface does not enforce
// the InputSchema enum, so a raw tools/call can name anything at all.
//
// Both halves matter. A type the provider knows nothing about stages a target
// the approvals surface has no visibility rule for — an authority object that
// is invisible, undecidable and minted at the caller's choosing. A type it
// half-knows stages one a human CAN release, onto a retry that then dies at
// the provider with the one-shot authority already spent.
func TestTheArchiveToolRefusesARecordTypeItsOwnWritePathCannotArchive(t *testing.T) {
	// `lead` is the one that looks safe and is not: a real seam entity, so the
	// old check (the seam's whole vocabulary) admitted it, while Archive has no
	// branch for it and never had. Exactly the trap `activity` sat in.
	for _, recordType := range []string{"tag", "list", "saved_view", "lead", "nonsense", ""} {
		t.Run(recordType, func(t *testing.T) {
			// A provider that fails EVERY read, so a door that reached the seam
			// anyway fails here rather than passing on a lenient stub.
			_, err := archiveRecord{p: unreadableProvider{}}.StageInfo(context.Background(),
				json.RawMessage(fmt.Sprintf(`{"record_type":%q,"id":%q}`, recordType, ids.NewV7())))
			var bad *BadArgsError
			if !errors.As(err, &bad) {
				t.Fatalf("staging an archive of %q answered %v, want a BadArgsError — an approval for it "+
					"could never be carried out", recordType, err)
			}
		})
	}
}

// The other half of the same rule: a type the seam DOES archive must reach
// staging, or the tool refuses work the contract promises.
//
// `activity` is the case this was written for. crm.yaml has always declared
// archiveActivity as archive_record's (x-mcp-tool at DELETE /v1/activities/{id}),
// but SystemOfRecordProvider.Archive had no branch for it: the tool staged the
// confirmation, a human approved, and the retry died at the provider with
// `unsupported_entity_type` — a one-shot authority spent on a call that could
// never run, and the activity had to be removed over REST instead.
func TestTheArchiveToolStagesEveryRecordTypeItsWritePathServes(t *testing.T) {
	// Named here rather than ranged over archivableRecordTypes: a test that
	// iterates the list it is asserting on cannot notice the list shrinking,
	// which is the regression it exists to catch.
	for _, recordType := range []string{
		"person", "organization", "deal", "project", "relationship", "activity",
	} {
		t.Run(recordType, func(t *testing.T) {
			id := ids.NewV7()
			info, err := archiveRecord{p: oneRecord(datasource.EntityType(recordType), id, `{}`, 3)}.
				StageInfo(context.Background(),
					json.RawMessage(fmt.Sprintf(`{"record_type":%q,"id":%q}`, recordType, id)))
			if err != nil {
				t.Fatalf("staging an archive of %q answered %v, want it staged — the contract "+
					"declares this type archivable over the tool surface", recordType, err)
			}
			// The approval names the record the human is deciding about.
			if info.TargetType != recordType || info.TargetID != id {
				t.Errorf("staged %s/%s, want %s/%s", info.TargetType, info.TargetID, recordType, id)
			}
		})
	}
}

// Booking onto another host's calendar is the admin's alone, and the staging
// door asks before the store does — a management or ops passport reads every
// calendar and would otherwise spend a human's approval on a booking the
// store can only refuse.
func TestBookingAnotherHostStagesOnlyForAnAdmin(t *testing.T) {
	self, host, org := ids.NewV7(), ids.NewV7(), ids.NewV7()
	window := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	call := func(hostID *ids.UUID) GovernedCall {
		return NewBookMeetingCall(oneRecord(datasource.EntityOrganization, org, `{}`, 1), BookMeetingCommand{
			HostUserID: hostID, Start: window, End: window.Add(30 * time.Minute),
			Links: []RecordLink{{EntityType: "organization", EntityID: org}},
		})
	}
	as := func(roles []string, scope principal.RowScope) context.Context {
		return principal.WithActor(context.Background(), principal.Principal{
			Type: principal.PrincipalHuman, ID: "human:" + self.String(), UserID: self,
			Permissions: principal.Permissions{RoleKeys: roles, RowScope: scope},
		})
	}
	cases := []struct {
		name   string
		ctx    context.Context
		host   *ids.UUID
		denied bool
	}{
		{"own calendar, any role", as([]string{"management"}, principal.RowScopeAll), &self, false},
		{"no host named, any role", as([]string{"rep"}, principal.RowScopeTeam), nil, false},
		{"another host, admin", as([]string{"admin"}, principal.RowScopeAll), &host, false},
		{"another host, management (unbounded, not admin)", as([]string{"management"}, principal.RowScopeAll), &host, true},
		{"another host, ops (unbounded, not admin)", as([]string{"ops"}, principal.RowScopeAll), &host, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := StageSubject(c.ctx, call(c.host))
			if got := errors.Is(err, apperrors.ErrPermissionDenied); got != c.denied {
				t.Fatalf("staging answered %v; want permission denied = %v", err, c.denied)
			}
		})
	}
}

// A decision names no target record, and the reason is not that it has none: it
// acts on the approval, which is the authority object itself. A staged row
// pointing at one would be an authority object pointing at an authority object,
// with nothing to row-scope or version-pin it by.
//
// Both verdicts, and both subjects, because the verdict is read off the
// OPERATION rather than the body — a table lookup that cannot fail to compile if
// it is wrong, only decide the other way.
func TestADecisionStagesNoTargetAndSaysWhichWayItGoes(t *testing.T) {
	approval, bundle := ids.NewV7(), ids.NewV7()
	cases := []struct {
		name    string
		call    GovernedCall
		wants   string
		mustSay string
	}{
		{
			"approve one", NewDecideApprovalCall(DecideApprovalCommand{ApprovalID: approval, Approve: true}),
			"Approve", approval.String(),
		},
		{
			"reject one", NewDecideApprovalCall(DecideApprovalCommand{ApprovalID: approval}),
			"Reject", approval.String(),
		},
		{
			"approve an act", NewDecideBundleCall(DecideBundleCommand{BundleID: bundle, Approve: true}),
			"Approve", bundle.String(),
		},
		{
			"reject an act", NewDecideBundleCall(DecideBundleCommand{BundleID: bundle}),
			"Reject", bundle.String(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subject, err := StageSubject(context.Background(), tc.call)
			if err != nil {
				t.Fatalf("staging a decision answered %v", err)
			}
			if subject.TargetType != "" || !subject.TargetID.IsZero() {
				t.Errorf("a decision staged target (%q,%s); the row it acts on is the approval itself",
					subject.TargetType, subject.TargetID)
			}
			if !strings.HasPrefix(subject.Summary, tc.wants) {
				t.Errorf("summary %q does not open with the verdict %q — a human reading it in an inbox "+
					"would not know which way they are being asked to go", subject.Summary, tc.wants)
			}
			if !strings.Contains(subject.Summary, tc.mustSay) {
				t.Errorf("summary %q does not say what is being decided", subject.Summary)
			}
			if err := tc.call.Guards(context.Background()); err != nil {
				t.Errorf("a decision's guards refused it: %v — what may be decided is the approvals "+
					"engine's own question, answered against the deciding person", err)
			}
		})
	}
}
