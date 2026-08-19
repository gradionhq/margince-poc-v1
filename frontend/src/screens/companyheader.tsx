import { useQueryClient } from "@tanstack/react-query";
import { Fragment, type ReactElement } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { useCan } from "../app/capability";
import { navigate } from "../app/router";
import { Badge, Button, OverflowMenu } from "../design-system/atoms";
import { InlineChoice } from "../design-system/inlinechoice";
import { ProvenanceTag } from "../design-system/trust";
import { formatDateAbbrev } from "../format/format";
import { useLocale, useT } from "../i18n";
import { ArchiveAction } from "./archive";
import { provenanceOf, throwProblem, useSorMode, useViewerId } from "./common";
import { RECORD_ZONE } from "./company360";
import { DecisionsChip } from "./companyapprovals";
import { ComposeModal } from "./compose";
import { joinMultiselectValue } from "./create";
import { useObjectCustomFields } from "./customfields.form";
import { EditAction } from "./edit";
import { EntityRef, useRoster } from "./entityref";
import { LogActivityAction } from "./logactivity";
import { MergeAction } from "./merge";
import {
  addressFrom,
  companyEditFields,
  LIFECYCLE_LABELS,
  LIFECYCLE_OPTIONS,
  mapOrgUpdate,
  RELATIONSHIP_TYPE_LABELS,
  searchOrgTargets,
} from "./organizations";
import { ShareAction } from "./share";

// The account header: the verbs a rep reaches for, the two values they change
// in place, and the lines of facts that say who the account is and where the
// relationship stands.
//
// Lifecycle reads beside the account's NAME — the target header's own
// arrangement — rather than in the meta line below with everything else the
// account carries: it is the one value a reader looks for first, and both it
// and owner stay editable in place (InlineChoice) rather than moving into an
// edit modal, which is what buried them behind a form the last time this
// header changed shape.
//
// Split out of organizations.tsx because that file had grown past 2,700 lines
// carrying the list screen, the enrichment tools, the evidence cards and this
// at once — and the V2 work adds to every one of them.

type Organization = components["schemas"]["Organization"];
type Organization360View = components["schemas"]["Organization360"];
type Lifecycle = NonNullable<Organization["lifecycle"]>;
type UpdateOrganizationRequest =
  components["schemas"]["UpdateOrganizationRequest"];

// The verbs a rep reaches for on an account, in the header where they can see
// them. They were one button — "Log activity" — and setting what happens NEXT
// was two clicks inside it, behind a type picker, which is why accounts get
// notes and no follow-ups.
//
// "Write email" leads, because it is the account's primary action (plan §4.1):
// a rep opening a company is usually about to start a conversation, not log
// one that happened. It sends through POST /emails, the account-started origin
// — a new thread filed under this company — rather than fabricating an
// activity to reply to.
export function CompanyPrimaryActions({
  org,
  composerOpen,
  onComposerOpen,
}: Readonly<{
  org: Organization;
  // The composer's open state belongs to the PAGE, not to this button: the
  // drawer opens into the right rail's column, so the rail has to know it is
  // open in order to stand down. Held here as a controlled pair rather than
  // privately, which is what kept the rail rendering underneath it.
  composerOpen: boolean;
  onComposerOpen: (open: boolean) => void;
}>) {
  // Archived records take no new activity: the write is refused server-side,
  // so offering the verb would only produce a modal that fails on save.
  if (org.archived_at) {
    return null;
  }
  return (
    <>
      <WriteEmailAction org={org} open={composerOpen} onOpen={onComposerOpen} />
      <LogActivityAction entityType="organization" entityId={org.id} />
      <LogActivityAction
        entityType="organization"
        entityId={org.id}
        initialKind="task"
        triggerLabel="log.addTask"
      />
    </>
  );
}

// WriteEmailAction opens the composer with no anchor. The modal owns the send,
// the consent gate and the refusal vocabulary; this owns only whether the
// surface is offered and the open/close state, so the account-started and
// reply surfaces stay one component.
function WriteEmailAction({
  org,
  open,
  onOpen,
}: Readonly<{
  org: Organization;
  open: boolean;
  onOpen: (open: boolean) => void;
}>) {
  const t = useT();
  return (
    <>
      <Button variant="primary" onClick={() => onOpen(true)}>
        {t("co.writeEmail")}
      </Button>
      {open && (
        // Keyed by the record, so navigating to another company while the
        // composer is open REMOUNTS it rather than re-pointing it. Without the
        // key the form keeps the text written for the previous account while
        // the links payload follows the new one — a message composed for A,
        // filed against B, with nothing on screen saying so.
        <ComposeModal
          key={org.id}
          entityType="organization"
          entityId={org.id}
          open={open}
          onClose={() => onOpen(false)}
        />
      )}
    </>
  );
}

// patchCompanyField sends one field through the ordinary organization PATCH,
// with the record's own version as If-Match. The inline controls share it so a
// lifecycle change and an owner change cannot end up with different conflict,
// refusal or invalidation behaviour.
//
// It throws on failure rather than swallowing: InlineChoice renders what is
// thrown beside the control, and the server's problem detail is a better
// sentence than any this layer could invent.
async function patchCompanyField(
  org: Organization,
  body: UpdateOrganizationRequest,
): Promise<void> {
  const { error } = await api.PATCH("/organizations/{id}", {
    params: { path: { id: org.id }, ...ifMatch(requireVersion(org.version)) },
    body,
  });
  if (error) {
    throwProblem(error);
  }
}

// useCompanyFieldPatch wires one inline header edit to the query cache: the
// record, the list it appears in and the 360 that summarizes it all read the
// value being changed, so all three are refetched rather than left showing the
// old one until something else happens to invalidate them.
//
// Exported so the rail's own Details grid (companyrail.tsx) wires its inline
// edits to the SAME PATCH shape and the SAME three-key invalidation rather
// than keeping a second copy: one inline organization edit and another that
// silently invalidates a different set of caches is the drift this file
// already exists to prevent within its own component.
export function useCompanyFieldPatch(org: Organization) {
  const queryClient = useQueryClient();
  return async (body: UpdateOrganizationRequest) => {
    await patchCompanyField(org, body);
    await queryClient.invalidateQueries({ queryKey: ["organizations"] });
    await queryClient.invalidateQueries({
      queryKey: ["organization360", org.id],
    });
    // The header renders from the SINGLE-record query, and its version is the
    // If-Match the next inline edit sends. Leaving it stale shows the old value
    // after a successful save and makes the following edit fail on a version
    // the server has already moved past.
    await queryClient.invalidateQueries({
      queryKey: ["organization", org.id],
    });
  };
}

// companyReadOnlyReason says why this record cannot be edited, when there is
// something worth saying. Archived first: it is the one a reader can act on
// (restore it), where the overlay case is a property of the installation.
//
// Exported for the same reason as useCompanyFieldPatch above: the rail's
// Details grid gates its own edit affordances on `writable`, and the reason
// an archived or overlay-mirrored account is read-only is a fact about the
// RECORD, not about which component happens to be drawing it.
export function useCompanyReadOnlyReason(
  org: Organization,
): string | undefined {
  const t = useT();
  const overlay = useSorMode() === "overlay";
  if (org.archived_at) {
    return t("record.archivedReadOnly");
  }
  if (overlay) {
    return t("overlay.partialWriteBack");
  }
  return undefined;
}

// Exported for its two mount points: the header passes it into RecordView's
// `nameBadge` slot, where the record's standing belongs on the name's own
// line, and the rail's Details grid mounts the SAME control rather than a
// second InlineChoice with its own PATCH. One implementation of how lifecycle
// is written, two places it is drawn, so the two cannot disagree about what
// they last wrote. `hideLabel` is unconditional: both callers name the field
// themselves, the badge beside the name and the grid's own label column.
export function CompanyLifecycleControl({
  org,
}: Readonly<{ org: Organization }>) {
  const t = useT();
  const canUpdate = useCan("organization", "update");
  const readOnlyReason = useCompanyReadOnlyReason(org);
  const patch = useCompanyFieldPatch(org);
  return (
    <InlineChoice
      label={t("org.lifecycle")}
      // The badge already reads as the account's standing beside its name —
      // a "Lifecycle: " prefix in front of it would be the one value on the
      // line saying its own name twice. `label` still drives the accessible
      // name (aria-label, sr-only form label), so a screen reader hears
      // "Lifecycle" regardless.
      hideLabel
      value={org.lifecycle ?? "unknown"}
      options={LIFECYCLE_OPTIONS.map((value) => ({
        value,
        label: t(LIFECYCLE_LABELS[value]),
      }))}
      canEdit={canUpdate && !readOnlyReason}
      readOnlyReason={readOnlyReason}
      // The account's standing is the one value beside its name a reader
      // looks for first. Tinted rather than filled: it marks the one value
      // here a reader can set, without reading as the page's primary action.
      render={(value) => (
        <Badge tone="accent">{t(LIFECYCLE_LABELS[value as Lifecycle])}</Badge>
      )}
      onSave={(next) =>
        patch({
          lifecycle: next as NonNullable<
            UpdateOrganizationRequest["lifecycle"]
          >,
        })
      }
    />
  );
}

// Exported for the same reason as useCompanyFieldPatch/useCompanyReadOnlyReason
// above: the rail's Details grid edits the SAME field through the SAME
// roster read, the SAME not-in-roster fallback and the SAME
// unowned-only-while-unowned rule, rather than a second picker that could
// silently diverge from any of the three. `hideLabel` lets the rail's own
// FieldRow label column say "Owner" once instead of this control saying it
// again — the header call site omits it and keeps its current prose.
export function CompanyOwnerControl({
  org,
  hideLabel,
}: Readonly<{ org: Organization; hideLabel?: boolean }>) {
  const t = useT();
  const canUpdate = useCan("organization", "update");
  const readOnlyReason = useCompanyReadOnlyReason(org);
  const patch = useCompanyFieldPatch(org);
  const roster = useRoster("user", true);
  const owners = (roster.data ?? []).flatMap((entry) =>
    "display_name" in entry
      ? [{ value: entry.id, label: entry.display_name }]
      : [],
  );
  // The account's current owner may sit outside the roster's one page — a big
  // workspace, a deactivated user — and a select whose current value is not an
  // option renders blank. Naming them keeps the control honest about who owns
  // it today even when it cannot resolve them. Which sentence is honest depends
  // on whether the roster has answered: "no longer in the user list" is a claim
  // about a settled read, and stating it over one still in flight says the
  // owner is gone when all that is known is that nothing has come back yet.
  if (org.owner_id && !owners.some((user) => user.value === org.owner_id)) {
    owners.unshift({
      value: org.owner_id,
      label: roster.isPending ? t("common.loading") : t("co.owner.notInRoster"),
    });
  }
  // "Unowned" is offered only while the account IS unowned. `owner_id` cannot
  // carry "unassign" on the wire — a null is indistinguishable from an omitted
  // field — so offering it on an owned account would take the answer and drop
  // it. Present as the truthful current state, absent as an edit we cannot make.
  const options = org.owner_id
    ? owners
    : [{ value: "", label: t("co.pulse.unowned") }, ...owners];
  return (
    <InlineChoice
      label={t("co.pulse.owner")}
      // The caller names the field: the header's meta line prints "Owner"
      // immediately before this control, and the grid has its own label
      // column. `label` still drives the accessible name either way.
      hideLabel={hideLabel}
      value={org.owner_id ?? ""}
      options={options}
      canEdit={canUpdate && !readOnlyReason}
      readOnlyReason={readOnlyReason}
      // The closed control reads off the SAME labels the open one offers, so
      // the header cannot name the owner one way and the editor another. That
      // is also what keeps the uuid out: reading the owner through the generic
      // record reference painted the raw id for the first moments of every page
      // load, and a uuid is not a weaker name — it is a non-answer spelled so
      // that no reader can use it.
      render={(value) => {
        if (!value) {
          return t("co.pulse.unowned");
        }
        return (
          owners.find((user) => user.value === value)?.label ??
          t("co.owner.notInRoster")
        );
      }}
      onSave={(next) => patch({ owner_id: next })}
    />
  );
}

function CompanyEditAction({
  org,
  overlay,
}: Readonly<{ org: Organization; overlay: boolean }>) {
  const t = useT();
  const cf = useObjectCustomFields("organization");
  const roster = useRoster("user", true);
  // The roster hook serves users and teams alike, so narrow to the entries
  // that actually carry a person's name rather than asserting the shape.
  const owners = (roster.data ?? []).flatMap((entry) =>
    "display_name" in entry
      ? [{ id: entry.id, display_name: entry.display_name }]
      : [],
  );
  // The roster is one page of 200. An owner outside it — a big workspace, a
  // deactivated user — would leave the prefilled select showing a blank it
  // cannot resolve, and since the select is required once an owner is set,
  // saving anything else would then force a reassignment nobody asked for.
  if (org.owner_id && !owners.some((user) => user.id === org.owner_id)) {
    owners.push({ id: org.owner_id, display_name: t("co.owner.notInRoster") });
  }
  return (
    <EditAction
      label={t("record.edit")}
      notice={overlay ? t("overlay.partialWriteBack") : undefined}
      fields={[
        ...companyEditFields(owners, Boolean(org.owner_id), t),
        ...cf.formFields,
      ]}
      record={{
        id: org.id,
        version: org.version,
        display_name: org.display_name,
        owner_id: org.owner_id ?? "",
        legal_name: org.legal_name ?? "",
        industry: org.industry ?? "",
        size_band: org.size_band ?? "",
        // Both stage fields prefill from the live record. relationship_types
        // is a REPLACE-SET: an unseeded multiselect collects as the empty
        // string, which mapOrgUpdate reads as the honest empty set, so saving
        // an unrelated field would clear every type the account has.
        lifecycle: org.lifecycle ?? "",
        relationship_types: joinMultiselectValue(org.relationship_types ?? []),
        linkedin_url: org.linkedin_url ?? "",
        ...addressFrom(org.address),
        // The repeatable domains field prefills from the org's live set;
        // its rows are string-keyed, so the primary flag stringifies to
        // match the "true"/"" the primary radio writes.
        domains: (org.domains ?? []).map((domain) => ({
          domain: domain.domain,
          is_primary: String(domain.is_primary),
        })),
        ...cf.recordSlice(org),
      }}
      update={async (values, rows) => {
        const { data, error } = await api.PATCH("/organizations/{id}", {
          params: {
            path: { id: org.id },
            ...ifMatch(requireVersion(org.version)),
          },
          body: {
            ...mapOrgUpdate(values, rows ?? {}, org.domains),
            ...cf.toBody(values),
          },
        });
        if (error) {
          throwProblem(error);
        }
        return data;
      }}
      invalidate="organizations"
      recordKey="organization"
      resolveExisting={(_code, existingId) => ({
        screen: "companies",
        id: existingId,
      })}
    />
  );
}

export function CompanyActionBadges({
  org,
  view,
  onOpenHistory,
  onSetUpPartner,
  onOpenDecisions,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  onOpenHistory: () => void;
  onSetUpPartner: () => void;
  onOpenDecisions?: () => void;
}>) {
  const t = useT();
  const overlay = useSorMode() === "overlay";
  // An archived record is read-only: the backend rejects edit/merge/archive
  // on a non-live row (there is no unarchive path), so those items would only
  // 404. Its history stays readable — what happened to a record is exactly
  // what a reader wants after it has been put away.
  const writable = !org.archived_at;
  return (
    <>
      {/* What the company IS to us. Where it STANDS is a separate question,
          and it now has a separate control — the editable lifecycle in the
          pulse line — so it is not repeated here as a read-only badge. The two
          were one field once, which is how an account whose contract had ended
          still read as "Prospect". */}
      {(org.relationship_types ?? []).map((relType) => (
        <Badge key={relType} tone="accent">
          {t(RELATIONSHIP_TYPE_LABELS[relType])}
        </Badge>
      ))}
      {org.archived_at && <Badge tone="warn">{t("record.archived")}</Badge>}
      {/* An archived record read from a mirror offers nothing at all: every
          write is refused and the history is a native read the mirror has no
          row for. Rendering the trigger anyway would open an empty popover. */}
      {(writable || !overlay) && (
        <OverflowMenu label={t("record.moreActions")}>
          {writable && <CompanyEditAction org={org} overlay={overlay} />}
          {/* Merge has no incumbent-first projection — the seam refuses it
            outright (overlay/provider_writes.go Merge) — unlike
            edit/archive above, which it serves, so it stays hidden here. */}
          {writable && !overlay && (
            <MergeAction
              label={t("merge.org")}
              sourceId={org.id}
              sourceName={org.display_name}
              searchTargets={searchOrgTargets}
              merge={async (targetId) => {
                const { data, error } = await api.POST(
                  "/organizations/{id}/merge",
                  {
                    params: {
                      path: { id: org.id },
                      ...ifMatch(requireVersion(org.version)),
                    },
                    body: { target_id: targetId },
                  },
                );
                if (error) {
                  throwProblem(error, t);
                }
                return data;
              }}
              invalidate="organizations"
              recordKey="organization"
              survivorRoute={(targetId) => ({
                screen: "companies",
                id: targetId,
              })}
            />
          )}
          {writable && (
            <ArchiveAction
              label={t("record.archive")}
              confirmText={t("record.archiveConfirm")}
              archive={async () => {
                const { data, error } = await api.DELETE(
                  "/organizations/{id}",
                  {
                    params: { path: { id: org.id } },
                  },
                );
                if (error) {
                  throwProblem(error);
                }
                return data;
              }}
              invalidate="organizations"
              recordKey="organization"
              onArchived={() => navigate({ screen: "companies" })}
            />
          )}
          {/* A record grant probes the native row via auth.EnsureLinkTarget,
            which a mirrored record has no row for — sharing stays hidden
            in overlay regardless of record type (see deals.tsx's
            DealBadges). */}
          {writable && !overlay && (
            <ShareAction recordType="organization" recordId={org.id} />
          )}
          {/* The way in to the partner programme for an account that has none.
            The tab only shows once there IS one, so without this the first
            partner row would be unreachable — this is the same form, asked
            for rather than offered. */}
          {writable &&
            !overlay &&
            !(org.relationship_types ?? []).includes("partner") && (
              <Button small onClick={onSetUpPartner}>
                {t("org.partnerSetUp")}
              </Button>
            )}
          {/* The audit spine: who changed this record and when. It reads as an
            inspection of the record rather than part of its story, so it sits
            with the other rare verbs instead of beside the account's own
            timeline. */}
          {!overlay && (
            <Button
              small
              data-testid="company-full-history"
              onClick={onOpenHistory}
            >
              {t("record.fullHistory")}
            </Button>
          )}
          {/* The account's own waiting decisions. It reads as a count in the
              header, which is a state, and this is the verb that answers it —
              so it sits with the other rare verbs rather than as a chip beside
              the account's name. Absent when nothing waits. */}
          {onOpenDecisions && (
            <DecisionsChip view={view} onOpen={onOpenDecisions} />
          )}
        </OverflowMenu>
      )}
    </>
  );
}

// CompanyDescription is the one-line "what this company does" under the
// title — READ-ONLY here (plan §4.1's editable line moved to the rail's
// Details grid, companyraildetails.tsx's DescriptionRow, which is where a
// reader goes to fill fields in). A second editable control on the same
// field, wired to a second PATCH, is the duplicate-control defect the
// lifecycle row was fixed for; this is the same fix one field over. Absent
// entirely rather than shown empty: an unwritten description with no
// pressable to start it here would be a dead end pointing nowhere at the
// field that actually writes it.
export function CompanyDescription({ org }: Readonly<{ org: Organization }>) {
  const value = org.description ?? "";
  // The line is drawn only when it says something the chips below it do not.
  // Nothing written earns no line: the details grid is where that gets
  // filled in now. A description that merely repeats the industry earns no
  // line either — a reader who sees the same words twice reads the second
  // copy as a different fact and looks for the difference. Enrichment writes
  // this field from the same source the industry comes from, so the two
  // matching is the common case, not a freak one.
  const industry = org.industry?.trim().toLowerCase() ?? "";
  if (!value || value.trim().toLowerCase() === industry) {
    return null;
  }
  return <p className="co-description">{value}</p>;
}

// The scheme is noise in a chip: every one of these is https, and "https://"
// costs eight characters of a row that has little space to fit it in. A URL
// we cannot parse is shown whole rather than silently dropped.
export function displayHost(url: string): string {
  try {
    return new URL(url).host.replace(/^www\./, "");
  } catch {
    return url;
  }
}

// `website_url` is derived server-side from the primary domain row, and an
// overlay-mirrored company carries the domain without it. Falling back to
// the row keeps the domain on those records rather than silently dropping
// the one identifying fact the reader had before. Shared by every reader of
// the company's web presence, so the fallback lives in one place rather than
// being re-derived per caller.
function companyWebsite(org: Organization): string | undefined {
  const primaryDomain = (org.domains ?? []).find((d) => d.is_primary)?.domain;
  return (
    org.website_url ?? (primaryDomain ? `https://${primaryDomain}` : undefined)
  );
}

// CompanyIdentityLine is the header's two meta lines, under the name and its
// lifecycle badge (which now sits on the name's own line — see RecordView's
// `nameBadge`). The first names what the account is and who owns it — its
// domain, its industry, its owner — as plain facts rather than pill chips;
// the second, quieter, says when the record was made and when it was last
// exchanged with. This replaces the four-row scatter the header used to draw
// (name; a chip row; a "way in / they wrote / we wrote / agent: X" pulse
// line; lifecycle and owner stranded in their own column at the top right),
// which gave the reader four places to look for one fact each instead of two
// lines to read in order: who this is, then when.
//
// Location and employee-band chips are dropped rather than moved: both
// already have a home in the rail's Details grid (companyraildetails.tsx),
// so drawing them here a second time would be the same fact stated twice
// rather than a real loss.
//
// `agent: deepread`-style record provenance no longer renders here. It was
// CompanyPulse's ProvenanceTag on `org.captured_by` — who/what wrote the
// ORGANIZATION ROW itself, not a fact about any field on this line — and
// nothing at this level of the mockup shows anything like it. There is
// currently no rail section that owns record-level provenance to move it
// to; removing it here is a real loss of that passive display (the fact
// still lives in the record's full history), flagged rather than papered
// over.
//
// The account's "way in" (the contact who carries the relationship,
// previously StrengthPulse) is dropped for the same reason and the same
// caveat: nothing in the identity line's mockup shape has room for it, and
// no other surface on this page currently states it.
export function CompanyIdentityLine({
  org,
  view,
  loading,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  // Still fetching the composite read: the exchange date is withheld the
  // same way it is when the section itself is withheld, so a page mid-load
  // never reads as an account nobody has ever written to.
  loading?: boolean;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const viewerId = useViewerId();
  // WHO wrote this record, by name. The roster is the page's existing one —
  // CompanyOwnerControl on the line above already reads it, and both share
  // react-query's `["users"]` entry, so this adds no request.
  //
  // Undefined on a roster miss, deliberately: `ProvenanceTag` falls back to
  // "typed by a person", which is true. The roster reads one page of 200 users,
  // so an author outside it would otherwise be named with the raw uuid the
  // generic reference renders — and "typed by 3f2b8c…" is worse than not
  // claiming to know, not better.
  const roster = useRoster("user", true);
  const authorName = (userId: string) => {
    const entry = roster.data?.find((candidate) => candidate.id === userId);
    return entry && "display_name" in entry ? entry.display_name : undefined;
  };
  // Withheld or still in flight, the line says nothing about it: naming no way
  // in on an account that has one is worse than saying nothing.
  const wayIn = loading ? undefined : view?.strength;
  const when = (at: string) => formatDateAbbrev(at, locale, RECORD_ZONE);
  // Withheld, absent, or still in flight, the line says nothing about it at
  // all: "never contacted" read off data the page could not answer is a
  // business conclusion it has no basis for, and it is the one a rep would
  // act on.
  const touchKnown = Boolean(
    view && !loading && !view.sections_omitted?.includes("last_touch"),
  );
  // The two directions folded into the later of the two, now that acting on
  // WHICH side wrote last belongs to the daily brief rather than the header
  // (the brief's own detail line still names direction). The header states
  // only that the relationship is or is not live.
  const inbound = view?.last_inbound_at;
  const outbound = view?.last_outbound_at;
  const lastExchange =
    inbound && outbound
      ? inbound > outbound
        ? inbound
        : outbound
      : (inbound ?? outbound);
  const website = companyWebsite(org);
  // What the account IS and who owns it, as plain facts rather than pill
  // chips — built as a list rather than three fixed slots because website
  // and industry are each legitimately absent, and a fixed "· ·" either
  // side of a missing fact would leave a stray separator.
  const facts: ReactElement[] = [];
  if (website) {
    facts.push(
      <a key="website" className="co-meta-link" href={website}>
        {displayHost(website)}
      </a>,
    );
  }
  if (org.industry) {
    facts.push(<span key="industry">{org.industry}</span>);
  }
  // Owner always has a slot, even unowned — "Unassigned" is the honest
  // current state, not an absence to omit the way a missing website is.
  facts.push(
    <span key="owner">
      {t("co.pulse.owner")} <CompanyOwnerControl org={org} hideLabel />
    </span>,
  );
  return (
    <div className="co-identity-meta">
      <div className="co-meta-line">
        {facts.map((fact, i) => (
          <Fragment key={fact.key}>
            {i > 0 && <span className="co-sep">·</span>}
            {fact}
          </Fragment>
        ))}
      </div>
      <div className="co-meta-line co-meta-quiet">
        <span>{t("co.pulse.created", { when: when(org.created_at) })}</span>
        {/* WHO wrote the record, beside WHEN it was written: a mark about the
            row itself rather than about any field on it, so it belongs with
            the record's own dates and not on the line that says what the
            account is. */}
        <ProvenanceTag
          provenance={provenanceOf(org.captured_by, viewerId)}
          renderUser={authorName}
        />
        {/* The contact the relationship actually runs through, on the line of
            the record's own facts. The NAME is a live lookup, so the sentence
            is built from two translated halves around it rather than
            interpolating a placeholder and appending the name after the full
            stop — which breaks word order in English and worse in German. */}
        {wayIn?.contributor_person_id && (
          <>
            <span className="co-sep">·</span>
            <span>
              {t("co.pulse.strongestLead")}{" "}
              <EntityRef kind="person" id={wayIn.contributor_person_id} />{" "}
              {t(
                wayIn.contact_count === 1
                  ? "co.pulse.strengthTail.one"
                  : "co.pulse.strengthTail.other",
                { count: wayIn.contact_count },
              )}
            </span>
          </>
        )}
        {touchKnown && (
          <>
            <span className="co-sep">·</span>
            <span>
              {lastExchange
                ? t("co.pulse.lastExchange", { when: when(lastExchange) })
                : t("co.pulse.neverTouched")}
            </span>
          </>
        )}
      </div>
    </div>
  );
}

// useAccountChronology assembles the middle column's history: what happened
// with this account, what changed about the record, or both in one order.
//
// The two feeds page independently, so "both" is not a concatenation — the
// merge is cut where it stops being provably complete (mergeChronology), and
// the cut is stated rather than left to look like the end of the history.
