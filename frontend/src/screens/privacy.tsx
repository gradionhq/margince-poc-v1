import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { type ReactNode, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
  SegmentedControl,
  Skeleton,
  TextInput,
} from "../design-system/atoms";
import { formatDate } from "../format/format";
import { useNow } from "../format/now";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { humanizeToken } from "./audit";
import {
  LoadMoreButton,
  ProblemError,
  problemMessage,
  QueryGate,
  throwProblem,
} from "./common";
import { EntityRef, useRoster } from "./entityref";
import {
  DSR_STATUS_FACETS,
  type DsrStatus,
  type DsrStatusFacet,
  dsrKindTone,
  isOverdue,
  isTerminal,
  nextStatuses,
} from "./privacy.logic";
import "./privacy.css";

type DataSubjectRequest = components["schemas"]["DataSubjectRequest"];
type UpdateDataSubjectRequest =
  components["schemas"]["UpdateDataSubjectRequest"];
type User = components["schemas"]["User"];

// The two settings/privacy surfaces, extracted out of the 1309-line
// settings.tsx (the audit.tsx extraction precedent): the consent-purpose
// catalogue (G-3 adds create — POST /consent-purposes already routed, but
// nothing in this app called it) and the DSR inbox. GET + POST only — there
// is no PATCH or DELETE on /consent-purposes, so a purpose is append-only by
// contract, not by convention; the create form says so up front.

// share.tsx:417's honestMessage idiom, shared by every mutation error render
// in this file: surface the server's own explanation rather than a canned
// one. A ProblemError's message is already problemMessage(problem), so this
// covers both the plain-Error and ProblemError mutation failures below.
function honestMessage(error: unknown): string | null {
  return error instanceof Error ? error.message : null;
}

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

// G-3: the inline purpose-create form, toggled by "Add purpose" — the
// share.tsx precedent (create is an inline card, never a modal; only the
// destructive revoke there uses one). A stale create error must not outlive
// the edit that could fix it, so every field's onChange clears it first
// (share.tsx:432's dismissGrantError idiom).
function PurposeCreateForm({ onDone }: Readonly<{ onDone: () => void }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [key, setKey] = useState("");
  const [label, setLabel] = useState("");
  const [requiresDoi, setRequiresDoi] = useState(false);
  const keyId = useId();
  const labelId = useId();

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
        throw new Error(problemMessage(error));
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
    <div className="card card-inset purpose-form">
      <p className="t-caption purpose-form-warning">
        {t("privacy.purposeAppendOnly")}
      </p>
      <div className="form-stack">
        <div className="field">
          <label className="t-label" htmlFor={keyId}>
            {t("privacy.purposeKey")}
          </label>
          <TextInput
            id={keyId}
            value={key}
            onChange={(event) => {
              setKey(event.target.value);
              dismissCreateError();
            }}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={labelId}>
            {t("privacy.purposeLabel")}
          </label>
          <TextInput
            id={labelId}
            value={label}
            onChange={(event) => {
              setLabel(event.target.value);
              dismissCreateError();
            }}
          />
        </div>
        <label className="t-caption purpose-doi-check">
          <input
            type="checkbox"
            checked={requiresDoi}
            onChange={(event) => {
              setRequiresDoi(event.target.checked);
              dismissCreateError();
            }}
          />
          {t("privacy.purposeDoi")}
        </label>
        {create.isError && (
          <p className="t-caption purpose-form-error">
            {honestMessage(create.error)}
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
    </div>
  );
}

export function ConsentPurposesCard() {
  const t = useT();
  const [adding, setAdding] = useState(false);
  const query = useQuery({
    queryKey: ["consent-purposes"],
    queryFn: async () => {
      const { data, error } = await api.GET("/consent-purposes");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <div className="list-head">
        <SectionHeader title={t("settings.purposes")} />
        <Button small onClick={() => setAdding((value) => !value)}>
          {t("privacy.addPurpose")}
        </Button>
      </div>
      {adding && <PurposeCreateForm onDone={() => setAdding(false)} />}
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <div
            style={{
              display: "flex",
              gap: 8,
              flexWrap: "wrap",
              marginTop: adding ? 10 : 0,
            }}
          >
            {page.data.map((purpose) => (
              <Badge
                key={purpose.id}
                tone={purpose.requires_double_opt_in ? "warn" : undefined}
              >
                {purpose.label}
                {purpose.requires_double_opt_in ? " · DOI" : ""}
              </Badge>
            ))}
          </div>
        )}
      </QueryGate>
    </section>
  );
}

// Matches a proper person-id UUID; an external identifier (email, a partner's
// own reference string) never does, so it stays raw mono text rather than a
// dead EntityRef lookup against a record that was never a person id.
const SUBJECT_UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function statusTone(
  status: DsrStatus,
): "success" | "warn" | "danger" | undefined {
  if (status === "fulfilled") return "success";
  if (status === "rejected") return "danger";
  if (status === "in_progress") return "warn";
  return undefined;
}

// nextStatuses(open|in_progress) only ever yields these three targets (the
// TRANSITIONS DAG in privacy.logic.ts never routes to "open"); the fallback
// return keeps this total without a needless fourth i18n key for a status
// that can never reach here.
function transitionLabelKey(status: DsrStatus): MessageKey {
  if (status === "in_progress") return "privacy.inProgress";
  if (status === "fulfilled") return "privacy.fulfil";
  return "privacy.reject";
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
  onFulfilErasure?: (dsr: DataSubjectRequest) => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [resolution, setResolution] = useState(dsr.resolution ?? "");
  const assigneeFieldId = useId();
  const resolutionFieldId = useId();

  // Only fetched while this row's panel is actually open — the roster is the
  // same shared ["users"] cache entry EntityRef and the share picker read.
  const roster = useRoster("user", expanded);
  // Agent seats can't hold requireDSRAdmin's unbounded row scope (only a
  // human admission can), so the picker never offers one — same is_agent
  // filter as the share subject picker.
  const assignableUsers = ((roster.data ?? []) as User[]).filter(
    (u) => !u.is_agent,
  );

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
    // Deferred to Task 9 (the typed-ERASE confirmation + legal-hold 409
    // handling) — an erasure fulfil never goes through this plain PATCH.
    if (dsr.kind === "erasure" && next === "fulfilled") {
      onFulfilErasure?.(dsr);
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
      : honestMessage(patch.error);

  return (
    <li className="dsr-row">
      <Button className="dsr-row-toggle" onClick={onToggle}>
        <Badge tone={dsrKindTone(dsr.kind)}>{humanizeToken(dsr.kind)}</Badge>
        <span className="t-mono">{dsr.subject_ref}</span>
        <Badge tone={statusTone(dsr.status)}>{humanizeToken(dsr.status)}</Badge>
        <span className="t-small">
          {t("settings.due", { date: formatDate(dsr.due_at, locale, tz) })}
        </span>
        {overdue && <Badge tone="danger">{t("privacy.overdue")}</Badge>}
      </Button>
      {expanded && (
        <div className="card card-inset dsr-expanded">
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
              <select
                id={assigneeFieldId}
                className="input"
                value={dsr.assignee_id ?? ""}
                onChange={(event) => {
                  const value = event.target.value;
                  // No "unassign" option: coalesce($3, assignee_id) treats an
                  // explicit null as a no-op, so there is nothing an empty
                  // selection could legitimately send.
                  if (!value) {
                    return;
                  }
                  patch.mutate({ assignee_id: value });
                }}
              >
                <option value="">—</option>
                {assignableUsers.map((user) => (
                  <option key={user.id} value={user.id}>
                    {user.display_name}
                  </option>
                ))}
              </select>
              <p className="t-caption">{t("privacy.assigneeUnassignable")}</p>
            </div>

            {terminal ? (
              <p className="t-caption">{t("privacy.closed")}</p>
            ) : (
              <>
                <div className="field">
                  <label className="t-label" htmlFor={resolutionFieldId}>
                    {t("privacy.resolution")}
                  </label>
                  <textarea
                    id={resolutionFieldId}
                    className="input"
                    value={resolution}
                    onChange={(event) => {
                      setResolution(event.target.value);
                      dismissPatchError();
                    }}
                  />
                  <p className="t-caption">{t("privacy.resolutionRequired")}</p>
                </div>
                {patchErrorMessage && (
                  <p className="t-caption dsr-error">{patchErrorMessage}</p>
                )}
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
        </div>
      )}
    </li>
  );
}

export function PrivacyInboxCard({
  onFulfilErasure,
}: Readonly<{
  onFulfilErasure?: (dsr: DataSubjectRequest) => void;
}> = {}) {
  const t = useT();
  const { locale } = useLocale();
  // useNow is the only clock touching rendering (format/now.ts) — isOverdue
  // itself stays pure and takes the epoch ms this hook produces.
  const nowMs = useNow(60_000);
  // FIX-1: due_at is a statutory deadline. A hardcoded zone shows the wrong
  // calendar day to anyone outside it — the viewer's own resolved IANA zone
  // is the only honest signal for "what date does THIS reader see"
  // (share.tsx:290's precedent for the same problem on grant expiry).
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [facet, setFacet] = useState<DsrStatusFacet>("all");
  // One case open at a time: expandedId lives here (not per-row) so opening
  // a second row's panel closes the first — the queue itself (sibling rows,
  // the facet bar) stays on screen throughout; an officer working a case
  // never loses sight of what else is waiting.
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // The facet is server-side (part of the queryKey and the query param), not
  // a client re-slice of one big page — a re-slice would hide rows the
  // server never told the pager about, breaking `has_more`/`next_cursor`
  // (the house rule at history.tsx:258).
  const query = useInfiniteQuery({
    queryKey: ["dsrs", facet],
    initialPageParam: null as string | null,
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
        throw new Error(problemMessage(error));
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });

  const rows = query.data?.pages.flatMap((page) => page.data) ?? [];

  const facetLabels = Object.fromEntries(
    DSR_STATUS_FACETS.map((value) => [
      value,
      value === "all" ? t("privacy.facetAll") : humanizeToken(value),
    ]),
  ) as Record<DsrStatusFacet, string>;

  // Honest state matrix (§3a): pending/error stay identical to every other
  // list here; filtering happens server-side so an empty page after a facet
  // change is a real "nothing matches", not a client-side hide.
  let body: ReactNode;
  if (query.isPending) {
    body = (
      <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        <Skeleton width="60%" />
        <Skeleton width="90%" />
      </div>
    );
  } else if (query.isError) {
    body = (
      <EmptyState>
        <p>{t("common.error")}</p>
        <p className="t-mono" style={{ marginTop: 6 }}>
          {query.error instanceof Error ? query.error.message : null}
        </p>
        <Button small onClick={() => query.refetch()} style={{ marginTop: 10 }}>
          {t("common.retry")}
        </Button>
      </EmptyState>
    );
  } else if (rows.length === 0) {
    body = <EmptyState>{t("common.empty")}</EmptyState>;
  } else {
    body = (
      <>
        <ul className="dsr-list">
          {rows.map((dsr) => (
            <DsrRow
              key={dsr.id}
              dsr={dsr}
              expanded={expandedId === dsr.id}
              onToggle={() =>
                setExpandedId((current) => (current === dsr.id ? null : dsr.id))
              }
              nowMs={nowMs}
              tz={tz}
              locale={locale}
              onFulfilErasure={onFulfilErasure}
            />
          ))}
        </ul>
        <LoadMoreButton query={query} />
      </>
    );
  }

  return (
    <section className="card">
      <SectionHeader
        title={t("settings.privacy")}
        sub={t("settings.privacySub")}
      />
      <SegmentedControl
        options={DSR_STATUS_FACETS}
        value={facet}
        onChange={setFacet}
        labels={facetLabels}
      />
      {body}
    </section>
  );
}
