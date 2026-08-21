import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { type ReactNode, useId, useMemo, useRef, useState } from "react";
import { api, FIRST_PAGE } from "../api/client";
import type { components } from "../api/schema";
import { useHoldsAdminRole, useHoldsConsentAdminRole } from "../app/capability";
import {
  Badge,
  Button,
  Card,
  Checkbox,
  EmptyState,
  Field,
  Modal,
  SegmentedControl,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { CardBoundary } from "../design-system/cardboundary";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import { Select, type SelectOption } from "../design-system/select";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatDate } from "../format/format";
import { useNow } from "../format/now";
import { viewerZone } from "../format/timezone";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { humanizeToken } from "./audit";
import {
  LoadMoreButton,
  ProblemError,
  problemMessageOf,
  QueryGate,
  QueryStates,
  throwProblem,
  useMe,
} from "./common";
import {
  EntityRef,
  RosterPartialNote,
  rosterMissLabel,
  useRoster,
  useRosterPartial,
} from "./entityref";
import {
  DSR_STATUS_FACETS,
  type DsrStatus,
  type DsrStatusFacet,
  dsrKindTone,
  endOfDayInZone,
  isOverdue,
  isTerminal,
  nextStatuses,
} from "./privacy.logic";
import "./privacy.css";
import { isOption } from "../app/options";

type DataSubjectRequest = components["schemas"]["DataSubjectRequest"];
type CreateDataSubjectRequest =
  components["schemas"]["CreateDataSubjectRequest"];
type UpdateDataSubjectRequest =
  components["schemas"]["UpdateDataSubjectRequest"];
type User = components["schemas"]["User"];
type DsrKind = CreateDataSubjectRequest["kind"];

// The two settings/privacy surfaces, extracted out of the 1309-line
// settings.tsx (the audit.tsx extraction precedent): the consent-purpose
// catalogue (G-3 adds create — POST /consent-purposes already routed, but
// nothing in this app called it) and the DSR inbox. GET + POST only — there
// is no PATCH or DELETE on /consent-purposes, so a purpose is append-only by
// contract, not by convention; the create form says so up front.

// The DSR closed status machine (consent/dsr.go's dsrTransitions) rejects an
// illegal "<from> → <to>" move with a 422 validation_error whose ONE failing
// field is "status" (writeConsentErr → httperr.Validation("status", "invalid",
// reason)). That is the only field-level validation error this endpoint's
// status changes can produce — the sibling "closing a request needs its
// answer" case fails on "resolution", not "status" — so field "status" on a
// validation_error is an unambiguous signal the request moved on underneath
// us. Every other failure (permission_denied, an infra 500, a network error)
// is a different kind of problem and must never wear that copy.
function isIllegalTransition(problem: unknown): boolean {
  if (!problem || typeof problem !== "object") return false;
  const record = problem as Record<string, unknown>;
  if (record.code !== "validation_error") return false;
  const details = record.details;
  if (!details || typeof details !== "object") return false;
  const errors = (details as Record<string, unknown>).errors;
  if (!Array.isArray(errors)) return false;
  return errors.some(
    (item) =>
      item &&
      typeof item === "object" &&
      (item as Record<string, unknown>).field === "status",
  );
}

// G-3: the purpose-create form — three inputs committed together, so it is the
// BODY of the dialog the registry's "Add purpose" row opens rather than a card
// unfolding inside the card. A stale create error must not outlive the edit
// that could fix it, so every field's onChange clears it first (share.tsx:432's
// dismissGrantError idiom).
function PurposeCreateForm({ onDone }: Readonly<{ onDone: () => void }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [key, setKey] = useState("");
  const [label, setLabel] = useState("");
  const [requiresDoi, setRequiresDoi] = useState(false);

  const create = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/consent-purposes", {
        body: {
          key: key.trim(),
          label: label.trim(),
          requires_double_opt_in: requiresDoi,
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["consent-purposes"] });
      setKey("");
      setLabel("");
      setRequiresDoi(false);
      onDone();
    },
  });

  function dismissCreateError() {
    if (create.isError) {
      create.reset();
    }
  }

  return (
    <div className="form-stack">
      <p className="t-caption purpose-form-warning">
        {t("privacy.purposeAppendOnly")}
      </p>
      <Field label={t("privacy.purposeKey")}>
        {(control) => (
          <TextInput
            {...control}
            value={key}
            onChange={(event) => {
              setKey(event.target.value);
              dismissCreateError();
            }}
          />
        )}
      </Field>
      <Field label={t("privacy.purposeLabel")}>
        {(control) => (
          <TextInput
            {...control}
            value={label}
            onChange={(event) => {
              setLabel(event.target.value);
              dismissCreateError();
            }}
          />
        )}
      </Field>
      <Checkbox
        className="t-caption"
        label={t("privacy.purposeDoi")}
        checked={requiresDoi}
        onChange={(event) => {
          setRequiresDoi(event.target.checked);
          dismissCreateError();
        }}
      />
      {create.isError && (
        <p className="t-caption purpose-form-error">
          {problemMessageOf(create.error, t)}
        </p>
      )}
      <Button
        small
        variant="primary"
        disabled={!key.trim() || !label.trim() || create.isPending}
        onClick={() => create.mutate()}
      >
        {t("privacy.purposeCreate")}
      </Button>
    </div>
  );
}

export function ConsentPurposesCard() {
  const t = useT();
  // The probe itself, not just its answer: every role predicate reads false
  // while /me is in flight, so branching on `!canAdminister` alone would flash
  // the read-only line at an admin on every load.
  const me = useMe();
  const canAdminister = useHoldsConsentAdminRole();
  const addTitleId = useId();
  const [adding, setAdding] = useState(false);
  const query = useQuery({
    queryKey: ["consent-purposes"],
    queryFn: async () => {
      const { data, error } = await api.GET("/consent-purposes");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  // No bottom margin of its own: `.settings-stack` owns the gap between cards.
  return (
    <Panel
      title={t("settings.purposes")}
      // The card's one write affordance rides in the header rather than in a
      // row of its own. A row states a setting and its answer; a create verb is
      // neither, and a row whose LABEL was the button's own words said "Add
      // purpose" twice a hand apart. `titleAction` is the slot for exactly this
      // (panel.tsx), and it keeps the verb above a registry that grows.
      //
      // Authoring a purpose is an admin/ops act, and the registry is on a page
      // every seat opens — so the verb still asks. Rendered unconditionally it
      // offered a form whose submit the server refuses, which is the one thing
      // a governance surface must not do: promise an authority it does not
      // carry. The registry row's own description is where that posture is
      // stated instead.
      titleAction={
        canAdminister ? (
          <Button small onClick={() => setAdding(true)}>
            {t("privacy.addPurpose")}
          </Button>
        ) : undefined
      }
    >
      <PanelBody>
        <p className="settings-panel-sub">{t("settings.purposesSub")}</p>
        <SettingList>
          {/* The registry is the card's subject rather than an answer beside a
              question, so it takes the full width under its naming.

              The read-only posture is this row's DESCRIPTION rather than a
              paragraph of its own between the card's line and the list: dropping
              the write affordance without saying so leaves a rep looking at a
              registry that has no way to grow, and the sentence belongs beside
              the thing it is a posture about. Never a disabled button that
              promises a click. */}
          <SettingRow
            label={t("privacy.purposesRegistry")}
            description={
              me.isSuccess && !canAdminister
                ? t("privacy.purposesReadOnly")
                : undefined
            }
            layout="stack"
            control={
              <QueryGate query={query} empty={(page) => page.data.length === 0}>
                {(page) => (
                  <div className="purpose-badges">
                    {page.data.map((purpose) => (
                      <Badge
                        key={purpose.id}
                        tone={
                          purpose.requires_double_opt_in ? "warn" : undefined
                        }
                      >
                        {purpose.label}
                        {purpose.requires_double_opt_in ? " · DOI" : ""}
                      </Badge>
                    ))}
                  </div>
                )}
              </QueryGate>
            }
          />
        </SettingList>
        <Modal
          open={adding}
          onClose={() => setAdding(false)}
          labelledBy={addTitleId}
        >
          <h2 id={addTitleId} className="t-h2 modal-title">
            {t("privacy.addPurpose")}
          </h2>
          <PurposeCreateForm onDone={() => setAdding(false)} />
        </Modal>
      </PanelBody>
    </Panel>
  );
}

// Matches a proper person-id UUID; an external identifier (email, a partner's
// own reference string) never does, so it stays raw mono text rather than a
// dead EntityRef lookup against a record that was never a person id.
const SUBJECT_UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const DSR_KINDS: readonly DsrKind[] = ["access", "rectify", "erasure"];

// The erasure fulfiller (consent/dsr.go) resolves subject_ref to a person id
// and erases that record — free text there cannot be erased, so an erasure
// request must be opened against a picked person, never typed in by hand.
// No purpose-built person-search endpoint exists yet (offers.tsx's org/product
// pickers are RecordPicker's only other callers today), so this reuses the
// person list's own full-text `q` param, exactly as searchOrganizationCandidates
// reuses /organizations.
async function searchPersonCandidates(
  q: string,
): Promise<RecordPickerCandidate[]> {
  const { data, error } = await api.GET("/people", {
    params: { query: { q, limit: 10 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.map((person) => ({ id: person.id, name: person.full_name }));
}

// G-2: the DSR-open form — kind, subject and deadline committed together, so it
// is the body of the dialog the queue's "New request" row opens, the same shape
// PurposeCreateForm takes above. kind flips the subject field's very shape: an
// erasure locks onto
// a picked person (RecordPicker, uuid subject_ref) so the create form is
// physically incapable of producing the free-text-erasure state the server
// now refuses; access/rectify keep the free-text field the contract's
// "person id or external identifier" wording actually allows.
function NewDsrForm({ onDone }: Readonly<{ onDone: () => void }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<DsrKind>("access");
  const [subjectRef, setSubjectRef] = useState("");
  const [person, setPerson] = useState<RecordPickerCandidate | null>(null);
  const [dueAt, setDueAt] = useState("");
  // The statutory deadline is minted in the OPERATOR's own zone, the same
  // zone the row later renders it back in (PrivacyInboxCard's tz below) —
  // `new Date(dueAt).toISOString()` would instead read the date-only input
  // as UTC midnight, silently rolling the picked day back a day for anyone
  // west of UTC.
  const tz = viewerZone();

  const create = useMutation({
    mutationFn: async () => {
      const body: CreateDataSubjectRequest = {
        kind,
        subject_ref: subjectRef.trim(),
        due_at: endOfDayInZone(dueAt, tz),
      };
      const { data, error } = await api.POST("/data-subject-requests", {
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dsrs"] });
      setKind("access");
      setSubjectRef("");
      setPerson(null);
      setDueAt("");
      onDone();
    },
  });

  function dismissCreateError() {
    if (create.isError) {
      create.reset();
    }
  }

  function changeKind(next: DsrKind) {
    setKind(next);
    // The subject field's meaning changes with kind (a picked person's uuid
    // vs. free text) — carrying either value across the switch would let a
    // stale value from the OTHER shape ride into the request unnoticed.
    setSubjectRef("");
    setPerson(null);
    dismissCreateError();
  }

  return (
    <div className="form-stack">
      <Field label={t("privacy.kind")}>
        {(control) => (
          <Select
            {...control}
            options={DSR_KINDS.map((value) => ({
              value,
              label: humanizeToken(value),
            }))}
            value={kind}
            onChange={(value) => {
              if (isOption(value, DSR_KINDS)) changeKind(value);
            }}
          />
        )}
      </Field>

      {kind === "erasure" ? (
        <div className="field">
          <span className="t-label">{t("privacy.person")}</span>
          <RecordPicker
            label={t("privacy.person")}
            searchTargets={searchPersonCandidates}
            selected={person}
            onPick={(candidate) => {
              setPerson(candidate);
              setSubjectRef(candidate.id);
              dismissCreateError();
            }}
          />
          <p className="t-caption">{t("privacy.erasureNeedsPerson")}</p>
        </div>
      ) : (
        <Field
          label={t("privacy.subjectRef")}
          hint={kind === "access" ? t("privacy.accessManual") : undefined}
        >
          {(control) => (
            <TextInput
              {...control}
              value={subjectRef}
              onChange={(event) => {
                setSubjectRef(event.target.value);
                dismissCreateError();
              }}
            />
          )}
        </Field>
      )}

      <Field label={t("privacy.dueAt")}>
        {(control) => (
          <TextInput
            {...control}
            type="date"
            value={dueAt}
            onChange={(event) => {
              setDueAt(event.target.value);
              dismissCreateError();
            }}
          />
        )}
      </Field>

      {create.isError && (
        <p className="t-caption dsr-error">
          {problemMessageOf(create.error, t)}
        </p>
      )}

      <Button
        small
        variant="primary"
        disabled={!subjectRef.trim() || !dueAt || create.isPending}
        onClick={() => create.mutate()}
      >
        {t("privacy.openRequest")}
      </Button>
    </div>
  );
}

// The status badge tone, keyed on the closed DSR status machine — open carries
// no tone. Keying on the union keeps a status added upstream a compile error
// here rather than a silently untoned badge.
const STATUS_TONE: Record<
  DsrStatus,
  "success" | "warn" | "danger" | undefined
> = {
  open: undefined,
  in_progress: "warn",
  fulfilled: "success",
  rejected: "danger",
};

// nextStatuses(open|in_progress) only ever yields these three targets (the
// TRANSITIONS DAG in privacy.logic.ts never routes to "open"); the fallback
// return keeps this total without a needless fourth i18n key for a status
// that can never reach here.
function transitionLabelKey(status: DsrStatus): MessageKey {
  if (status === "in_progress") return "privacy.inProgress";
  if (status === "fulfilled") return "privacy.fulfil";
  return "privacy.reject";
}

// Who a request can be assigned to, led by the unassigned entry. That entry is
// DISABLED, and it is still an option rather than the select's placeholder: the
// server's update coalesces an omitted assignee onto the stored one, so nothing
// an empty selection sent could unassign anybody — and an entry a reader can
// aim at has to be able to change something. Kept in the list because it is the
// face an unassigned request shows, and the state has to stay legible even
// where it is not actionable. The em dash carries no words to translate.
//
// `current` is the request's own assignee when they are nobody this list offers
// — deactivated out of the roster, sitting past the walk's bound, or an agent
// seat this picker deliberately withholds. Without it the select's value matches
// no option and paints as the unassigned em dash: a DPO would read an erasure
// request that IS assigned as one that is not, and reassign it off the holder
// with a statutory clock running. It leads the list because it is the state the
// field is in, exactly as the unassigned entry does.
function assigneeOptions(
  users: readonly User[],
  current: SelectOption | null,
): SelectOption[] {
  return [
    current ?? { value: "", label: "—", disabled: true },
    ...users.map((user) => ({ value: user.id, label: user.display_name })),
  ];
}

/**
 * The request's own assignee as an option, when they are nobody the picker
 * offers — and null when they are, or when nobody holds it.
 *
 * `members` is the whole roster read and `offered` the filtered list: an agent
 * seat is in the first and never the second, so it can be named by its own name
 * while still not being offered. An id in neither is one the roster could not
 * name at all, and `rosterMissLabel` decides what that is honest to say.
 */
function unofferedAssignee({
  assigneeId,
  offered,
  members,
  roster,
  partial,
  t,
}: Readonly<{
  assigneeId: string | null | undefined;
  offered: readonly User[];
  members: readonly User[];
  roster: Readonly<{ isPending: boolean; isError: boolean }>;
  partial: boolean;
  t: ReturnType<typeof useT>;
}>): SelectOption | null {
  if (!assigneeId || offered.some((member) => member.id === assigneeId)) {
    return null;
  }
  return {
    value: assigneeId,
    label:
      members.find((member) => member.id === assigneeId)?.display_name ??
      rosterMissLabel(roster, partial, t, t("ref.notInRoster")),
    // Disabled for the same reason the unassigned entry is: re-choosing the
    // holder this request already has changes nothing, and an entry a reader can
    // aim at has to be able to change something.
    disabled: true,
  };
}

// One DSR row: collapsed summary + (on click) the case-work panel — subject,
// assignee, resolution, and only the transitions the server's closed status
// machine (consent/dsr.go:58-61) would actually accept. Which row is open is
// the CARD's state, not this row's own — a queue keeps every sibling row and
// the facet bar visible while one case is worked, so `expanded` and its
// toggle arrive as props; useRoster only fetches the workspace roster while
// THIS row is the open one, not for every row on the page.
function DsrRow({
  dsr,
  expanded,
  onToggle,
  nowMs,
  tz,
  locale,
  onFulfilErasure,
}: Readonly<{
  dsr: DataSubjectRequest;
  expanded: boolean;
  onToggle: () => void;
  nowMs: number;
  tz: string;
  locale: Locale;
  onFulfilErasure: (
    dsr: DataSubjectRequest,
    resolution: string,
    // The row's summary toggle, named by id rather than handed over as an
    // element: the erasure confirm lives at the card root and has to find it
    // again AFTER the fulfil, when the transition button that staged it no
    // longer exists to have carried a reference.
    toggleId: string,
  ) => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [resolution, setResolution] = useState(dsr.resolution ?? "");
  const assigneeFieldId = useId();
  const panelId = useId();
  const toggleId = useId();

  // Only fetched while this row's panel is actually open — the roster is the
  // same shared ["users"] cache entry EntityRef and the share picker read.
  const roster = useRoster("user", expanded);
  const rosterPartial = useRosterPartial("user", expanded);
  // The roster hook serves users and teams alike, so narrow to the entries that
  // carry a person's name rather than asserting the shape.
  const members = (roster.data ?? []).flatMap((entry) =>
    "display_name" in entry ? [entry] : [],
  );
  // Agent seats can't hold requireDSRAdmin's unbounded row scope (only a
  // human admission can), so the picker never offers one — same is_agent
  // filter as the share subject picker.
  const assignableUsers = members.filter((member) => !member.is_agent);
  const currentAssignee = unofferedAssignee({
    assigneeId: dsr.assignee_id,
    offered: assignableUsers,
    members,
    roster,
    partial: rosterPartial,
    t,
  });

  const patch = useMutation({
    mutationFn: async (body: UpdateDataSubjectRequest) => {
      const { data, error } = await api.PATCH("/data-subject-requests/{id}", {
        params: { path: { id: dsr.id } },
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dsrs"] });
    },
    // The stale-row race: another officer decided this request first, so the
    // transition this row offered is no longer legal server-side (422). This
    // is NOT approvals' already_decided 409 — re-read via invalidation ONLY
    // for that specific case; an assignee 403 or an infra 500 is not a race,
    // and invalidating for those would just hide the real failure behind a
    // refetch instead of explaining it.
    onError: (error) => {
      const problem = error instanceof ProblemError ? error.problem : null;
      if (problem && isIllegalTransition(problem)) {
        queryClient.invalidateQueries({ queryKey: ["dsrs"] });
      }
    },
  });

  function dismissPatchError() {
    if (patch.isError) {
      patch.reset();
    }
  }

  function submitTransition(next: DsrStatus) {
    // An erasure fulfil is the single most destructive action in the
    // product, so it never goes through this plain PATCH — it routes to the
    // typed-ERASE confirmation modal instead (which also handles the
    // legal-hold 409). The resolution the operator already wrote here must
    // ride along: the closingWithoutAnswer gate above requires one before
    // this button is even clickable, and the modal has no field of its own
    // to collect it again.
    if (dsr.kind === "erasure" && next === "fulfilled") {
      onFulfilErasure(dsr, resolution.trim(), toggleId);
      return;
    }
    const body: UpdateDataSubjectRequest = { status: next };
    const trimmed = resolution.trim();
    // A blank resolution key would still be a value the server writes
    // (coalesce only skips an omitted key, not an empty string) — omit it
    // rather than risk clearing a resolution nothing here actually changed.
    if (trimmed) {
      body.resolution = trimmed;
    }
    patch.mutate(body);
  }

  const overdue = isOverdue(dsr.due_at, dsr.status, nowMs);
  const terminal = isTerminal(dsr.status);
  const patchProblem =
    patch.error instanceof ProblemError ? patch.error.problem : null;
  // Only the illegal-transition race gets the "moved on" copy; any other
  // failure gets the server's own honest explanation instead of a specific
  // claim about a race that never happened.
  const patchErrorMessage = !patch.isError
    ? null
    : patchProblem && isIllegalTransition(patchProblem)
      ? t("privacy.movedOn")
      : problemMessageOf(patch.error, t);

  return (
    <li className="dsr-row">
      <Button
        small
        id={toggleId}
        className="dsr-row-toggle"
        onClick={onToggle}
        aria-expanded={expanded}
        aria-controls={panelId}
      >
        <Badge tone={dsrKindTone(dsr.kind)}>{humanizeToken(dsr.kind)}</Badge>
        <span className="t-mono">{dsr.subject_ref}</span>
        <Badge tone={STATUS_TONE[dsr.status]}>
          {humanizeToken(dsr.status)}
        </Badge>
        <span className="t-small dsr-due">
          {t("settings.due", { date: formatDate(dsr.due_at, locale, tz) })}
        </span>
        {overdue && <Badge tone="danger">{t("privacy.overdue")}</Badge>}
      </Button>
      {expanded && (
        <Card as="div" inset id={panelId} className="dsr-expanded">
          <div className="form-stack">
            <div className="field">
              {SUBJECT_UUID_RE.test(dsr.subject_ref) ? (
                <EntityRef kind="person" id={dsr.subject_ref} />
              ) : (
                <span className="t-mono">{dsr.subject_ref}</span>
              )}
            </div>

            <div className="field">
              <label className="t-label" htmlFor={assigneeFieldId}>
                {t("privacy.assignee")}
              </label>
              <Select
                id={assigneeFieldId}
                options={assigneeOptions(assignableUsers, currentAssignee)}
                value={dsr.assignee_id ?? ""}
                disabled={patch.isPending}
                onChange={(value) => patch.mutate({ assignee_id: value })}
              />
              <p className="t-caption">{t("privacy.assigneeUnassignable")}</p>
              {/* Who this list leaves out is already its subject, so a roster
                  that stopped short of the workspace belongs on the same line
                  rather than being the one omission nobody is told about. */}
              <RosterPartialNote partial={rosterPartial} />
              {patch.isPending && (
                <p className="t-caption">{t("common.saving")}</p>
              )}
            </div>

            {/* The assignee select above and the transition buttons below
                share this one `patch` mutation, and either can fail — a
                closed request still offers reassignment, so this must render
                regardless of `terminal`, not only inside the open-case
                branch below (an assignment failure on a closed request would
                otherwise be invisible). */}
            {/* role="alert": this line is the ONLY report that a transition
                did not land, and `privacy.movedOn` exists precisely to say the
                click a reader just made changed nothing. Rendered silently it
                told nobody — the row's badges do not move on a refused write,
                so a reader who was looking at the buttons saw the same screen
                either way. The paragraph mounts carrying its message, which is
                the case an assertive region is for. */}
            {patchErrorMessage && (
              <p className="t-caption dsr-error" role="alert">
                {patchErrorMessage}
              </p>
            )}

            {terminal ? (
              <p className="t-caption">{t("privacy.closed")}</p>
            ) : (
              <>
                <Field
                  label={t("privacy.resolution")}
                  hint={t("privacy.resolutionRequired")}
                >
                  {(control) => (
                    <Textarea
                      {...control}
                      value={resolution}
                      onChange={(event) => {
                        setResolution(event.target.value);
                        dismissPatchError();
                      }}
                    />
                  )}
                </Field>
                <div className="dsr-actions">
                  {nextStatuses(dsr.status).map((next) => {
                    const closingWithoutAnswer =
                      (next === "fulfilled" || next === "rejected") &&
                      !resolution.trim() &&
                      !dsr.resolution;
                    return (
                      <Button
                        key={next}
                        small
                        disabled={closingWithoutAnswer || patch.isPending}
                        onClick={() => submitTransition(next)}
                      >
                        {t(transitionLabelKey(next))}
                      </Button>
                    );
                  })}
                </div>
              </>
            )}
          </div>
        </Card>
      )}
    </li>
  );
}

// This mutation's ONE possible 409: fulfilling an erasure calls into the
// erasure engine (ErasePerson), and the ONLY thing that engine ever wraps in
// ErrConflict is a person under statutory legal hold — there is no second
// conflict source on this call to confuse it with. So code === "conflict"
// here is an unambiguous legal-hold signal, not a guess (unlike the
// consent-purpose or record-grant 409s elsewhere in this codebase, which
// carry a more specific discriminating code).
function isLegalHold(problem: unknown): boolean {
  if (!problem || typeof problem !== "object") return false;
  return (problem as Record<string, unknown>).code === "conflict";
}

// The single most destructive action in the product: fulfilling an erasure
// permanently wipes a person across the whole system. Follows share.tsx's
// revoke-confirm id-in-state pattern — ONE modal at the card root (never one
// per row), gated by a typed "ERASE" rather than a plain confirm click. A
// legal-hold 409 is a documented, lawful refusal (Art. 17(3)(b)), not a
// malfunction — it gets its own honest copy ahead of the generic fallback,
// same branch-before-generic shape as isIllegalTransition above.
function FulfilErasureModal({
  dsr,
  resolution,
  onClose,
  returnFocusTo,
}: Readonly<{
  dsr: DataSubjectRequest | null;
  resolution: string;
  onClose: () => void;
  // Passed through to the confirm: a fulfilled request is terminal, so the whole
  // actions branch this was opened from — the button included — is gone by the
  // time focus comes back.
  returnFocusTo: () => HTMLElement | null;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [typed, setTyped] = useState("");

  // Both the staged request and the operator's resolution arrive as the
  // mutation's variable rather than through this closure. react-query re-arms
  // a mutation's options in a passive effect, so a confirm landing between the
  // commit that stages a request and that effect runs the previous render's
  // function. On the most destructive action in the product that matters twice
  // over: read through a stale closure, `dsr` is null and the erasure refuses,
  // and `resolution` is whatever the operator had typed one render ago.
  const patch = useMutation({
    mutationFn: async (
      fulfilment: Readonly<{ request: DataSubjectRequest; resolution: string }>,
    ) => {
      const body: UpdateDataSubjectRequest = { status: "fulfilled" };
      // Same omit-if-blank rule as the row's own plain PATCH above: a blank
      // resolution key would still be a value the server writes over
      // whatever it already had stored, so it only rides along when there is
      // something to write.
      if (fulfilment.resolution.trim()) {
        body.resolution = fulfilment.resolution.trim();
      }
      const { data, error } = await api.PATCH("/data-subject-requests/{id}", {
        params: { path: { id: fulfilment.request.id } },
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: async () => {
      // The re-read queue FIRST, then the dialog: closing it hands focus back to
      // the row's summary, and a summary still reading "open" would announce the
      // state this erasure just ended.
      await queryClient.invalidateQueries({ queryKey: ["dsrs"] });
      setTyped("");
      onClose();
    },
    // The same stale-row race DsrRow's own plain PATCH already handles: some
    // other officer (or this same operator, from another tab) decided this
    // request first, so the fulfil this modal was staged against is no
    // longer legal server-side. Re-read the queue so the row behind this
    // modal reflects what actually happened — retrying the confirm here
    // could only 422 again the same way.
    onError: (error) => {
      const errProblem = error instanceof ProblemError ? error.problem : null;
      if (errProblem && isIllegalTransition(errProblem)) {
        queryClient.invalidateQueries({ queryKey: ["dsrs"] });
      }
    },
  });

  function close() {
    onClose();
    setTyped("");
    patch.reset();
  }

  const problem =
    patch.error instanceof ProblemError ? patch.error.problem : null;
  const held = problem !== null && isLegalHold(problem);
  const movedOn = problem !== null && isIllegalTransition(problem);
  // A legal hold and a stale transition each get their own explanation
  // ahead of the generic fallback — neither is a mistake a retry could fix,
  // so ConfirmModal's generic inline-error slot (built for a validation
  // mistake) is reserved for everything else.
  const errorMessage =
    patch.isError && !held && !movedOn
      ? problemMessageOf(patch.error, t)
      : null;

  return (
    <ConfirmModal
      open={dsr !== null}
      onClose={close}
      title={t("privacy.fulfilErasureTitle")}
      confirmLabel={t("privacy.erasureConfirm")}
      confirmVariant="danger"
      // Once the server has reported the hold OR that the request moved on,
      // retrying can only fail the same way again — neither can be resolved
      // from this modal, so the confirm stays disabled until the operator
      // closes it and re-opens against the request's current state.
      confirmDisabled={
        typed.trim().toUpperCase() !== "ERASE" || held || movedOn
      }
      onConfirm={() => dsr && patch.mutate({ request: dsr, resolution })}
      pending={patch.isPending}
      error={errorMessage}
      returnFocusTo={returnFocusTo}
    >
      <p>{t("privacy.erasureIrreversible")}</p>
      <div className="field dsr-erase-field">
        <label className="t-label" htmlFor="dsr-type-erase">
          {t("privacy.typeErase")}
        </label>
        <TextInput
          id="dsr-type-erase"
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
        />
      </div>
      {/* The one spelling of "what this surface says about itself". Both of
          these were a hand-rolled bordered panel — `.dsr-legal-hold`, at its
          own padding and its own radius — which is a Callout with a different
          name and a second set of numbers to keep in step. */}
      {held && (
        <Callout tone="danger" live="alert" className="dsr-refusal">
          <p>{t("privacy.legalHold")}</p>
        </Callout>
      )}
      {movedOn && (
        <Callout tone="danger" live="alert" className="dsr-refusal">
          <p>{t("privacy.movedOn")}</p>
        </Callout>
      )}
    </ConfirmModal>
  );
}

export function PrivacyInboxCard() {
  const t = useT();
  const { locale } = useLocale();
  // useNow is the only clock touching rendering (format/now.ts) — isOverdue
  // itself stays pure and takes the epoch ms this hook produces.
  const nowMs = useNow(60_000);
  // FIX-1: due_at is a statutory deadline. A hardcoded zone shows the wrong
  // calendar day to anyone outside it — the viewer's own resolved IANA zone
  // is the only honest signal for "what date does THIS reader see"
  // (share.tsx:290's precedent for the same problem on grant expiry).
  const tz = viewerZone();
  const [facet, setFacet] = useState<DsrStatusFacet>("all");
  // One case open at a time: expandedId lives here (not per-row) so opening
  // a second row's panel closes the first — the queue itself (sibling rows,
  // the facet bar) stays on screen throughout; an officer working a case
  // never loses sight of what else is waiting.
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const createTitleId = useId();
  const [creating, setCreating] = useState(false);
  // Which request is staged for the destructive fulfil, not per-row — same
  // id-in-state shape as share.tsx's revokingId, so ONE modal lives at the
  // card root instead of one per row. Carries the resolution the row already
  // had written, since the modal itself has no field to collect it again.
  const [fulfilling, setFulfilling] = useState<{
    dsr: DataSubjectRequest;
    resolution: string;
  } | null>(null);
  // The staged row's summary toggle, remembered outside that state because the
  // confirm resolves its focus target as it CLOSES — the moment `fulfilling` is
  // already back to null. The toggle survives the fulfil and then reads the
  // request's new status, which is what makes it the right landing place.
  const stagedRowToggle = useRef<string | null>(null);

  // The queue is the admin's: its rows name data subjects who exercised an
  // Art. 15/17 right, so the read is gated rather than merely rendered. The
  // fetch is disabled for anyone else, which keeps a non-admin who reaches the
  // tab for its consent registry from issuing a call that only 403s.
  const isAdmin = useHoldsAdminRole();
  // The probe itself, not only its answer. useHoldsAdminRole reads the roles
  // off the /me cache, so it is false while that read is in flight — and
  // branching on `!isAdmin` alone flashed "the subject queue is admin only" at
  // every administrator, on every load of this tab, until the session landed.
  const me = useMe();

  // The facet is server-side (part of the queryKey and the query param), not
  // a client re-slice of one big page — a re-slice would hide rows the
  // server never told the pager about, breaking `has_more`/`next_cursor`
  // (the house rule at history.tsx:258).
  const query = useInfiniteQuery({
    queryKey: ["dsrs", facet],
    enabled: isAdmin,
    initialPageParam: FIRST_PAGE,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/data-subject-requests", {
        params: {
          query: {
            limit: 20,
            ...(facet !== "all" ? { status: facet } : {}),
            ...(pageParam ? { cursor: pageParam } : {}),
          },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });

  // Both are memoised against the 60-second clock above: `useNow` re-renders
  // this card every minute for the overdue badges, and without these the tick
  // also re-flattened every loaded page and rebuilt the facet labels — the
  // second of which then handed SegmentedControl a new object identity a
  // minute at a time, for a set of five words that never change.
  const pages = query.data?.pages;
  const rows = useMemo(
    () => pages?.flatMap((page) => page.data) ?? [],
    [pages],
  );
  const facetLabels = useMemo(
    () =>
      Object.fromEntries(
        DSR_STATUS_FACETS.map((value) => [
          value,
          value === "all" ? t("privacy.facetAll") : humanizeToken(value),
        ]),
      ) as Record<DsrStatusFacet, string>,
    [t],
  );

  // Honest state matrix (§3a): pending/error stay identical to every other
  // list here; filtering happens server-side so an empty page after a facet
  // change is a real "nothing matches", not a client-side hide.
  let body: ReactNode;
  if (!isAdmin) {
    // Withheld rather than absent: the card keeps its place on a tab an ops
    // seat reaches for the consent registry, and says why it is empty. An
    // absent card there would read as "no requests", which is a different
    // claim entirely.
    //
    // Behind the probe, so this states a settled denial and not the absence of
    // an answer — while /me is in flight nobody holds any role yet.
    body = (
      <QueryGate query={me}>
        {() => (
          <EmptyState>
            <p className="t-small">{t("privacy.inboxAdminOnly")}</p>
          </EmptyState>
        )}
      </QueryGate>
    );
  } else {
    // The shared spelling of the loading and failure rungs, rather than a third
    // hand-rolled copy: the placeholder announces itself as busy, and the
    // failure is an assertive live region carrying the server's own explanation
    // beside the retry. The hand-rolled pair said neither out loud, and it
    // measured its own gaps in inline style objects.
    body = (
      <QueryStates query={query}>
        {rows.length === 0 ? (
          <EmptyState>{t("common.empty")}</EmptyState>
        ) : (
          <>
            <ul className="dsr-list">
              {rows.map((dsr) => (
                <DsrRow
                  key={dsr.id}
                  dsr={dsr}
                  expanded={expandedId === dsr.id}
                  onToggle={() =>
                    setExpandedId((current) =>
                      current === dsr.id ? null : dsr.id,
                    )
                  }
                  nowMs={nowMs}
                  tz={tz}
                  locale={locale}
                  onFulfilErasure={(dsr, resolution, toggleId) => {
                    stagedRowToggle.current = toggleId;
                    setFulfilling({ dsr, resolution });
                  }}
                />
              ))}
            </ul>
            <LoadMoreButton query={query} />
          </>
        )}
      </QueryStates>
    );
  }

  return (
    <Panel
      title={t("settings.privacy")}
      // The verb rides in the header, above a queue that is as long as the queue
      // is: as a row it moved every time a request arrived, and its label was
      // the button's own words repeated. Opening a request is a kind, a subject
      // and a statutory deadline committed together, so the header keeps the
      // verb and the dialog keeps the form.
      titleAction={
        <Button small onClick={() => setCreating(true)}>
          {t("privacy.newRequest")}
        </Button>
      }
    >
      <PanelBody>
        <p className="settings-panel-sub">{t("settings.privacySub")}</p>
        {/* One card's throw stays inside one card: this body renders a queue
            of subject requests straight off the wire, and without a boundary
            a single malformed row costs the reader the whole tab and the rail
            they would have left by. */}
        <CardBoundary>
          <SettingList>
            {/* The queue IS the subject, so it takes the full width — with its
                own facet bar, because filtering belongs to the list it filters
                and not to a row of its own. It stays a queue that expands in
                place: an officer working one case keeps every sibling row and
                the facet bar in sight, which a dialog would take away. */}
            <SettingRow
              label={t("privacy.queue")}
              layout="stack"
              control={
                <div className="dsr-queue">
                  {/* .filter-tabs puts the gap below the tabs so it holds for
                      every body state (rows, empty, loading), not just a
                      populated list. */}
                  <div className="filter-tabs">
                    <SegmentedControl
                      options={DSR_STATUS_FACETS}
                      value={facet}
                      onChange={setFacet}
                      labels={facetLabels}
                    />
                  </div>
                  {body}
                </div>
              }
            />
          </SettingList>
          <Modal
            open={creating}
            onClose={() => setCreating(false)}
            labelledBy={createTitleId}
          >
            <h2 id={createTitleId} className="t-h2 modal-title">
              {t("privacy.newRequest")}
            </h2>
            <NewDsrForm onDone={() => setCreating(false)} />
          </Modal>
          <FulfilErasureModal
            dsr={fulfilling?.dsr ?? null}
            resolution={fulfilling?.resolution ?? ""}
            onClose={() => setFulfilling(null)}
            returnFocusTo={() => {
              const id = stagedRowToggle.current;
              return id ? document.getElementById(id) : null;
            }}
          />
        </CardBoundary>
      </PanelBody>
    </Panel>
  );
}
