// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link2, ShieldCheck } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Button,
  EmptyState,
  SearchField,
  SectionHeader,
  SegmentedControl,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { formatDate } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage, QueryGate, throwProblem } from "./common";
import { EntityRef, useRoster } from "./entityref";
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
type RecordType = CreateRecordGrantRequest["record_type"];
type User = components["schemas"]["User"];
type Team = components["schemas"]["Team"];

type RosterSubject = {
  id: string;
  name: string;
  note: string;
  kind: "user" | "team";
};

const RECORD_TYPES: readonly RecordType[] = [
  "person",
  "organization",
  "deal",
  "lead",
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
}: Readonly<{ recordType: RecordType; recordId: string }>) {
  const t = useT();
  return (
    <Button
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
    throw new Error(problemMessage(error));
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

function ShareScreenBody({
  recordType,
  recordId,
}: Readonly<{ recordType: RecordType; recordId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const headingId = useId();
  const grantsKey = ["record-grants", recordType, recordId];

  const grantsQuery = useQuery({
    queryKey: grantsKey,
    queryFn: () => fetchGrants(recordType, recordId),
  });

  // Shares EntityRef's exact cache entries ([ "users" ] / [ "teams" ]) —
  // one roster fetch, whether it's the picker here or a resolved name there.
  const usersQuery = useRoster("user", true);
  const teamsQuery = useRoster("team", true);

  const grantedSubjectIds = useMemo(
    () => new Set((grantsQuery.data ?? []).map((g) => g.subject_id)),
    [grantsQuery.data],
  );

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
    const teams = ((teamsQuery.data ?? []) as Team[]).map((team) => ({
      id: team.id,
      name: team.name,
      note: t("share.teamMembers", { count: team.member_count ?? 0 }),
      kind: "team" as const,
    }));
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

  const grant = useMutation({
    mutationFn: async () => {
      if (!subject) {
        // The submit button is disabled until a subject is picked — this
        // guard only protects a stale closure, never a real path.
        throw new Error("no subject selected");
      }
      const body: CreateRecordGrantRequest = {
        record_type: recordType,
        record_id: recordId,
        subject_type: subject.kind,
        subject_id: subject.id,
        access,
        reason: reason.trim() || undefined,
        expires_at: expiresAtFor(expiryDays),
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
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: grantsKey });
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

  // A 403 approval_required or a 422 validation problem both need to render
  // as honest, human copy — never the bare code or a stringified object.
  function honestMessage(error: unknown): string | null {
    if (error instanceof ApprovalRequiredError) {
      return t("share.approvalRequired");
    }
    return error instanceof Error ? error.message : null;
  }

  const grantErrorMessage = grant.isError ? honestMessage(grant.error) : null;
  const revokeErrorMessage = revoke.isError
    ? honestMessage(revoke.error)
    : null;

  // A stale grant error must not outlive the edit that could fix it — clearing
  // it as the user changes any field mirrors the revoke path's reset(). Guarded
  // so a keystroke in an already-clean form doesn't churn react-query state.
  function dismissGrantError() {
    if (grant.isError) {
      grant.reset();
    }
  }

  return (
    <div className="wrap share-screen">
      <div className="card share-head">
        <SectionHeader title={t("share.title")} />
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
      </div>

      {/* The mockup's at-a-glance scope chip and the client-side "can't grant
          wider than you" (write-disabled-when-you-only-have-read) block both
          need the CURRENT USER's own access level FOR THIS RECORD, which no
          endpoint cheaply returns today. Rather than fake it, the ceiling is
          server-enforced: a POST that exceeds the granter's access comes back
          422 / approval_required and is surfaced honestly below. The
          client-side ceiling UI is deferred until a "my access for this
          record" read exists — same call the 🟡 agent-proposed-grant card
          made. */}
      <div className="card">
        <SectionHeader title={t("share.grantAccess")} />
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
              dismissGrantError();
            }}
          />
          <ul className="share-subject-list">
            {filteredRoster.map((candidate) => {
              const already = grantedSubjectIds.has(candidate.id);
              return (
                <li key={candidate.id}>
                  <button
                    type="button"
                    className="btn btn-ghost share-subject-row"
                    disabled={already}
                    aria-pressed={subject?.id === candidate.id}
                    onClick={() => {
                      setSubject(candidate);
                      setTerm(candidate.name);
                      dismissGrantError();
                    }}
                  >
                    <span>{candidate.name}</span>
                    <span className="share-subject-note">
                      {already ? t("share.alreadyGranted") : candidate.note}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </div>

        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-access`}>
            {t("share.access")}
          </label>
          <div id={`${headingId}-access`}>
            <SegmentedControl
              options={["read", "write"] as const}
              value={access}
              onChange={(next) => {
                setAccess(next);
                dismissGrantError();
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

        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-expiry`}>
            {t("share.expiry")}
          </label>
          <select
            id={`${headingId}-expiry`}
            className="input"
            value={expiryDays}
            onChange={(event) => {
              setExpiryDays(Number(event.target.value));
              dismissGrantError();
            }}
          >
            {EXPIRY_OPTIONS.map((option) => (
              <option key={option.days} value={option.days}>
                {t(option.key)}
              </option>
            ))}
          </select>
        </div>

        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-reason`}>
            {t("share.reason")}
          </label>
          <textarea
            id={`${headingId}-reason`}
            className="input share-reason"
            value={reason}
            onChange={(event) => {
              setReason(event.target.value);
              dismissGrantError();
            }}
          />
        </div>

        {grantErrorMessage && (
          <p className="t-caption share-error">{grantErrorMessage}</p>
        )}

        <Button
          variant="primary"
          disabled={!subject || grant.isPending}
          onClick={() => grant.mutate()}
          data-testid="share-grant-submit"
        >
          {t("share.grant")}
        </Button>
      </div>

      <div className="card">
        <SectionHeader title={t("share.whoHasAccess")} />
        <QueryGate query={grantsQuery} empty={(rows) => rows.length === 0}>
          {(rows) => (
            <ul className="share-acl-list" data-testid="share-acl-list">
              {rows.map((g) => (
                <li key={g.id} className="share-acl-row">
                  <div className="share-acl-who">
                    <EntityRef kind={g.subject_type} id={g.subject_id} />
                    <div className="share-acl-meta">
                      <span
                        className={`share-access-pill share-access-${g.access}`}
                      >
                        {t(
                          g.access === "read"
                            ? "share.access.read"
                            : "share.access.write",
                        )}
                      </span>
                      <span className="t-caption">
                        {t("share.grantedBy")}{" "}
                        <EntityRef kind="user" id={g.granted_by} />
                      </span>
                      {g.reason && (
                        <span className="t-caption">{g.reason}</span>
                      )}
                      {g.expires_at && (
                        <span className="share-expiry-badge">
                          {formatDate(g.expires_at, locale, "Europe/Berlin")}
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
      </div>

      <ConfirmModal
        open={revokingId !== null}
        onClose={() => {
          setRevokingId(null);
          revoke.reset();
        }}
        title={t("share.revoke")}
        confirmLabel={t("share.revoke")}
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
    </div>
  );
}
