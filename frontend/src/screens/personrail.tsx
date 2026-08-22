import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Mail, Phone } from "lucide-react";
import type { ReactNode } from "react";
import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { useCan } from "../app/capability";
import { navigate } from "../app/router";
import {
  Avatar,
  Button,
  Checkbox,
  Disclosure,
  Field,
  Modal,
  OverflowMenu,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { FieldGrid, FieldRow } from "../design-system/fieldgrid";
import { InlineText } from "../design-system/inlinechoice";
import { Panel } from "../design-system/panel";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import {
  omitted,
  type SectionState,
  SurfaceState,
} from "../design-system/surfacestate";
import { useT } from "../i18n";
import { useProviderLabel } from "./channelproviders";
import { problemMessageOf, throwProblem, useSorMode } from "./common";
import { stillHeld, today } from "./employmentcurrency";
import { interactionIcon } from "./interactionchrome";
import { consentWord } from "./personstrip";
import { personTabRoute } from "./persontab";

// The right rail (concept §5.11): one continuous panel, its six slices told
// apart by a hairline rather than by a gap between cards — the same anatomy
// the company record's rail draws (companyrail.tsx), and for the same
// reason: a hairline between two facts about the same person reads as one
// story with headings, where a gap between cards reads as two stories that
// happen to sit beside each other.
//
// Every section here is still a GLANCE. The rail never becomes a second
// body — a reader who has to read the margin has lost the column it sits
// beside.

type Person360 = components["schemas"]["Person360"];
type Person = components["schemas"]["Person"];
type PersonConsentGuard = components["schemas"]["PersonConsentGuard"];
type UpdatePersonRequest = components["schemas"]["UpdatePersonRequest"];

// --- what this reader was allowed to read ----------------------------------

// A negative verdict must first prove it was allowed to look.
//
// The 360 answers a section the reader has no grant for by leaving it out and
// naming it in `sections_omitted`, so an absent field carries two opposite
// meanings — nothing was captured, or nothing was shown — and only the list
// tells them apart. This rail is the worst place in the product to get that
// wrong: its words are short verdicts ("One-sided", "Never", "Thin") that read
// as measured facts rather than as summaries, so a reader without an activity
// grant is told the contact has never written to them.
//
// So the list is read ONCE, into the sections this rail derives its words
// from, rather than as a check each verdict is trusted to remember — seven
// separate checks is the shape that left every one of them unwritten.
type Withheld = Readonly<{
  lastTouch: boolean;
  activities: boolean;
  commercial: boolean;
  nextMeeting: boolean;
  network: boolean;
  employments: boolean;
}>;

function withheldSections(view: Person360): Withheld {
  return {
    lastTouch: omitted(view, "last_touch"),
    activities: omitted(view, "activities"),
    commercial: omitted(view, "commercial"),
    nextMeeting: omitted(view, "next_meeting"),
    network: omitted(view, "network"),
    employments: omitted(view, "employments"),
  };
}

// A withheld reading says so rather than rendering the word a derivation would
// have produced from the absence, and it carries no tone, because there is no
// verdict to colour. The strip on this same page keeps the identical rule for
// its own six readings (personstrip.tsx), down to the word, so the two halves
// of the record cannot disagree about what a missing section means.
function reading(
  value: string,
  withheld: boolean,
  t: ReturnType<typeof useT>,
): string {
  return withheld ? t("record.notShown") : value;
}

// The body state of a section whose sentence is a claim about the record.
// `empty` is the only state allowed to say there is none of something, so a
// withheld section keeps its place in the rail and says it is withheld instead
// of drawing as an account with nothing on it.
function bodyState(withheld: boolean, count: number): SectionState {
  if (withheld) {
    return "withheld";
  }
  return count === 0 ? "empty" : "ready";
}

export function PersonRail({
  view,
  guard,
  firstName,
  onExplain,
}: Readonly<{
  view: Person360;
  guard: PersonConsentGuard | undefined;
  firstName: string;
  onExplain: () => void;
}>) {
  const t = useT();
  return (
    <div className="pe-rail" data-testid="person-rail">
      <Panel title={t("person.page.asideLabel")}>
        <DetailsGrid view={view} />
        <Employers view={view} />
        <RelationshipPulse view={view} onExplain={onExplain} />
        <WhoKnows view={view} firstName={firstName} />
        <SignalsAndRisks view={view} />
        <ConsentAndChannels view={view} guard={guard} />
        <RecentActivity view={view} />
      </Panel>
    </div>
  );
}

// --- Details -----------------------------------------------------------

// patchPersonField sends one field through the ordinary person PATCH, with
// the record's own version as If-Match — the same shape companyheader.tsx's
// patchCompanyField uses for the account, so a person edit and an
// organization edit cannot end up disagreeing about what a version conflict
// or a failed save looks like.
//
// It throws on failure rather than swallowing: InlineText renders what is
// thrown beside the control, and the server's problem detail is a better
// sentence than any this layer could invent.
async function patchPersonField(
  person: Person,
  body: UpdatePersonRequest,
): Promise<void> {
  const { error } = await api.PATCH("/people/{id}", {
    params: {
      path: { id: person.id },
      ...ifMatch(requireVersion(person.version)),
    },
    body,
  });
  if (error) {
    throwProblem(error);
  }
}

// usePersonFieldPatch wires one inline Details edit to the query cache.
// person360 is the ONE read every other component on this page draws the
// person's own fields from (the identity line, the strip, this rail), so it
// is what gets refetched; personBrief comes with it because a changed name
// or title can change what the brief's own sentences say about this person.
function usePersonFieldPatch(person: Person) {
  const queryClient = useQueryClient();
  return async (body: UpdatePersonRequest) => {
    await patchPersonField(person, body);
    await queryClient.invalidateQueries({
      queryKey: ["person360", person.id],
    });
    await queryClient.invalidateQueries({
      queryKey: ["personBrief", person.id],
    });
  };
}

// usePersonReadOnlyReason says why this record cannot be edited, when there
// is something worth saying — the same two reasons companyheader.tsx's own
// useCompanyReadOnlyReason gives for an account: archived first, since it is
// the one a reader can act on (restore it), overlay second, since it is a
// property of the installation rather than of this one record.
function usePersonReadOnlyReason(person: Person): string | undefined {
  const t = useT();
  const overlay = useSorMode() === "overlay";
  if (person.archived_at) {
    return t("person.rail.archivedReadOnly");
  }
  if (overlay) {
    return t("overlay.partialWriteBack");
  }
  return undefined;
}

// `social` is an open map on the wire (`{ [key: string]: unknown }`), so a
// key that is present but not a string reads as absent rather than throwing —
// the same care companyheader.tsx's own address/domain reads take with a
// wire type wider than the one part being written.
function linkedinOf(person: Person): string {
  const value = person.social?.linkedin;
  return typeof value === "string" ? value : "";
}

// Email and phone cannot be sent to `PATCH /people/{id}` (interfaces.md,
// UpdatePersonRequest carries only full_name/first_name/last_name/title/
// owner_id/social/address) — they are set once at capture and have no
// update path on the wire, native or overlay. They still draw as ordinary
// rows, so the section reads as one grid rather than four editable rows and
// two that look like an omission.
const CONTACT_METHOD_IMMUTABLE = "person.rail.contactMethodImmutable" as const;

type DetailsRowProps = Readonly<{
  person: Person;
  canEdit: boolean;
  readOnlyReason: string | undefined;
  patch: (body: UpdatePersonRequest) => Promise<void>;
}>;

function NameRow({ person, canEdit, readOnlyReason, patch }: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("create.fullName")}>
      <InlineText
        label={t("create.fullName")}
        value={person.full_name}
        placeholder={t("field.addFullName")}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) => patch({ full_name: next })}
      />
    </FieldRow>
  );
}

function TitleRow({ person, canEdit, readOnlyReason, patch }: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("create.personTitle")}>
      <InlineText
        label={t("create.personTitle")}
        value={person.title ?? ""}
        placeholder={t("field.addTitle")}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) => patch({ title: next || null })}
      />
    </FieldRow>
  );
}

// Always sends the WHOLE `social` object back with only `linkedin` changed —
// `social` replaces the map wholesale on the wire, so omitting the other
// entries (twitter, github, …) would blank them.
function LinkedinRow({
  person,
  canEdit,
  readOnlyReason,
  patch,
}: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("person.page.linkedin")}>
      <InlineText
        label={t("person.page.linkedin")}
        value={linkedinOf(person)}
        placeholder={t("field.addLinkedinUrl")}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) =>
          patch({ social: { ...person.social, linkedin: next || null } })
        }
      />
    </FieldRow>
  );
}

// Always sends the WHOLE `address` object back with only `city` changed — the
// same reason LinkedinRow above sends the whole `social` object: `address`
// replaces wholesale on the wire, and this row is the only address part the
// rail draws.
function CityRow({ person, canEdit, readOnlyReason, patch }: DetailsRowProps) {
  const t = useT();
  return (
    <FieldRow label={t("create.city")}>
      <InlineText
        label={t("create.city")}
        value={person.address?.city ?? ""}
        placeholder={t("field.addCity")}
        canEdit={canEdit}
        readOnlyReason={readOnlyReason}
        onSave={(next) =>
          patch({ address: { ...person.address, city: next || null } })
        }
      />
    </FieldRow>
  );
}

// Email and phone below are always drawn with `canEdit={false}`: see
// CONTACT_METHOD_IMMUTABLE above for why there is no PATCH for either. The
// `onSave` each carries can never run — InlineText only calls it from the
// editable path, which `canEdit={false}` never opens — so it exists only to
// satisfy the prop's type, not as a path this rail actually takes.
function EmailRow({ person }: Readonly<{ person: Person }>) {
  const t = useT();
  const reason = t(CONTACT_METHOD_IMMUTABLE);
  return (
    <FieldRow label={t("person.rail.email")}>
      <InlineText
        label={t("person.rail.email")}
        value={person.emails?.[0]?.email ?? ""}
        placeholder={t("field.unset")}
        canEdit={false}
        readOnlyReason={reason}
        onSave={() => Promise.reject(new Error(reason))}
      />
    </FieldRow>
  );
}

function PhoneRow({ person }: Readonly<{ person: Person }>) {
  const t = useT();
  const reason = t(CONTACT_METHOD_IMMUTABLE);
  return (
    <FieldRow label={t("person.rail.phone")}>
      <InlineText
        label={t("person.rail.phone")}
        value={person.phones?.[0]?.phone ?? ""}
        placeholder={t("field.unset")}
        canEdit={false}
        readOnlyReason={reason}
        onSave={() => Promise.reject(new Error(reason))}
      />
    </FieldRow>
  );
}

// The rail's own Details grid — the record's own fields, at a glance above
// the six relationship sections below it. Writability gates the VERBS only:
// an archived or overlay-mirrored contact still shows every field, it simply
// loses the edit affordance (InlineText's own `canEdit={false}` path), the
// same rule companyraildetails.tsx's DetailsGrid keeps for the account.
function DetailsGrid({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const person = view.person;
  const canUpdate = useCan("person", "update");
  const readOnlyReason = usePersonReadOnlyReason(person);
  const patch = usePersonFieldPatch(person);
  const row: DetailsRowProps = {
    person,
    canEdit: canUpdate && !readOnlyReason,
    readOnlyReason,
    patch,
  };
  return (
    <Disclosure
      className="pe-sect"
      open
      summary={t("person.rail.detailsTitle")}
    >
      <FieldGrid>
        <NameRow {...row} />
        <TitleRow {...row} />
        <EmailRow person={person} />
        <PhoneRow person={person} />
        <LinkedinRow {...row} />
        <CityRow {...row} />
      </FieldGrid>
    </Disclosure>
  );
}

// --- Employers ---------------------------------------------------------

type Employment = components["schemas"]["Person360Employment"];
type CreateRelationshipRequest =
  components["schemas"]["CreateRelationshipRequest"];
type UpdateRelationshipRequest =
  components["schemas"]["UpdateRelationshipRequest"];

async function searchOrganizationCandidates(
  q: string,
): Promise<RecordPickerCandidate[]> {
  const { data, error } = await api.GET("/organizations", {
    params: { query: { q, limit: 10 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.map((org) => ({ id: org.id, name: org.display_name }));
}

// Person360Employment is the 360's own projection of an employment edge — it
// carries `relationship_id` but not the relationship row's own `version`, and
// there is no `GET /relationships/{id}` in the contract to re-read one by id
// (relationships.tsx's RelationshipsTab keeps the same note, for the same
// reason). The one honest way to get an If-Match for a row this rail only
// knows by id is to re-read it through the list endpoint, scoped tight enough
// (this person, this org, this kind) that it can only answer with the one
// edge this row is already showing.
async function fetchEmploymentVersion(
  employment: Employment,
  personId: string,
): Promise<number | undefined> {
  const { data, error } = await api.GET("/relationships", {
    params: {
      query: {
        person_id: personId,
        organization_id: employment.organization_id,
        kind: "employment",
      },
    },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.find((rel) => rel.id === employment.relationship_id)
    ?.version;
}

// The one write path for everything on an employment row that is neither its
// creation nor its removal: the role InlineText commits below and the
// "mark as ended" verb both patch through here, so a role edit and an ended
// date answer the same version-skew and permission failures the same way.
//
// An unresolved version is refused here rather than sent unpinned: a write with
// no precondition writes straight over whatever changed underneath it instead of
// failing loud with a 409. The list scoping fetchEmploymentVersion uses can
// legitimately come back without this row (a narrower read scope, a paged
// response, an edge whose kind changed), and this rail is the one place that
// knows to say so — hence its own sentence for the reader rather than the shared
// refusal, which can only report that the write did not happen.
async function patchEmployment(
  employment: Employment,
  personId: string,
  body: UpdateRelationshipRequest,
  t: ReturnType<typeof useT>,
): Promise<void> {
  const version = await fetchEmploymentVersion(employment, personId);
  if (version === undefined) {
    throwProblem({
      detail: t("person.rail.employmentVersionUnresolved"),
    });
  }
  const { error } = await api.PATCH("/relationships/{id}", {
    params: {
      path: { id: employment.relationship_id },
      ...ifMatch(version),
    },
    body,
  });
  if (error) {
    throwProblem(error);
  }
}

// The four writes the Companies section makes, sharing one invalidation:
// person360 is what this section itself reads its rows from, and personBrief
// comes with it because the brief's first sentence names the employer. The
// role InlineText below goes through `update` rather than calling
// patchEmployment on its own, so every write this section makes — role,
// ended date, create, remove — ends in the same refetch and the rail never
// shows a saved edit next to its own stale value.
function useEmploymentActions(personId: string) {
  const t = useT();
  const queryClient = useQueryClient();
  const invalidate = async () => {
    await queryClient.invalidateQueries({
      queryKey: ["person360", personId],
    });
    await queryClient.invalidateQueries({
      queryKey: ["personBrief", personId],
    });
  };
  const create = useMutation({
    mutationFn: async (body: CreateRelationshipRequest) => {
      const { data, error } = await api.POST("/relationships", { body });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: invalidate,
  });
  const end = useMutation({
    mutationFn: (employment: Employment) =>
      patchEmployment(employment, personId, { ended_at: today() }, t),
    onSuccess: invalidate,
  });
  const update = useMutation({
    mutationFn: ({
      employment,
      body,
    }: {
      employment: Employment;
      body: UpdateRelationshipRequest;
    }) => patchEmployment(employment, personId, body, t),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: async (relationshipId: string) => {
      const { error } = await api.DELETE("/relationships/{id}", {
        params: { path: { id: relationshipId } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: invalidate,
  });
  return { create, end, update, remove };
}

type EmploymentActions = ReturnType<typeof useEmploymentActions>;

// A person can hold more than one employment edge at once, so this is a
// list rather than the single Details row it used to be. The current
// employer leads and carries an explicit marker: `is_current_primary` is
// the recorded fact, not something a reader should have to derive from
// whether `ended_at` happens to be blank — a rep who has to check dates to
// know which company to email has already lost the point of the marker.
function Employers({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const person = view.person;
  const readOnlyReason = usePersonReadOnlyReason(person);
  // The same grant and the same read-only reasons DetailsGrid gates its own
  // edit affordances on — a reader who cannot edit the person's own fields
  // cannot edit which company they work at either.
  const canEdit = useCan("person", "update") && !readOnlyReason;
  const actions = useEmploymentActions(person.id);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Employment | null>(null);
  const employments = [...(view.employments?.data ?? [])].sort(
    (a, b) =>
      Number(b.is_current_primary && stillHeld(b)) -
      Number(a.is_current_primary && stillHeld(a)),
  );
  // Every org this person already has a live edge to — the 360 projection
  // drops an edge the moment it is removed, so this list IS the live set,
  // nothing further to filter. AddEmploymentModal excludes these from its
  // own picker so a rep cannot draw a second edge to a company already on
  // this list.
  //
  // Memoized on a primitive key rather than on `employments` itself: the
  // array here is rebuilt fresh every render (the sort above always returns
  // a new one), and AddEmploymentModal's own searchTargets treats a new
  // identity as a new search space and clears the picker's candidates —
  // so the set only gets a new identity when the set of ids it names
  // actually changes.
  // "en" rather than the reader's locale, because this string is only ever
  // compared against a previous rendering of itself: whose locale produced it
  // must not be part of the answer.
  const connectedOrgKey = employments
    .map((employment) => employment.organization_id)
    .sort((a, b) => a.localeCompare(b, "en"))
    .join(",");
  const connectedOrgIds = useMemo(
    () => (connectedOrgKey === "" ? [] : connectedOrgKey.split(",")),
    [connectedOrgKey],
  );
  return (
    <Disclosure
      className="pe-sect"
      open
      summary={
        canEdit ? (
          <span className="pe-sect-summary">
            {t("person.rail.employmentTitle")}
            <Button small onClick={() => setAdding(true)}>
              {t("person.rail.addEmployment")}
            </Button>
          </span>
        ) : (
          t("person.rail.employmentTitle")
        )
      }
    >
      <SurfaceState
        state={bodyState(
          withheldSections(view).employments,
          employments.length,
        )}
        emptyLabel={t("person.rail.noEmployment")}
      >
        {employments.map((employment) => (
          <EmploymentRow
            key={employment.relationship_id}
            employment={employment}
            canEdit={canEdit}
            readOnlyReason={readOnlyReason}
            actions={actions}
            onRemove={() => setRemoving(employment)}
          />
        ))}
      </SurfaceState>
      <AddEmploymentModal
        open={adding}
        onClose={() => setAdding(false)}
        personId={person.id}
        create={actions.create}
        excludedOrgIds={connectedOrgIds}
        hasCurrentEmployment={employments.some(stillHeld)}
      />
      {/* Remove is the irreversible verb — the connection and its history are
          gone, not merely dated — so it is the one that sits behind a
          confirm, unlike "mark as ended" which is an ordinary field edit. */}
      <ConfirmModal
        open={removing !== null}
        onClose={() => {
          setRemoving(null);
          actions.remove.reset();
        }}
        title={t("person.rail.removeEmploymentTitle")}
        confirmLabel={t("rel.remove")}
        confirmVariant="danger"
        onConfirm={() => {
          if (removing) {
            actions.remove.mutate(removing.relationship_id, {
              onSuccess: () => setRemoving(null),
            });
          }
        }}
        pending={actions.remove.isPending}
        error={
          actions.remove.isError
            ? problemMessageOf(actions.remove.error, t)
            : null
        }
      >
        <p className="t-body">
          {t("person.rail.removeEmploymentBody", {
            org: removing?.organization_name ?? t("field.unset"),
          })}
        </p>
      </ConfirmModal>
    </Disclosure>
  );
}

// One employment edge: the org it names, the role at that org (inline-
// editable — this is the ONE place a per-company title is corrected;
// `person.title` is a different field, edited in Details above), the dates,
// and the row's own verbs folded behind an OverflowMenu — this row already
// carries a focusable inline-edit control, so the verbs stay out of the way
// until the row is hovered or that control (or the trigger itself) has
// focus, the same reveal the company page's task rows use for theirs.
function EmploymentRow({
  employment,
  canEdit,
  readOnlyReason,
  actions,
  onRemove,
}: Readonly<{
  employment: Employment;
  canEdit: boolean;
  readOnlyReason: string | undefined;
  actions: EmploymentActions;
  onRemove: () => void;
}>) {
  const t = useT();
  const detail = employmentDetail(employment, t);
  const ending =
    actions.end.isPending &&
    actions.end.variables?.relationship_id === employment.relationship_id;
  // isPending and isError can never both hold at once (one shared mutation
  // status behind both), so a failure that only rendered while "ending" was
  // also true could never actually draw: pending clears before error sets.
  // This row's own failure is instead keyed on the same identifier ending
  // uses, just checked against isError rather than isPending, so the row
  // that failed keeps its message once the mutation has settled.
  const endFailed =
    actions.end.isError &&
    actions.end.variables?.relationship_id === employment.relationship_id;
  return (
    <div className="pe-employment">
      <span className="pe-employment-body">
        <span className="pe-employment-org">
          {employment.organization_name ? (
            <button
              type="button"
              className="pe-meta-link"
              onClick={() =>
                navigate({
                  screen: "companies",
                  id: employment.organization_id,
                })
              }
            >
              {employment.organization_name}
            </button>
          ) : (
            <span className="inlinetext">{t("field.unset")}</span>
          )}
          {employment.is_current_primary && stillHeld(employment) && (
            <span className="pe-rail-value-good">{t("rel.current")}</span>
          )}
        </span>
        <span className="pe-employment-role">
          <InlineText
            label={t("rel.role")}
            value={employment.role ?? ""}
            placeholder={t("field.addTitle")}
            canEdit={canEdit}
            readOnlyReason={readOnlyReason}
            onSave={(next) =>
              actions.update.mutateAsync({
                employment,
                body: { role: next || null },
              })
            }
          />
        </span>
        {detail && <span className="pe-colleague-proof">{detail}</span>}
      </span>
      {canEdit && (
        <span className="pe-employment-actions">
          <OverflowMenu label={t("record.moreActions")}>
            {!employment.ended_at && (
              <Button
                small
                disabled={ending}
                onClick={() => actions.end.mutate(employment)}
              >
                {t("person.rail.markEnded")}
              </Button>
            )}
            <Button small variant="danger" onClick={onRemove}>
              {t("rel.remove")}
            </Button>
          </OverflowMenu>
        </span>
      )}
      {endFailed && (
        <p className="pe-colleague-proof" role="alert">
          {problemMessageOf(actions.end.error, t)}
        </p>
      )}
    </div>
  );
}

// The "add a company" modal: pick the org (RecordPicker, the shared
// debounced search-and-pick), optionally its role, and whether it is the
// current primary employer — a Checkbox, not a Switch, because ticking it
// states an intent this modal's own Save then writes, it is not itself the
// write (design-system/README.md's Checkbox/Switch distinction).
function AddEmploymentModal({
  open,
  onClose,
  personId,
  create,
  excludedOrgIds,
  hasCurrentEmployment,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  personId: string;
  create: EmploymentActions["create"];
  // Organizations this person already has a live employment edge to — the
  // picker refuses to offer a second edge to the same company, since only
  // a duplicated current-primary is refused server-side.
  excludedOrgIds: ReadonlyArray<string>;
  // Whether this person already holds a job that has not ended. It is the exact
  // fact the server's own rule turns on, read off the same rows, so the box can
  // START in the state the save will produce instead of showing the reader one
  // answer and writing the other.
  hasCurrentEmployment: boolean;
}>) {
  const t = useT();
  const headingId = useId();
  const [org, setOrg] = useState<RecordPickerCandidate | null>(null);
  const [role, setRole] = useState("");
  // Ticked by default for somebody with no current job, because that is what
  // the save will do either way: the server marks a person's only current
  // employment as their primary one. A box that started unticked and then
  // produced the opposite was worse than sending the wrong value — it showed
  // the reader a state the record never took, and left "no" expressible only by
  // ticking and unticking again.
  //
  // So the box always STATES an answer and the reader can change it. The
  // server's rule still exists for callers who send nothing — MCP and the API —
  // and `hasCurrentEmployment` is read off the same rows that rule reads, so the
  // two agree by construction rather than by being maintained in step.
  const [isCurrent, setIsCurrent] = useState(!hasCurrentEmployment);
  // useState only reads its initial value ONCE, and this modal is mounted for
  // the life of the section rather than remounted per open. So the default has
  // to be re-taken every time it opens, or it answers a question about the rows
  // as they were the first time the section rendered: end the only employment,
  // reopen, and the box would still be unticked because the initializer had
  // already run — writing an explicit `false` for the person's one current job,
  // which is the whole defect this default exists to prevent.
  useEffect(() => {
    if (open) {
      setIsCurrent(!hasCurrentEmployment);
    }
  }, [open, hasCurrentEmployment]);
  const [allConnected, setAllConnected] = useState(false);

  // Wraps the shared org search with this person's own already-connected
  // list. Kept on `excludedOrgIds` alone, nothing that changes while the
  // reader types — RecordPicker treats a new `searchTargets` identity as a
  // new search space and empties whatever it was already showing.
  const searchTargets = useCallback(
    async (q: string) => {
      const results = await searchOrganizationCandidates(q);
      const offered = results.filter(
        (candidate) => !excludedOrgIds.includes(candidate.id),
      );
      // Every match this query found is a company already on the list, not
      // an empty search — the two read the same in a bare candidate box, so
      // the modal says which one it is rather than leaving a silent gap.
      setAllConnected(results.length > 0 && offered.length === 0);
      return offered;
    },
    [excludedOrgIds],
  );

  function close() {
    setOrg(null);
    setRole("");
    setAllConnected(false);
    create.reset();
    onClose();
  }

  return (
    <Modal open={open} onClose={close} labelledBy={headingId}>
      <h2
        id={headingId}
        className="t-h2"
        style={{ marginBottom: "var(--space-3)" }}
      >
        {t("person.rail.addEmployment")}
      </h2>
      <div className="form-stack">
        <div className="field">
          <span className="t-label">{t("person.rail.employer")}</span>
          <RecordPicker
            label={t("person.rail.employer")}
            searchTargets={searchTargets}
            selected={org}
            onPick={setOrg}
            disabled={create.isPending}
          />
          {!org && allConnected && (
            <p className="t-caption">{t("person.rail.allOrgsConnected")}</p>
          )}
        </div>
        <Field label={t("rel.role")}>
          {(control) => (
            <TextInput
              {...control}
              value={role}
              disabled={create.isPending}
              onChange={(event) => setRole(event.target.value)}
            />
          )}
        </Field>
        <Checkbox
          label={t("person.rail.isCurrentEmployer")}
          checked={isCurrent}
          disabled={create.isPending}
          onChange={(event) => setIsCurrent(event.target.checked)}
        />
      </div>
      {create.isError && (
        <p
          className="t-caption"
          role="alert"
          style={{ color: "var(--danger)" }}
        >
          {problemMessageOf(create.error, t)}
        </p>
      )}
      <div className="actions">
        <Button onClick={close} disabled={create.isPending}>
          {t("create.cancel")}
        </Button>
        <Button
          variant="primary"
          disabled={!org || create.isPending}
          onClick={() => {
            if (!org) {
              return;
            }
            create.mutate(
              {
                kind: "employment",
                person_id: personId,
                organization_id: org.id,
                role: role.trim() || undefined,
                is_current_primary: isCurrent,
                // `manual` is the one word for a first-party write by a
                // person — through this form or through an assistant. It used
                // to say "ui", which named the screen rather than the origin,
                // and would have re-created the spelling the backfill removed.
                source: "manual",
              },
              { onSuccess: close },
            );
          }}
        >
          {t("create.save")}
        </Button>
      </div>
    </Modal>
  );
}

// The date range only — role is now its own InlineText control above, so
// repeating it here would be the same fact twice. The same day-month
// convention personmemory.tsx's own `dayMonth` renders every other date on
// this page with, so the two rail sections never disagree about what
// "12 Jan" means.
// An employment that has ENDED says so even when nobody recorded when it
// began: a period is a nicety, but a former employer that reads like a current
// one is a rep writing to the wrong company. Only a connection with neither
// date has nothing to say.
function employmentDetail(
  employment: Employment,
  t: ReturnType<typeof useT>,
): string {
  const start = employment.started_at
    ? dayMonth(employment.started_at)
    : undefined;
  const end = employment.ended_at ? dayMonth(employment.ended_at) : undefined;
  if (start && end) {
    return `${start} – ${end}`;
  }
  if (end) {
    return t("rel.endedOn", { when: end });
  }
  if (start) {
    return `${start} – ${t("rel.current")}`;
  }
  return "";
}

function dayMonth(at: string): string {
  return new Date(at).toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
  });
}

// --- Relationship pulse ----------------------------------------------------

// Words and directional facts. The composite score is NOT on the face
// (ADR-0096 D1); Explain reveals it with its factors and arithmetic.
function RelationshipPulse({
  view,
  onExplain,
}: Readonly<{ view: Person360; onExplain: () => void }>) {
  const t = useT();
  const hidden = withheldSections(view);
  const inbound = view.last_inbound_at;
  const outbound = view.last_outbound_at;
  const twoWay = Boolean(inbound && outbound);
  const colleagues = view.network?.colleagues?.length ?? 0;
  return (
    <Disclosure
      className="pe-sect"
      open
      summary={
        <span className="pe-sect-summary">
          {t("person.rail.pulseTitle")}
          <Button small onClick={onExplain}>
            {t("person.rail.explain")}
          </Button>
        </span>
      }
    >
      {/* Four of these five readings are derived from the two directional
          timestamps, which arrive together or not at all — one grant governs
          both — so one question answers all four. */}
      <Row
        label={t("person.rail.direction")}
        value={reading(
          twoWay ? t("person.rail.twoWay") : t("person.rail.oneSided"),
          hidden.lastTouch,
          t,
        )}
      />
      <Row
        label={t("person.rail.lastReply")}
        value={reading(sinceWords(inbound, t), hidden.lastTouch, t)}
      />
      <Row
        label={t("person.rail.coverage")}
        value={reading(colleagueWords(colleagues, t), hidden.network, t)}
      />
      <Row
        label={t("person.rail.trend")}
        value={reading(trendWord(view, t), hidden.lastTouch, t)}
      />
      <div className="pe-pulse-overall">
        {/* The overall reading is the only one drawn in the verdict colour, and
            a withheld reading is not a verdict: colouring "Not shown" would
            state a healthy relationship in the one place a reader glances. */}
        <Row
          label={t("person.rail.overall")}
          value={reading(overallWord(view, t), hidden.lastTouch, t)}
          strong={!hidden.lastTouch}
        />
      </div>
    </Disclosure>
  );
}

function colleagueWords(count: number, t: ReturnType<typeof useT>): string {
  return count === 1
    ? t("person.rail.colleagueOne")
    : t("person.rail.colleagues", { count });
}

function trendWord(view: Person360, t: ReturnType<typeof useT>): string {
  const inbound = view.last_inbound_at;
  const outbound = view.last_outbound_at;
  if (!inbound) {
    return t("person.rail.noInbound");
  }
  if (outbound && new Date(outbound) > new Date(inbound)) {
    return t("person.rail.cooling");
  }
  return t("person.rail.warming");
}

function overallWord(view: Person360, t: ReturnType<typeof useT>): string {
  const days = daysSince(view.last_inbound_at);
  if (days == null) {
    return t("person.rail.thin");
  }
  if (days > 14) {
    return t("person.rail.atRisk");
  }
  return t("person.rail.strong");
}

// --- Who knows them --------------------------------------------------------

function WhoKnows({
  view,
  firstName,
}: Readonly<{ view: Person360; firstName: string }>) {
  const t = useT();
  const colleagues = view.network?.colleagues ?? [];
  return (
    <Disclosure
      className="pe-sect"
      open
      summary={t("person.rail.whoKnows", { name: firstName })}
    >
      <SurfaceState
        state={bodyState(withheldSections(view).network, colleagues.length)}
        emptyLabel={t("person.rail.nobodyYet")}
      >
        {colleagues.slice(0, 3).map((colleague) => (
          <div className="pe-colleague" key={colleague.user_id}>
            <Avatar name={colleague.display_name} />
            <span>
              <span className="pe-colleague-name">
                {colleague.display_name}
              </span>
              <span className="pe-colleague-proof">
                {/* The PROOF, never a ranking nobody can check: six unanswered
                    sends must not read as stronger than two real exchanges. */}
                {t("person.rail.exchanges", {
                  count: colleague.interactions_90d,
                })}
              </span>
            </span>
          </div>
        ))}
      </SurfaceState>
    </Disclosure>
  );
}

// --- Signals and risks -----------------------------------------------------

function SignalsAndRisks({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const { signals, skipped } = derivedSignals(view, withheldSections(view), t);
  return (
    <Disclosure className="pe-sect" open summary={t("person.rail.signals")}>
      <SurfaceState
        state={signalsState(signals.length, skipped)}
        emptyLabel={t("person.rail.noSignals")}
      >
        {signals.map((signal) => (
          <div className="pe-signal" key={signal.text}>
            <span className={`pe-dot pe-dot-${signal.tone}`} />
            <span>{signal.text}</span>
          </div>
        ))}
      </SurfaceState>
    </Disclosure>
  );
}

// "Nothing stands out on this relationship" is the strongest sentence in the
// rail — it is the one a reader stops reading after — so it may only be said
// when every rule below actually ran. A derivation that had to skip a rule for
// want of a grant says the list is short instead, and one that could run none
// of them says it is withheld: a reader who is shown one signal out of three
// otherwise takes it for the whole finding.
function signalsState(shown: number, skipped: boolean): SectionState {
  if (!skipped) {
    return shown === 0 ? "empty" : "ready";
  }
  return shown === 0 ? "withheld" : "partial";
}

type Signal = Readonly<{ text: string; tone: "good" | "warn" | "bad" }>;

// Deterministic, from what the page already read. Each one is a fact the
// reader can check against the cards beside it rather than an assessment.
//
// A rule whose section this reader may not see is SKIPPED and reported as
// skipped, never resolved against the absence: "no next meeting booked" derived
// from a withheld calendar is an assertion about the deal manufactured out of a
// permission boundary, and it is indistinguishable from the real finding.
function derivedSignals(
  view: Person360,
  hidden: Withheld,
  t: ReturnType<typeof useT>,
): Readonly<{ signals: ReadonlyArray<Signal>; skipped: boolean }> {
  const out: Signal[] = [];
  let skipped = hidden.lastTouch;
  const quiet = hidden.lastTouch ? null : daysSince(view.last_inbound_at);
  if (quiet != null && quiet > 14) {
    out.push({
      text: t("person.rail.noReplyDays", { count: quiet }),
      tone: "bad",
    });
  } else if (quiet != null) {
    out.push({
      text: t("person.rail.repliedDaysAgo", { count: quiet }),
      tone: "good",
    });
  }
  if (hidden.commercial) {
    return { signals: out, skipped: true };
  }
  const deal = view.commercial?.deal;
  const committee = view.commercial?.committee?.length ?? 0;
  if (deal && committee === 0) {
    out.push({ text: t("person.rail.singleThreaded"), tone: "warn" });
  }
  // The meeting rule needs BOTH sections — a visible deal for the finding to be
  // about, and a readable calendar to prove nothing is booked on it — so a
  // withheld calendar only costs a signal where there is a deal to book against.
  if (deal && hidden.nextMeeting) {
    skipped = true;
  } else if (deal && !view.next_meeting) {
    out.push({ text: t("person.rail.noMeetingBooked"), tone: "warn" });
  }
  return { signals: out, skipped };
}

// --- Consent and channels --------------------------------------------------

// The action guard, not the proof ledger. It renders even on a thin record,
// because "may I write to this person" is a question with an answer whatever
// else is missing.
//
// Every row answers ONE transport, and it answers reachability before consent.
// A verdict presupposes somewhere to send: reporting "allowed" against mail and
// phone for a contact captured over a chat channel — no address, no number —
// asserted a reachability nothing in the record supports, while the transport
// the CRM can actually reach them on had no row at all.
//
// The channel rows come from `person.reachability`, the record's own read of
// `person_channel_identity` — the same binding the reply path resolves a
// recipient from. They carry the correspondence verdict rather than one of
// their own because the send gate is per PURPOSE: it resolves the person and
// asks the one question, so the transport never enters the answer.
function ConsentAndChannels({
  view,
  guard,
}: Readonly<{ view: Person360; guard: PersonConsentGuard | undefined }>) {
  const t = useT();
  const providerLabel = useProviderLabel();
  const entries = guard?.entries ?? [];
  const correspondence = entries.find((entry) => entry.channel === "email");
  const phone = entries.find((entry) => entry.channel === "phone");
  const hasEmail = (view.person.emails?.length ?? 0) > 0;
  const channels = view.person.reachability ?? [];
  return (
    <Disclosure
      className="pe-sect"
      open
      summary={t("person.rail.consentTitle")}
    >
      <ConsentRow
        icon={<Mail size={15} aria-hidden="true" />}
        label={t("person.rail.email")}
        reachable={hasEmail}
        verdict={correspondence?.verdict}
        unreachableWord={t("person.rail.noEmailAddress")}
      />
      <ConsentRow
        icon={<Phone size={15} aria-hidden="true" />}
        label={t("person.rail.phone")}
        reachable={(view.person.phones?.length ?? 0) > 0}
        verdict={phone?.verdict}
        unreachableWord={t("person.rail.noPhoneNumber")}
      />
      {/* A blocked identity still gets its row, with `reachable: false`: the
          conversation happened, and hiding the transport it happened on would
          answer "can I write to them" by pretending they were never here. */}
      {channels.map((channel) => (
        <ConsentRow
          key={channel.provider}
          icon={interactionIcon("message", 15)}
          label={providerLabel(channel.provider)}
          reachable={channel.reachable}
          verdict={correspondence?.verdict}
          unreachableWord={t("person.rail.channelNotDeliverable")}
        />
      ))}
      {/* The REASON, in the reader's words. A verdict a rep cannot explain to
          the person in front of them is not usable — and one explaining a
          verdict no row above shows explains nothing. */}
      {(hasEmail || channels.some((channel) => channel.reachable)) &&
        correspondence?.reason && (
          <p className="pe-colleague-proof">{correspondence.reason}</p>
        )}
    </Disclosure>
  );
}

// One transport's row. Reachability is a fact about the RECORD and the verdict
// is a fact about consent, and the row states the first before the second: a
// permission to send where there is nowhere to send is not one a rep can act
// on, and colouring it green says they may.
function ConsentRow({
  icon,
  label,
  reachable,
  verdict,
  unreachableWord,
}: Readonly<{
  icon: ReactNode;
  label: string;
  reachable: boolean;
  verdict: string | undefined;
  unreachableWord: string;
}>) {
  const t = useT();
  return (
    <div className="pe-rail-row">
      <span className="pe-rail-label">
        {icon}
        {label}
      </span>
      <span
        className={
          reachable
            ? verdictClass(verdict)
            : "pe-rail-value pe-rail-value-muted"
        }
      >
        {reachable ? consentWord(verdict, t) : unreachableWord}
      </span>
    </div>
  );
}

function verdictClass(verdict: string | undefined): string {
  switch (verdict) {
    case "allowed":
      return "pe-rail-value pe-rail-value-good";
    case "blocked":
      return "pe-rail-value pe-rail-value-warn";
    default:
      return "pe-rail-value pe-rail-value-muted";
  }
}

// --- Recent activity -------------------------------------------------------

// Three condensed items. It never duplicates the raw timeline visible beside
// it — this is the glance, the Activity tab is the ledger.
function RecentActivity({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  // The section's own emptiness is decided BEFORE the rows are defaulted: an
  // absent list and an empty one collapse into the same `[]` here, and that
  // collapse is what turns "you may not read the timeline" into "nothing has
  // ever happened with this contact".
  const withheld = withheldSections(view).activities;
  const rows = (view.activities?.data ?? []).slice(0, 3);
  return (
    <Disclosure
      className="pe-sect"
      open
      summary={t("person.rail.recentActivity")}
    >
      <SurfaceState
        state={bodyState(withheld, rows.length)}
        emptyLabel={t("person.rail.nothingCaptured")}
      >
        {rows.map((row) => (
          <div className="pe-rail-row" key={row.id}>
            <span className="pe-rail-label">{row.subject ?? row.kind}</span>
            <span className="pe-rail-value-muted">
              {sinceWords(row.occurred_at, t)}
            </span>
          </div>
        ))}
      </SurfaceState>
      {/* The rail's own glance leaves the tab's ledger one click away, the
          same "see the rest elsewhere" verb the section's own body carries
          now that there is no panel footer band to hold it. */}
      <Button
        small
        className="pe-rail-more"
        onClick={() => navigate(personTabRoute(view.person.id, "timeline"))}
      >
        {t("person.rail.viewAllActivity")}{" "}
        <ChevronRight size={13} aria-hidden="true" />
      </Button>
    </Disclosure>
  );
}

// --- shared ----------------------------------------------------------------

function Row({
  label,
  value,
  strong,
}: Readonly<{ label: string; value: string; strong?: boolean }>) {
  return (
    <div className="pe-rail-row">
      <span className="pe-rail-label">{label}</span>
      <span className={strong ? "pe-rail-value-good" : "pe-rail-value"}>
        {value}
      </span>
    </div>
  );
}

function daysSince(at: string | null | undefined): number | null {
  if (!at) {
    return null;
  }
  return Math.floor((Date.now() - new Date(at).getTime()) / 86_400_000);
}

function sinceWords(
  at: string | null | undefined,
  t: ReturnType<typeof useT>,
): string {
  const days = daysSince(at);
  if (days == null) {
    return t("person.strip.never");
  }
  if (days <= 0) {
    return t("person.strip.today");
  }
  if (days === 1) {
    return t("person.strip.yesterday");
  }
  return t("person.strip.days", { count: days });
}
