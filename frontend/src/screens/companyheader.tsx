import { useQueryClient } from "@tanstack/react-query";
import { Globe, Link2, MapPin, Tag, Users } from "lucide-react";
import { type ReactElement, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { useCan } from "../app/capability";
import { navigate } from "../app/router";
import { Badge, Button, OverflowMenu } from "../design-system/atoms";
import { InlineChoice, InlineText } from "../design-system/inlinechoice";
import { Chip } from "../design-system/readings";
import { ProvenanceTag } from "../design-system/trust";
import { formatDateTime } from "../format/format";
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
// in place, and the line of facts that says where the relationship stands.
//
// Lifecycle and owner sit in their OWN block at the top right rather than in
// the pulse line (mockup State D). They are the two things a reader SETS about
// an account; the pulse states what happened to it. Mixed into one line the
// two controls read as more facts, and the reader has no cue that they can be
// changed.
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
}: Readonly<{ org: Organization }>) {
  // Archived records take no new activity: the write is refused server-side,
  // so offering the verb would only produce a modal that fails on save.
  if (org.archived_at) {
    return null;
  }
  return (
    <>
      <WriteEmailAction org={org} />
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
function WriteEmailAction({ org }: Readonly<{ org: Organization }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="primary" onClick={() => setOpen(true)}>
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
          onClose={() => setOpen(false)}
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
    params: { path: { id: org.id }, ...ifMatch(org.version) },
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
function useCompanyFieldPatch(org: Organization) {
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
function useCompanyReadOnlyReason(org: Organization): string | undefined {
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

function CompanyLifecycleControl({ org }: Readonly<{ org: Organization }>) {
  const t = useT();
  const canUpdate = useCan("organization", "update");
  const readOnlyReason = useCompanyReadOnlyReason(org);
  const patch = useCompanyFieldPatch(org);
  return (
    <InlineChoice
      label={t("org.lifecycle")}
      value={org.lifecycle ?? "unknown"}
      options={LIFECYCLE_OPTIONS.map((value) => ({
        value,
        label: t(LIFECYCLE_LABELS[value]),
      }))}
      canEdit={canUpdate && !readOnlyReason}
      readOnlyReason={readOnlyReason}
      render={(value) => (
        <Badge>{t(LIFECYCLE_LABELS[value as Lifecycle])}</Badge>
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

function CompanyOwnerControl({ org }: Readonly<{ org: Organization }>) {
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
  // it today even when it cannot resolve them.
  if (org.owner_id && !owners.some((user) => user.value === org.owner_id)) {
    owners.unshift({
      value: org.owner_id,
      label: t("co.owner.notInRoster"),
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
      value={org.owner_id ?? ""}
      options={options}
      canEdit={canUpdate && !readOnlyReason}
      readOnlyReason={readOnlyReason}
      render={(value) =>
        value ? <EntityRef kind="user" id={value} /> : t("co.pulse.unowned")
      }
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
            ...ifMatch(org.version),
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
  onOpenHistory,
  onSetUpPartner,
}: Readonly<{
  org: Organization;
  onOpenHistory: () => void;
  onSetUpPartner: () => void;
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
                      ...ifMatch(org.version),
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
        </OverflowMenu>
      )}
    </>
  );
}

// CompanyDescription is the one-line "what this company does" under the title,
// editable where it is read (plan §4.1). Absent, it is still pressable — an
// unwritten description is the case a rep most wants to fix, and a line that
// only appears once it exists can never be started.
export function CompanyDescription({ org }: Readonly<{ org: Organization }>) {
  const t = useT();
  const canUpdate = useCan("organization", "update");
  const readOnlyReason = useCompanyReadOnlyReason(org);
  const patch = useCompanyFieldPatch(org);
  const value = org.description ?? "";
  // Nothing written and nothing the reader could write: the line is not
  // rendered at all rather than as an empty row with a placeholder they
  // cannot act on.
  if (!value && (!canUpdate || readOnlyReason)) {
    return null;
  }
  return (
    <p className="co-description">
      {/* Keyed by the record, so navigating to another company while the line
          is open REMOUNTS the control rather than re-pointing it. Without the
          key the draft typed for company A survives, and pressing Save then
          writes it to company B with nothing on screen saying so — the same
          trap the composer is keyed against. */}
      <InlineText
        key={org.id}
        label={t("co.description.label")}
        value={value}
        placeholder={t("co.description.placeholder")}
        maxLength={COMPANY_DESCRIPTION_MAX}
        canEdit={canUpdate && !readOnlyReason}
        readOnlyReason={readOnlyReason}
        onSave={(next) => patch({ description: next || null })}
      />
    </p>
  );
}

// The column's own CHECK bound (core 0203). Stated here so the field stops the
// reader at the limit rather than letting the server refuse the save.
const COMPANY_DESCRIPTION_MAX = 500;

// CompanyChips is the header's row of facts: where the company is on the web,
// where it is on the map, what it does and how big it is (plan §4.1). It
// replaces the joined subtitle string it grew out of — five values crushed
// into one dot-separated line, where the two that are links did not read as
// links and none of them said which was which.
export function CompanyChips({ org }: Readonly<{ org: Organization }>) {
  const t = useT();
  // `website_url` is derived server-side from the primary domain row, and an
  // overlay-mirrored company carries the domain without it. Falling back to
  // the row keeps the chip on those records rather than silently dropping the
  // one identifying fact the reader had before.
  const primaryDomain = (org.domains ?? []).find((d) => d.is_primary)?.domain;
  const website =
    org.website_url ?? (primaryDomain ? `https://${primaryDomain}` : undefined);
  const location = [org.address?.city, org.address?.country]
    .filter(Boolean)
    .join(", ");
  const chips: ReactElement[] = [];
  if (website) {
    chips.push(
      <Chip key="website" icon={Globe} href={website}>
        {displayHost(website)}
      </Chip>,
    );
  }
  if (org.linkedin_url) {
    chips.push(
      <Chip key="linkedin" icon={Link2} href={org.linkedin_url}>
        {t("co.chip.linkedin")}
      </Chip>,
    );
  }
  if (location) {
    chips.push(
      <Chip key="location" icon={MapPin}>
        {location}
      </Chip>,
    );
  }
  if (org.industry) {
    chips.push(
      <Chip key="industry" icon={Tag}>
        {org.industry}
      </Chip>,
    );
  }
  if (org.size_band) {
    chips.push(
      <Chip key="size" icon={Users}>
        {t("co.chip.employees", { band: org.size_band })}
      </Chip>,
    );
  }
  if (chips.length === 0) {
    return null;
  }
  return (
    // A list, so the row announces how many facts it carries and a reader can
    // step through them — a bare div with a label announces the label and then
    // runs the five chips together as one string.
    <ul className="co-chiprow" aria-label={t("co.chip.rowLabel")}>
      {chips.map((chip) => (
        <li key={chip.key}>{chip}</li>
      ))}
    </ul>
  );
}

// The scheme is noise in a chip: every one of these is https, and "https://"
// costs eight characters of a row that has five things to fit. A URL we cannot
// parse is shown whole rather than silently dropped.
function displayHost(url: string): string {
  try {
    return new URL(url).host.replace(/^www\./, "");
  } catch {
    return url;
  }
}

// CompanyPulse is the one-line state of the relationship: how warm it is and
// who carries it, when it was last touched, and who owns it. Each part is
// omitted when the 360 could not answer it, so the line never implies a
// number the reader was not allowed to see.
// The account's standing: the two values a reader changes in place, stacked
// at the top right of the header where the mockup puts them.
export function CompanyStanding({
  org,
}: Readonly<{ org: Organization }>): ReactElement {
  return (
    <div className="co-standing">
      <CompanyLifecycleControl org={org} />
      <CompanyOwnerControl org={org} />
    </div>
  );
}

export function CompanyPulse({
  org,
  view,
  onOpenDecisions,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  // The overview owns the decisions panel, so only it can offer the way in.
  // The other tabs render the same pulse line without the chip rather than a
  // button that has nothing to open.
  onOpenDecisions?: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const viewerId = useViewerId();
  const strength = view?.strength;
  // Withheld or absent, the line says nothing at all: "never contacted" read
  // off missing data is a business conclusion the page has no basis for, and
  // it is the one a rep would act on.
  const touchKnown = Boolean(
    view && !view.sections_omitted?.includes("last_touch"),
  );
  const inbound = view?.last_inbound_at;
  const outbound = view?.last_outbound_at;
  const when = (at: string) => formatDateTime(at, locale, RECORD_ZONE);
  return (
    <>
      {strength && <StrengthPulse strength={strength} />}
      {/* Both directions, side by side. Folding them into one "last touch"
          hides the only distinction a reader acts on: an account we mailed a
          fortnight ago with no reply and one that wrote to us this morning
          have the same last-touch date and opposite meanings. */}
      {touchKnown && !inbound && !outbound && (
        <span>{t("co.pulse.neverTouched")}</span>
      )}
      {touchKnown && (inbound || outbound) && (
        <>
          <span>
            {inbound
              ? t("co.pulse.lastInbound", { when: when(inbound) })
              : t("co.pulse.noInbound")}
          </span>
          <span>
            {outbound
              ? t("co.pulse.lastOutbound", { when: when(outbound) })
              : t("co.pulse.noOutbound")}
          </span>
        </>
      )}
      {/* Where the RECORD came from — a different question from who owns it,
          and the reason both now carry a word saying which is which. */}
      <ProvenanceTag
        provenance={provenanceOf(org.captured_by, viewerId)}
        renderUser={(userId) => <EntityRef kind="user" id={userId} />}
      />
      {/* What is waiting on a human decision here, and the way to make it.
          The count was a badge that led nowhere: a reader told that 27
          decisions are owed and given no way to pay them learns only that the
          page keeps score. */}
      {onOpenDecisions && (
        <DecisionsChip view={view} onOpen={onOpenDecisions} />
      )}
    </>
  );
}

// StrengthPulse names the contact who carries the relationship — the way in —
// and no longer renders a 0-100 score (AC-company-2, ADR-0079 arc).
//
// The number was PO-F-3's MAX over the account's contacts, and PO-F-3 is a
// decayed count of recent two-way messages. So one talkative contact spoke for
// the whole account, a long low-volume relationship scored near zero, and the
// header showed "Relationship 2/100" as though it were a verdict. The factors
// are still computed and still shown in the relationship detail, where each is
// traceable to the messages behind it; only the single number is withheld.
//
// The contributor's NAME is a live lookup, so the sentence is assembled from
// two translated halves around it rather than interpolating an empty
// placeholder and appending the name after the full stop — which broke word
// order in English and worse in German.
function StrengthPulse({
  strength,
}: Readonly<{ strength: NonNullable<Organization360View["strength"]> }>) {
  const t = useT();
  if (!strength.contributor_person_id) {
    // A dormant account: no contact has ever interacted, so there is no
    // relationship to attribute and no number worth leading with.
    return <span>{t("co.pulse.noStrength")}</span>;
  }
  return (
    <span>
      {t("co.pulse.strongestLead")}{" "}
      <EntityRef kind="person" id={strength.contributor_person_id} />{" "}
      {t(
        strength.contact_count === 1
          ? "co.pulse.strengthTail.one"
          : "co.pulse.strengthTail.other",
        { count: strength.contact_count },
      )}
    </span>
  );
}

// useAccountChronology assembles the middle column's history: what happened
// with this account, what changed about the record, or both in one order.
//
// The two feeds page independently, so "both" is not a concatenation — the
// merge is cut where it stops being provably complete (mergeChronology), and
// the cut is stated rather than left to look like the end of the history.
