// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Link2,
  ShieldCheck,
  User as UserIcon,
  Users as UsersIcon,
} from "lucide-react";
import { useId, useMemo, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import type { EntityKind } from "../app/entity";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  SearchField,
  SegmentedControl,
  Textarea,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Select } from "../design-system/select";
import { formatDate } from "../format/format";
import { viewerZone } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  problemCodeOf,
  problemMessageOf,
  QueryGate,
  throwProblem,
} from "./common";
import {
  EntityRef,
  RosterPartialNote,
  useRoster,
  useRosterPartial,
} from "./entityref";
import "./share.css";

// AS-3/4/5 — the record-share screen (A52/ADR-0039): grant a user/team
// read/write on exactly this one record, list who currently has manual
// access to it, revoke a grant. The base (owner/team/all) scope is NOT
// rendered here — this is only the *manual* grants layered on top of it
// (per listRecordGrants' description). The 🟡 agent-proposed-grant card
// from the mockup is deliberately deferred — this screen is the human
// compose/list/revoke path only.

type RecordGrant = components["schemas"]["RecordGrant"];
type CreateRecordGrantRequest =
  components["schemas"]["CreateRecordGrantRequest"];
type Access = CreateRecordGrantRequest["access"];
// The share screen serves the record kinds the app has a page for, which is
// every kind the grant contract admits: a grant on a record the reader could
// not open afterwards would be a door to nowhere.
type RecordType = Extract<CreateRecordGrantRequest["record_type"], EntityKind>;
type User = components["schemas"]["User"];
type Team = components["schemas"]["Team"];

type RosterSubject = {
  id: string;
  name: string;
  note: string;
  kind: "user" | "team";
};

/**
 * What a submit does to a grant the subject ALREADY holds.
 *
 * `POST /record-grants` is idempotent on
 * `(record_type, record_id, subject_type, subject_id)` and a re-assert
 * RESTATES the grant — access, expires_at and reason all take the new
 * request's values (crm.yaml, createRecordGrant) — so the screen offers an
 * already-granted subject rather than refusing one. It then owes the reader
 * the difference between the four things that press can mean: `lower` is the
 * only one that takes authority away, and the only one asked about first.
 */
type ReassertKind = "raise" | "lower" | "amend" | "unchanged";

type DraftFields = Readonly<{
  subject: RosterSubject;
  access: Access;
  expiresAt?: string;
  reason?: string;
}>;

/**
 * A submit against a subject who already holds a grant. `heldAccess` is
 * REQUIRED here rather than optional on one shared shape: the downgrade
 * confirm has to name the level being taken away, and a field that might be
 * missing would put a fallback level in that sentence — a made-up number in
 * the one place the reader is deciding something.
 */
type ReassertDraft = DraftFields &
  Readonly<{ change: ReassertKind; heldAccess: Access }>;

/**
 * One submit, in full: the subject, the three fields a re-assert restates,
 * and what this press does to whatever is already there.
 *
 * Built in the render whose button the reader pressed and carried into the
 * mutation as its variable, so nothing here can be older than that control —
 * and so the downgrade confirm, which submits from a LATER render, still sends
 * the choices the reader actually made.
 */
type GrantDraft = (DraftFields & Readonly<{ change: "new" }>) | ReassertDraft;

/**
 * The identity of a grant subject, for keying one against another.
 *
 * A subject is a user OR a team, and the two are separate id spaces — nothing
 * stops a team from carrying the same uuid as a user. So the type is part of
 * the identity, not decoration on it: keyed by id alone, one subject would
 * wear the other's level on their picker row, and a first grant would be
 * measured against — and reported as a restatement of — a grant nobody holds.
 */
function subjectKey(
  subjectType: RecordGrant["subject_type"],
  subjectId: string,
): string {
  return `${subjectType}:${subjectId}`;
}

function reassertKind(held: RecordGrant, next: DraftFields): ReassertKind {
  if (held.access !== next.access) {
    return next.access === "write" ? "raise" : "lower";
  }
  // Same level, so nothing about what the subject CAN DO moves — but the
  // re-assert still rewrites the other two fields, and a press that resets an
  // expiry or replaces the recorded reason is not a no-op. Only all three
  // matching is.
  const sameExpiry = (held.expires_at ?? null) === (next.expiresAt ?? null);
  const sameReason = (held.reason ?? "") === (next.reason ?? "");
  return sameExpiry && sameReason ? "unchanged" : "amend";
}

const RECORD_TYPES: readonly RecordType[] = [
  "person",
  "organization",
  "deal",
  "lead",
  "project",
];

function isRecordType(value: string): value is RecordType {
  return (RECORD_TYPES as readonly string[]).includes(value);
}

// The per-screen "Share" affordance, extracted from four verbatim copies that
// lived inline in the person/organization/deal/lead 360 action clusters
// (mirrors EditAction/ArchiveAction — a thin prop component owning its label
// and its navigation, nothing else). recordType is the narrow union, so a
// screen can't wire a share link to a record kind the route can't resolve.
export function ShareAction({
  recordType,
  recordId,
  disabledReasonId,
}: Readonly<{
  recordType: RecordType;
  recordId: string;
  // Why this action is unavailable, when it is. STATE-4a settles the
  // absent-vs-disabled question by CAUSE: a control blocked by STATE
  // rather than permission — an archived record — stays visible and
  // disabled WITH the reason, because the reason is the information and
  // hiding the control hides a fact the reader needs.
  disabledReasonId?: string;
}>) {
  const t = useT();
  return (
    <Button
      reasonId={disabledReasonId}
      small
      data-testid="share-record"
      onClick={() =>
        navigate({ screen: "share", id: recordType, id2: recordId })
      }
    >
      {t("record.share")}
    </Button>
  );
}

// day-count → i18n key, matching the mockup's expiry select (0/1/7/30).
const EXPIRY_OPTIONS: { days: number; key: MessageKey }[] = [
  { days: 0, key: "share.expiry.none" },
  { days: 1, key: "share.expiry.day" },
  { days: 7, key: "share.expiry.week" },
  { days: 30, key: "share.expiry.month" },
];

function expiresAtFor(days: number): string | undefined {
  if (days <= 0) {
    return undefined;
  }
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString();
}

// One word for a level, wherever the screen names one: the picker's own
// control, the who-has-access list, and the sentence a downgrade asks. A
// level worded one way in the list and another in the dialog reads as two
// different permissions.
function accessLabel(level: Access, t: ReturnType<typeof useT>): string {
  return t(level === "write" ? "share.access.write" : "share.access.read");
}

// Marks a 403 whose code is `approval_required` (createRecordGrant/
// revokeRecordGrant's 🟡 gate) so the render branch can show the honest
// "queued for approval" copy instead of the raw problem detail.
class ApprovalRequiredError extends Error {}

async function fetchGrants(
  recordType: RecordType,
  recordId: string,
): Promise<RecordGrant[]> {
  const { data, error } = await api.GET("/record-grants", {
    params: {
      query: {
        record_type: recordType,
        record_id: recordId,
        limit: 100,
      },
    },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data;
}

// recordType arrives as the raw 3rd URL segment (App.tsx passes it straight
// through). Guard it before rendering the screen: an unknown kind gets an
// honest empty state, never a share form wired to a record type the contract
// and RLS can't resolve. useT() is the only hook here, so the early return is
// rules-of-hooks-safe; all the query/mutation hooks live in ShareScreenBody,
// which mounts only for a valid kind.
export function ShareScreen({
  recordType,
  recordId,
}: Readonly<{ recordType: string; recordId: string }>) {
  const t = useT();
  if (!isRecordType(recordType)) {
    return (
      <div className="wrap">
        <EmptyState>{t("share.unknownRecord")}</EmptyState>
      </div>
    );
  }
  return <ShareScreenBody recordType={recordType} recordId={recordId} />;
}

// A person-vs-team affordance for every subject this screen names. The picker
// rows and the who-has-access list otherwise show a bare name with no cue to
// its kind, so a Lucide glyph carries it — a single silhouette for a person, a
// group for a team — labelled for assistive tech (the glyphs alone aren't).
function SubjectKindIcon({
  kind,
  t,
}: Readonly<{ kind: "user" | "team"; t: ReturnType<typeof useT> }>) {
  const label = t(kind === "team" ? "share.kindTeam" : "share.kindPerson");
  return kind === "team" ? (
    <UsersIcon className="share-kind-icon" aria-label={label} />
  ) : (
    <UserIcon className="share-kind-icon" aria-label={label} />
  );
}

// The subject-picker rows, shared between the normal render path and the
// partial-roster-failure path (one roster query succeeded, the other
// didn't) — kept as one function so a future field change on a row only
// needs one edit.
//
// A subject who already holds a grant is SELECTABLE, and their row says which
// level they hold. Disabling the row (what it did before the API became
// idempotent on the grant tuple) left one way to change a colleague's level:
// revoke it and start again — and it said "already has a grant" without
// saying which, so the reader could not tell an upgrade from a downgrade
// before pressing anything.
function renderSubjectList(
  candidates: RosterSubject[],
  held: ReadonlyMap<string, RecordGrant>,
  selected: RosterSubject | null,
  t: ReturnType<typeof useT>,
  onPick: (candidate: RosterSubject) => void,
) {
  return (
    <ul className="share-subject-list">
      {candidates.map((candidate) => {
        const key = subjectKey(candidate.kind, candidate.id);
        const heldGrant = held.get(key);
        return (
          <li key={key}>
            <Button
              className="share-subject-row"
              aria-pressed={
                selected !== null &&
                subjectKey(selected.kind, selected.id) === key
              }
              onClick={() => onPick(candidate)}
            >
              <span className="share-subject-name">
                <SubjectKindIcon kind={candidate.kind} t={t} />
                <span>{candidate.name}</span>
              </span>
              <span className="share-subject-held">
                {heldGrant && (
                  <Badge
                    tone={heldGrant.access === "write" ? "accent" : undefined}
                  >
                    {t(
                      heldGrant.access === "write"
                        ? "share.holdsWrite"
                        : "share.holdsRead",
                    )}
                  </Badge>
                )}
                <span className="share-subject-note">{candidate.note}</span>
              </span>
            </Button>
          </li>
        );
      })}
    </ul>
  );
}

// The whole "who can I share with" picker body — loading/error/empty/list —
// pulled out of ShareScreenBody (CodeRabbit [16] pushed its cognitive
// complexity over the lint budget). Owns nothing but the render ladder; all
// state (term/subject) and the roster queries stay in the parent.
function RosterPicker({
  usersQuery,
  teamsQuery,
  partial,
  filteredRoster,
  held,
  subject,
  t,
  onPick,
}: Readonly<{
  usersQuery: { isPending: boolean; isError: boolean; refetch: () => unknown };
  teamsQuery: { isPending: boolean; isError: boolean; refetch: () => unknown };
  // Whether either roster stopped short of the workspace. A subject nothing
  // here read cannot be granted access, and a picker silent about that reads as
  // the complete list of who there is to share with.
  partial: boolean;
  filteredRoster: RosterSubject[];
  // The grant each subject already holds on this record, by `subjectKey`.
  held: ReadonlyMap<string, RecordGrant>;
  subject: RosterSubject | null;
  t: ReturnType<typeof useT>;
  onPick: (candidate: RosterSubject) => void;
}>) {
  // Both roster fetches collapse a failure to `[]` in the caller's `roster`
  // (so the picker never crashes), which would otherwise make a failed
  // request indistinguishable from a workspace with zero shareable subjects.
  // Gate explicitly on loading/error first — the empty picker only renders
  // once both queries have actually succeeded with no subjects.
  if (usersQuery.isPending || teamsQuery.isPending) {
    return (
      <p className="t-caption" data-testid="share-roster-loading">
        {t("share.rosterLoading")}
      </p>
    );
  }
  if (usersQuery.isError || teamsQuery.isError) {
    return (
      <div data-testid="share-roster-error">
        <p className="t-caption share-error">
          {usersQuery.isError && teamsQuery.isError
            ? t("share.rosterErrorBoth")
            : usersQuery.isError
              ? t("share.rosterErrorUsers")
              : t("share.rosterErrorTeams")}
        </p>
        <Button
          small
          style={{ marginTop: 6 }}
          onClick={() => {
            if (usersQuery.isError) usersQuery.refetch();
            if (teamsQuery.isError) teamsQuery.refetch();
          }}
        >
          {t("common.retry")}
        </Button>
        {/* A partial failure still lets the subject that DID load be picked
            — the error is informational, not a hard block. */}
        {filteredRoster.length > 0 &&
          renderSubjectList(filteredRoster, held, subject, t, onPick)}
      </div>
    );
  }
  if (filteredRoster.length === 0) {
    return (
      <>
        <p className="t-caption" data-testid="share-roster-empty">
          {t("share.rosterEmpty")}
        </p>
        {/* "Nobody matches" over a roster that stopped early is the reader
            being told the subject they are looking for does not exist, on the
            strength of pages nothing read. */}
        <RosterPartialNote partial={partial} />
      </>
    );
  }
  return (
    <>
      {renderSubjectList(filteredRoster, held, subject, t, onPick)}
      <RosterPartialNote partial={partial} />
    </>
  );
}

function ShareScreenBody({
  recordType,
  recordId,
}: Readonly<{ recordType: RecordType; recordId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  // Grant expiry must read in the viewer's own timezone, not a hardcoded
  // one — the browser's resolved IANA zone is the honest signal for "what
  // calendar date does this viewer see".
  const zone = viewerZone();
  const queryClient = useQueryClient();
  const headingId = useId();
  // Where focus lands when a dialog closes on a control that no longer exists.
  // Both dialogs here destroy their own trigger on success — a revoked grant's
  // row leaves the roster, and a downgrade clears the picker that opened it —
  // so without this focus falls to the document body and a keyboard reader
  // starts the surface over. The subject field is the one control on this page
  // that is always present, which is what makes it the honest landing place.
  const returnFocusToSubject = () =>
    document.getElementById(`${headingId}-subject`);
  const grantsKey = ["record-grants", recordType, recordId];

  const grantsQuery = useQuery({
    queryKey: grantsKey,
    queryFn: () => fetchGrants(recordType, recordId),
  });

  // Shares EntityRef's exact cache entries ([ "users" ] / [ "teams" ]) —
  // one roster fetch, whether it's the picker here or a resolved name there.
  const usersQuery = useRoster("user", true);
  const teamsQuery = useRoster("team", true);
  // The picker offers users and teams as one list, so it is incomplete when
  // EITHER walk stopped short of its end. Both hooks run unconditionally —
  // short-circuiting the second behind the first would make the hook count
  // depend on how deep the user roster went.
  const usersPartial = useRosterPartial("user", true);
  const teamsPartial = useRosterPartial("team", true);
  const rosterPartial = usersPartial || teamsPartial;

  // At most one grant per subject on one record — the tuple the create is
  // idempotent on is exactly `(record, subject)` — so the grant itself is the
  // value, not just the fact that one exists: the picker row states the level,
  // and a re-assert is measured against it.
  const heldBySubject = useMemo(() => {
    const bySubject = new Map<string, RecordGrant>();
    for (const grantRow of grantsQuery.data ?? []) {
      bySubject.set(
        subjectKey(grantRow.subject_type, grantRow.subject_id),
        grantRow,
      );
    }
    return bySubject;
  }, [grantsQuery.data]);

  const roster: RosterSubject[] = useMemo(() => {
    // Agent seats carry is_agent (spec §2.1) precisely so the share picker
    // excludes them — a record is shared with people/teams, never an agent.
    const users = ((usersQuery.data ?? []) as User[])
      .filter((u) => !u.is_agent)
      .map((u) => ({
        id: u.id,
        name: u.display_name,
        note: u.email,
        kind: "user" as const,
      }));
    const teams = ((teamsQuery.data ?? []) as Team[]).map((team) => {
      const count = team.member_count ?? 0;
      return {
        id: team.id,
        name: team.name,
        note: t(
          count === 1 ? "share.teamMembers.one" : "share.teamMembers.other",
          {
            count,
          },
        ),
        kind: "team" as const,
      };
    });
    return [...users, ...teams];
  }, [usersQuery.data, teamsQuery.data, t]);

  const [term, setTerm] = useState("");
  const [subject, setSubject] = useState<RosterSubject | null>(null);
  const [access, setAccess] = useState<Access>("read");
  const [expiryDays, setExpiryDays] = useState(7);
  const [reason, setReason] = useState("");

  const filteredRoster = useMemo(() => {
    const q = term.trim().toLowerCase();
    if (!q) {
      return roster;
    }
    return roster.filter((r) =>
      `${r.name} ${r.note}`.toLowerCase().includes(q),
    );
  }, [roster, term]);

  function resetForm() {
    setTerm("");
    setSubject(null);
    setAccess("read");
    setExpiryDays(7);
    setReason("");
  }

  // A restatement that moved nothing, kept so the screen can say so. The
  // subject's name and level are copied out of the draft rather than read back
  // off the form, which the success has already cleared.
  const [unchanged, setUnchanged] = useState<{
    name: string;
    access: Access;
  } | null>(null);
  // The draft a downgrade is waiting on: the dialog is open exactly while one
  // exists, and confirming submits THIS draft rather than re-reading a form
  // the reader has been looking at a dialog instead of.
  const [downgrade, setDowngrade] = useState<ReassertDraft | null>(null);

  // The whole submit arrives as the mutation's variable, not through this
  // closure: react-query re-arms a mutation's options in a passive effect, so a
  // submit landing between the commit that enables the button and that effect
  // runs the previous render's function — where nothing had been picked yet.
  const grant = useMutation({
    mutationFn: async (draft: GrantDraft) => {
      const body: CreateRecordGrantRequest = {
        record_type: recordType,
        record_id: recordId,
        subject_type: draft.subject.kind,
        subject_id: draft.subject.id,
        access: draft.access,
        reason: draft.reason,
        expires_at: draft.expiresAt,
      };
      const { data, error } = await api.POST("/record-grants", { body });
      if (error) {
        if (error.code === "approval_required") {
          throw new ApprovalRequiredError();
        }
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (_data, draft) => {
      queryClient.invalidateQueries({ queryKey: grantsKey });
      // A re-assert of the same level, expiry and reason leaves the list below
      // looking exactly as it did. Silence there reads as "my change landed",
      // which is the one thing that did not happen, so this press says so.
      setUnchanged(
        draft.change === "unchanged"
          ? { name: draft.subject.name, access: draft.access }
          : null,
      );
      setDowngrade(null);
      resetForm();
    },
  });

  const [revokingId, setRevokingId] = useState<string | null>(null);
  const revoke = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/record-grants/{id}", {
        params: { path: { id } },
      });
      if (error) {
        if (error.code === "approval_required") {
          throw new ApprovalRequiredError();
        }
        throwProblem(error);
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: grantsKey });
      setRevokingId(null);
    },
  });

  // A 403 approval_required and a 403 seat_tier_insufficient each need the
  // surface's own sentence — the second one names the RECIPIENT's licence, not
  // the actor's permission, and "forbidden" sends the reader looking in the
  // wrong place. Every other refusal reads best in the server's words.
  function honestMessage(error: unknown): string {
    if (error instanceof ApprovalRequiredError) {
      return t("share.approvalRequired");
    }
    if (problemCodeOf(error) === "seat_tier_insufficient") {
      return t("share.seatCeiling");
    }
    return problemMessageOf(error, t);
  }

  const grantErrorMessage = grant.isError ? honestMessage(grant.error) : null;
  const revokeErrorMessage = revoke.isError
    ? honestMessage(revoke.error)
    : null;

  // A stale grant error, or a "nothing changed" from the last press, must not
  // outlive the edit that could change the answer — clearing both as the user
  // changes any field mirrors the revoke path's reset(). The react-query reset
  // is guarded so a keystroke in an already-clean form doesn't churn its state.
  function dismissGrantFeedback() {
    if (grant.isError) {
      grant.reset();
    }
    setUnchanged(null);
  }

  // Everything this press means, decided here in the committed render and
  // handed to the mutation whole.
  function draftFor(picked: RosterSubject): GrantDraft {
    const fields: DraftFields = {
      subject: picked,
      access,
      expiresAt: expiresAtFor(expiryDays),
      reason: reason.trim() || undefined,
    };
    const heldGrant = heldBySubject.get(subjectKey(picked.kind, picked.id));
    if (!heldGrant) {
      return { ...fields, change: "new" };
    }
    return {
      ...fields,
      change: reassertKind(heldGrant, fields),
      heldAccess: heldGrant.access,
    };
  }

  // Taking a colleague's level DOWN is the one direction the actor may not
  // have meant, so it is asked about first. Raising it, restating it, and
  // granting a subject their first access all go straight through: none of
  // them removes an authority somebody is already relying on.
  function submit(picked: RosterSubject) {
    const draft = draftFor(picked);
    if (draft.change === "lower") {
      setDowngrade(draft);
      return;
    }
    grant.mutate(draft);
  }

  return (
    <div className="wrap share-screen">
      <Card as="div" className="share-head" title={t("share.title")}>
        <div className="share-backlink">
          <Link2 aria-hidden />
          <EntityRef kind={recordType} id={recordId} />
        </div>
        <p className="share-ceiling">
          <ShieldCheck aria-hidden />
          <span>
            {t("share.ceiling.pre")}
            <b>{t("share.ceiling.recordEmphasis")}</b>
            {t("share.ceiling.mid")}
            <b>{t("share.ceiling.noWider")}</b>
            {t("share.ceiling.post")}
          </span>
        </p>
      </Card>

      {/* The mockup's at-a-glance scope chip and the client-side "can't grant
          wider than you" (write-disabled-when-you-only-have-read) block both
          need the CURRENT USER's own access level FOR THIS RECORD, which no
          endpoint cheaply returns today. Rather than fake it, the ceiling is
          server-enforced: a POST that exceeds the granter's access comes back
          422 / approval_required and is surfaced honestly below. The
          client-side ceiling UI is deferred until a "my access for this
          record" read exists — same call the agent-proposed-grant card
          (held-for-approval) made. */}
      <Card as="div" title={t("share.grantAccess")}>
        <div className="form-stack">
          <div className="field">
            <label className="t-label" htmlFor={`${headingId}-subject`}>
              {t("share.subject")}
            </label>
            <SearchField
              id={`${headingId}-subject`}
              placeholder={t("share.subject")}
              value={term}
              onChange={(event) => {
                setTerm(event.target.value);
                setSubject(null);
                dismissGrantFeedback();
              }}
            />
            <RosterPicker
              usersQuery={usersQuery}
              teamsQuery={teamsQuery}
              partial={rosterPartial}
              filteredRoster={filteredRoster}
              held={heldBySubject}
              subject={subject}
              t={t}
              onPick={(candidate) => {
                setSubject(candidate);
                setTerm(candidate.name);
                const heldGrant = heldBySubject.get(
                  subjectKey(candidate.kind, candidate.id),
                );
                if (heldGrant) {
                  // The form opens on what this subject holds TODAY, because a
                  // re-assert restates every field: left on the compose
                  // defaults, pressing Update would quietly downgrade a write
                  // holder to read and clear the reason on record. An expiry
                  // that is already set has no day-count to come back to — the
                  // four options are relative to now — so that one control
                  // stays where it is and the consequence line below says what
                  // the chosen option will do.
                  setAccess(heldGrant.access);
                  setReason(heldGrant.reason ?? "");
                  if (!heldGrant.expires_at) {
                    setExpiryDays(0);
                  }
                }
                dismissGrantFeedback();
              }}
            />
          </div>

          <div className="field">
            {/* A span, not a label: a segmented control is a group of buttons,
                and there is no single labelable element for a `for` to point
                at — aimed at the wrapper it resolved to nothing, so the words
                focused nothing and the name was never exposed. The group
                carries its own accessible name instead. */}
            <span className="t-label">{t("share.access")}</span>
            <div>
              <SegmentedControl
                options={["read", "write"] as const}
                value={access}
                label={t("share.access")}
                onChange={(next) => {
                  setAccess(next);
                  dismissGrantFeedback();
                }}
                labels={{
                  read: t("share.access.read"),
                  write: t("share.access.write"),
                }}
              />
            </div>
            <p className="t-caption">
              {access === "read"
                ? t("share.access.readNote")
                : t("share.access.writeNote")}
            </p>
          </div>

          <Field label={t("share.expiry")}>
            {(control) => (
              <Select
                {...control}
                // A day count is the state; the control speaks strings, so the
                // conversion happens here at the boundary and nowhere else.
                value={String(expiryDays)}
                onChange={(value) => {
                  setExpiryDays(Number(value));
                  dismissGrantFeedback();
                }}
                options={EXPIRY_OPTIONS.map((option) => ({
                  value: String(option.days),
                  label: t(option.key),
                }))}
              />
            )}
          </Field>

          {/* A share widens who can see a record, so the surface states the
              consequence in plain words rather than leaving the reader to
              infer it from a duration (AC-share-4). Access ending on its own
              is the default, and the one case that does NOT end says so. */}
          <p className="t-caption" data-testid="share-expiry-consequence">
            {expiryDays > 0
              ? t(
                  expiryDays === 1
                    ? "share.expiryConsequence.one"
                    : "share.expiryConsequence.other",
                  { days: expiryDays },
                )
              : t("share.expiryConsequenceNone")}
          </p>

          <Field label={t("share.reason")}>
            {(control) => (
              <Textarea
                {...control}
                className="share-reason"
                value={reason}
                onChange={(event) => {
                  setReason(event.target.value);
                  dismissGrantFeedback();
                }}
              />
            )}
          </Field>

          {unchanged && (
            <p
              className="t-caption"
              role="status"
              data-testid="share-unchanged"
            >
              {t("share.unchanged", {
                name: unchanged.name,
                access: accessLabel(unchanged.access, t),
              })}
            </p>
          )}

          {grantErrorMessage && (
            <p className="t-caption share-error">{grantErrorMessage}</p>
          )}

          <Button
            variant="primary"
            disabled={!subject}
            pending={grant.isPending}
            onClick={() => subject && submit(subject)}
            data-testid="share-grant-submit"
          >
            {/* A subject who already holds a grant is not being granted one:
                the press restates what they hold, and the word on the button
                is the reader's last cue to which of the two they are doing. */}
            {subject && heldBySubject.has(subjectKey(subject.kind, subject.id))
              ? t("share.update")
              : t("share.grant")}
          </Button>
        </div>
      </Card>

      <Card as="div" title={t("share.whoHasAccess")}>
        <QueryGate query={grantsQuery} empty={(rows) => rows.length === 0}>
          {(rows) => (
            <ul className="share-acl-list" data-testid="share-acl-list">
              {rows.map((g) => (
                <li key={g.id} className="share-acl-row">
                  <div className="share-acl-who">
                    <span className="share-acl-name">
                      <SubjectKindIcon kind={g.subject_type} t={t} />
                      <EntityRef kind={g.subject_type} id={g.subject_id} />
                    </span>
                    <div className="share-acl-meta">
                      {/* The same badge, in the same tone, as the picker row
                          for this subject: one drawing of a level per screen,
                          or the reader has two things to learn instead of one. */}
                      <Badge tone={g.access === "write" ? "accent" : undefined}>
                        {accessLabel(g.access, t)}
                      </Badge>
                      <span className="t-caption">
                        {t("share.grantedBy")}{" "}
                        <EntityRef kind="user" id={g.granted_by} />
                      </span>
                      {g.reason && (
                        <span className="t-caption">{g.reason}</span>
                      )}
                      {g.expires_at && (
                        <span className="share-expiry-badge">
                          {formatDate(g.expires_at, locale, zone)}
                        </span>
                      )}
                    </div>
                  </div>
                  <Button
                    small
                    variant="danger"
                    onClick={() => setRevokingId(g.id)}
                    data-testid="revoke-grant"
                  >
                    {t("share.revoke")}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </QueryGate>
      </Card>

      <ConfirmModal
        open={revokingId !== null}
        onClose={() => {
          setRevokingId(null);
          revoke.reset();
        }}
        title={t("share.revoke")}
        confirmLabel={t("share.revoke")}
        confirmVariant="danger"
        returnFocusTo={returnFocusToSubject}
        onConfirm={() => {
          if (revokingId) {
            revoke.mutate(revokingId);
          }
        }}
        pending={revoke.isPending}
        error={revokeErrorMessage}
      >
        <p>{t("share.revokeConfirm")}</p>
      </ConfirmModal>

      {/* Mounted only while a downgrade is waiting, because its copy names the
          person and the two levels — a dialog kept mounted with nothing to ask
          about would have to word that question about nobody. */}
      {downgrade && (
        <ConfirmModal
          open
          onClose={() => {
            setDowngrade(null);
            grant.reset();
          }}
          title={t("share.downgradeTitle")}
          confirmLabel={t("share.downgradeConfirm", {
            to: accessLabel(downgrade.access, t),
          })}
          confirmVariant="danger"
          returnFocusTo={returnFocusToSubject}
          onConfirm={() => grant.mutate(downgrade)}
          pending={grant.isPending}
          error={grantErrorMessage}
        >
          <p data-testid="share-downgrade-body">
            {t("share.downgradeBody", {
              name: downgrade.subject.name,
              from: accessLabel(downgrade.heldAccess, t),
              to: accessLabel(downgrade.access, t),
            })}
          </p>
        </ConfirmModal>
      )}
    </div>
  );
}
