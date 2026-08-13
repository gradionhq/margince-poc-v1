import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Mail, Phone } from "lucide-react";
import { useCallback, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
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
import { useT } from "../i18n";
import { problemMessageOf, throwProblem, useSorMode } from "./common";
import { consentWord } from "./personstrip";

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
        <ConsentAndChannels guard={guard} />
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
    params: { path: { id: person.id }, ...ifMatch(person.version) },
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
    return t("record.archivedReadOnly");
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
async function patchEmployment(
  employment: Employment,
  personId: string,
  body: UpdateRelationshipRequest,
): Promise<void> {
  const version = await fetchEmploymentVersion(employment, personId);
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

function todayDate(): string {
  return new Date().toISOString().slice(0, 10);
}

// The three writes the Companies section makes, sharing one invalidation:
// person360 is what this section itself reads its rows from, and personBrief
// comes with it because the brief's first sentence names the employer.
function useEmploymentActions(personId: string) {
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
      patchEmployment(employment, personId, { ended_at: todayDate() }),
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
  return { create, end, remove };
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
    (a, b) => Number(b.is_current_primary) - Number(a.is_current_primary),
  );
  // Every org this person already has a live edge to — the 360 projection
  // drops an edge the moment it is removed, so this list IS the live set,
  // nothing further to filter. AddEmploymentModal excludes these from its
  // own picker so a rep cannot draw a second edge to a company already on
  // this list.
  const connectedOrgIds = employments.map(
    (employment) => employment.organization_id,
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
      {employments.length === 0 && (
        <p className="pe-prose">{t("person.rail.noEmployment")}</p>
      )}
      {employments.map((employment) => (
        <EmploymentRow
          key={employment.relationship_id}
          employment={employment}
          personId={person.id}
          canEdit={canEdit}
          readOnlyReason={readOnlyReason}
          actions={actions}
          onRemove={() => setRemoving(employment)}
        />
      ))}
      <AddEmploymentModal
        open={adding}
        onClose={() => setAdding(false)}
        personId={person.id}
        create={actions.create}
        excludedOrgIds={connectedOrgIds}
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
  personId,
  canEdit,
  readOnlyReason,
  actions,
  onRemove,
}: Readonly<{
  employment: Employment;
  personId: string;
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
          {employment.is_current_primary && (
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
              patchEmployment(employment, personId, { role: next || null })
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
      {ending && actions.end.isError && (
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
}: Readonly<{
  open: boolean;
  onClose: () => void;
  personId: string;
  create: EmploymentActions["create"];
  // Organizations this person already has a live employment edge to — the
  // picker refuses to offer a second edge to the same company, since only
  // a duplicated current-primary is refused server-side.
  excludedOrgIds: ReadonlyArray<string>;
}>) {
  const t = useT();
  const headingId = useId();
  const [org, setOrg] = useState<RecordPickerCandidate | null>(null);
  const [role, setRole] = useState("");
  const [isCurrent, setIsCurrent] = useState(false);
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
    setIsCurrent(false);
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
                source: "ui",
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
      <Row
        label={t("person.rail.direction")}
        value={twoWay ? t("person.rail.twoWay") : t("person.rail.oneSided")}
      />
      <Row label={t("person.rail.lastReply")} value={sinceWords(inbound, t)} />
      <Row
        label={t("person.rail.coverage")}
        value={
          colleagues === 1
            ? t("person.rail.colleagueOne")
            : t("person.rail.colleagues", { count: colleagues })
        }
      />
      <Row label={t("person.rail.trend")} value={trendWord(view, t)} />
      <div className="pe-pulse-overall">
        <Row
          label={t("person.rail.overall")}
          value={overallWord(view, t)}
          strong
        />
      </div>
    </Disclosure>
  );
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
      {colleagues.length === 0 && (
        <p className="pe-prose">{t("person.rail.nobodyYet")}</p>
      )}
      {colleagues.slice(0, 3).map((colleague) => (
        <div className="pe-colleague" key={colleague.user_id}>
          <Avatar name={colleague.display_name} />
          <span>
            <span className="pe-colleague-name">{colleague.display_name}</span>
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
    </Disclosure>
  );
}

// --- Signals and risks -----------------------------------------------------

function SignalsAndRisks({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const signals = derivedSignals(view, t);
  return (
    <Disclosure className="pe-sect" open summary={t("person.rail.signals")}>
      {signals.length === 0 && (
        <p className="pe-prose">{t("person.rail.noSignals")}</p>
      )}
      {signals.map((signal) => (
        <div className="pe-signal" key={signal.text}>
          <span className={`pe-dot pe-dot-${signal.tone}`} />
          <span>{signal.text}</span>
        </div>
      ))}
    </Disclosure>
  );
}

// Deterministic, from what the page already read. Each one is a fact the
// reader can check against the cards beside it rather than an assessment.
function derivedSignals(
  view: Person360,
  t: ReturnType<typeof useT>,
): ReadonlyArray<{ text: string; tone: "good" | "warn" | "bad" }> {
  const out: Array<{ text: string; tone: "good" | "warn" | "bad" }> = [];
  const quiet = daysSince(view.last_inbound_at);
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
  const committee = view.commercial?.committee?.length ?? 0;
  if (view.commercial?.deal && committee === 0) {
    out.push({ text: t("person.rail.singleThreaded"), tone: "warn" });
  }
  if (!view.next_meeting && view.commercial?.deal) {
    out.push({ text: t("person.rail.noMeetingBooked"), tone: "warn" });
  }
  return out;
}

// --- Consent and channels --------------------------------------------------

// The action guard, not the proof ledger. It renders even on a thin record,
// because "may I write to this person" is a question with an answer whatever
// else is missing.
function ConsentAndChannels({
  guard,
}: Readonly<{ guard: PersonConsentGuard | undefined }>) {
  const t = useT();
  const entries = guard?.entries ?? [];
  const email = entries.find((entry) => entry.channel === "email");
  const phone = entries.find((entry) => entry.channel === "phone");
  return (
    <Disclosure
      className="pe-sect"
      open
      summary={t("person.rail.consentTitle")}
    >
      <div className="pe-rail-row">
        <span className="pe-rail-label">
          <Mail size={15} aria-hidden="true" />
          {t("person.rail.email")}
        </span>
        <span className={verdictClass(email?.verdict)}>
          {consentWord(email?.verdict, t)}
        </span>
      </div>
      <div className="pe-rail-row">
        <span className="pe-rail-label">
          <Phone size={15} aria-hidden="true" />
          {t("person.rail.phone")}
        </span>
        <span className={verdictClass(phone?.verdict)}>
          {consentWord(phone?.verdict, t)}
        </span>
      </div>
      {/* The REASON, in the reader's words. A verdict a rep cannot explain to
          the person in front of them is not usable. */}
      {email?.reason && <p className="pe-colleague-proof">{email.reason}</p>}
    </Disclosure>
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
  const rows = (view.activities?.data ?? []).slice(0, 3);
  return (
    <Disclosure
      className="pe-sect"
      open
      summary={t("person.rail.recentActivity")}
    >
      {rows.length === 0 && (
        <p className="pe-prose">{t("person.rail.nothingCaptured")}</p>
      )}
      {rows.map((row) => (
        <div className="pe-rail-row" key={row.id}>
          <span className="pe-rail-label">{row.subject ?? row.kind}</span>
          <span className="pe-rail-value-muted">
            {sinceWords(row.occurred_at, t)}
          </span>
        </div>
      ))}
      {/* The rail's own glance leaves the tab's ledger one click away, the
          same "see the rest elsewhere" verb the section's own body carries
          now that there is no panel footer band to hold it. */}
      <span className="pe-rail-more">
        {t("person.rail.viewAllActivity")}{" "}
        <ChevronRight size={13} aria-hidden="true" />
      </span>
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
