import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useCallback, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, TextInput } from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import { useT } from "../i18n";
import {
  isConsentNotGranted,
  ProblemError,
  problemMessage,
  throwProblem,
} from "./common";
import { useConsentPurposes } from "./consent";
import "./compose.css";

// The composer surface for the three already-routed ops (draftEmail /
// sendEmail / relinkActivity): a human's edit-then-confirm reply, and a
// mis-captured activity's relink. Pure frontend — every op is live, audited,
// and typed on the backend; this file only calls them.

type Activity = components["schemas"]["Activity"];

// The four link targets a relink can point at (relinkActivity's entity_type
// enum). Reused by ComposeModal and TimelineActions so the whole surface
// speaks one vocabulary.
export type RelinkKind = "person" | "organization" | "deal" | "lead";

// The relink target is chosen via cross-object search (/search covers all four
// kinds; the per-entity list endpoints don't all expose `q`). Each candidate's
// entity_type comes from its SearchResult.type, remembered here so the confirm
// can recover it — RecordPickerCandidate itself only carries {id,name}.
// Activity results are dropped: relink's target enum has no `activity`.
function useSearchTargets() {
  const kindById = useRef(new Map<string, RelinkKind>());
  const search = useCallback(
    async (q: string): Promise<RecordPickerCandidate[]> => {
      const { data, error } = await api.GET("/search", {
        params: { query: { q, limit: 10 } },
      });
      if (error) throw new Error(problemMessage(error));
      const out: RecordPickerCandidate[] = [];
      for (const result of data.data) {
        if (result.type === "activity") continue;
        kindById.current.set(result.id, result.type);
        out.push({ id: result.id, name: result.title ?? result.id });
      }
      return out;
    },
    [],
  );
  return { search, kindOf: (id: string) => kindById.current.get(id) ?? null };
}

// A 🟢 internal association (no autonomy dot): move or also-link a captured
// activity's typed link to the right person/org/deal/lead. Idempotent on the
// backend — re-relinking the same target is a no-op that still answers 200.
export function RelinkModal({
  activityId,
  entityType,
  entityId,
  open,
  onClose,
}: Readonly<{
  activityId: string;
  entityType: RelinkKind;
  entityId: string;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const { search, kindOf } = useSearchTargets();
  const [target, setTarget] = useState<RecordPickerCandidate | null>(null);
  const [replace, setReplace] = useState(false);

  const mutation = useMutation({
    mutationFn: async () => {
      const kind = target ? kindOf(target.id) : null;
      if (!target || !kind) {
        // The confirm is disabled without a target, so this only fires if the
        // remembered kind was lost — surface it, never send an empty relink.
        throw new Error(t("compose.relinkTarget"));
      }
      const { data, error } = await api.POST("/activities/{id}/relink", {
        params: {
          path: { id: activityId },
          header: { "Idempotency-Key": crypto.randomUUID() },
        },
        body: {
          entity_type: kind,
          entity_id: target.id,
          replace_existing_of_type: replace,
        },
      });
      if (error) throw new Error(problemMessage(error));
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["activities", entityType, entityId],
      });
      onClose();
    },
  });

  return (
    <ConfirmModal
      open={open}
      onClose={onClose}
      title={t("compose.relinkTitle")}
      confirmLabel={t("compose.relinkConfirm")}
      confirmDisabled={!target}
      onConfirm={() => mutation.mutate()}
      pending={mutation.isPending}
      error={mutation.isError ? mutation.error.message : null}
    >
      <div className="compose-fields">
        <RecordPicker
          label={t("compose.relinkTarget")}
          searchTargets={search}
          onPick={setTarget}
          selected={target}
        />
        <label className="t-body compose-check">
          <input
            type="checkbox"
            checked={replace}
            onChange={(event) => setReplace(event.target.checked)}
          />{" "}
          {t("compose.relinkReplace")}
        </label>
        <p className="t-caption">{t("compose.relinkReplaceHint")}</p>
      </div>
    </ConfirmModal>
  );
}

// A freeform email-chip input: typed address + Enter/comma (or blur) adds a
// chip, ✕ removes it. No client-side email regex beyond type=email — the
// server is the authority (422 on a malformed address), so this never rejects
// what the backend might accept.
function RecipientField({
  label,
  values,
  onChange,
}: Readonly<{
  label: string;
  values: string[];
  onChange: (next: string[]) => void;
}>) {
  const t = useT();
  const [draft, setDraft] = useState("");
  const add = () => {
    const value = draft.trim();
    if (value && !values.includes(value)) onChange([...values, value]);
    setDraft("");
  };
  return (
    <div className="recipient-field">
      <span className="t-caption">{label}</span>
      <ul className="chips">
        {values.map((value) => (
          <li key={value}>
            {value}{" "}
            <button
              type="button"
              aria-label={t("compose.removeRecipient", { recipient: value })}
              onClick={() =>
                onChange(values.filter((other) => other !== value))
              }
            >
              ×
            </button>
          </li>
        ))}
      </ul>
      <TextInput
        type="email"
        aria-label={label}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter" || event.key === ",") {
            event.preventDefault();
            add();
          }
        }}
        onBlur={add}
      />
    </div>
  );
}

// The 🟡 confirm-first composer (draftEmail + sendEmail). Draft with AI fills
// the fields; the human edits and confirms; the human's own click IS the
// approval (ADR-0055), so the human REST path sends no X-Approval-Token and no
// Idempotency-Key — that plumbing is the agent/passport path. The 409
// consent gate is the whole reason this surface exists: the default-deny
// suppression (A22/ADR-0011) has never been visible to a user before.
export function ComposeModal({
  activityId,
  entityType,
  entityId,
  personId,
  open,
  onClose,
}: Readonly<{
  activityId: string;
  entityType: RelinkKind;
  entityId: string;
  personId?: string;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const purposes = useConsentPurposes();
  const [to, setTo] = useState<string[]>([]);
  const [cc, setCc] = useState<string[]>([]);
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [intent, setIntent] = useState("");
  const [purpose, setPurpose] = useState("");
  // Two honest non-error outcomes, kept OUT of react-query's error channel so
  // the form stays usable: the model / mailer simply isn't configured (501).
  const [draftUnavailable, setDraftUnavailable] = useState(false);
  const [sendUnavailable, setSendUnavailable] = useState(false);

  const draft = useMutation({
    mutationFn: async () => {
      setDraftUnavailable(false);
      const { data, error, response } = await api.POST(
        "/activities/{id}/draft-email",
        {
          params: { path: { id: activityId } },
          body: intent.trim() ? { intent: intent.trim() } : {},
        },
      );
      if (response.status === 501) return { available: false as const };
      // Success is the real 2xx, never merely the absence of an error body:
      // openapi-fetch reports a falsy `error` for a bodiless non-2xx (a gateway
      // 502/503/504), which would otherwise fall through as a fabricated draft
      // and crash the fill on undefined fields.
      if (!response.ok) {
        throw new Error(
          problemMessage(error || { title: t("compose.actionFailed") }),
        );
      }
      return { available: true as const, draft: data };
    },
    onSuccess: (result) => {
      if (!result.available) {
        setDraftUnavailable(true);
        return;
      }
      const drafted = result.draft;
      // Never clobber a field the user already edited.
      if (!subject) setSubject(drafted.subject);
      if (!body) setBody(drafted.body);
      if (to.length === 0 && drafted.to?.length) setTo(drafted.to);
    },
  });

  const send = useMutation({
    mutationFn: async () => {
      setSendUnavailable(false);
      const { data, error, response } = await api.POST(
        "/activities/{id}/send-email",
        {
          params: { path: { id: activityId } },
          // No X-Approval-Token, no Idempotency-Key: the human's own click IS
          // the approval on the REST path (ADR-0055).
          body: {
            subject,
            body,
            to,
            cc: cc.length ? cc : undefined,
            consent_purpose: purpose,
          },
        },
      );
      if (response.status === 501) return { sent: false as const };
      // Only a real 202 is a send. openapi-fetch returns a falsy `error` for a
      // bodiless non-2xx (a gateway 502/503/504); inferring success from
      // `!error` would close the modal reporting an irreversible send that
      // never left the building. Gate on the status, not the error body.
      if (!response.ok) {
        throwProblem(error || { title: t("compose.actionFailed") });
      }
      return { sent: true as const, activity: data };
    },
    onSuccess: (result) => {
      if (!result.sent) {
        setSendUnavailable(true);
        return;
      }
      queryClient.invalidateQueries({
        queryKey: ["activities", entityType, entityId],
      });
      onClose();
    },
  });

  // The consent gate is a distinct product state, not a generic failure: keep
  // it out of the modal's inline error so the form stays open with pointed
  // default-deny copy instead of a raw server detail.
  const blockedByConsent =
    send.error instanceof ProblemError &&
    isConsentNotGranted(send.error.problem);
  const sendError =
    send.isError && !blockedByConsent ? send.error.message : null;
  const canSend =
    to.length > 0 &&
    subject.trim() !== "" &&
    body.trim() !== "" &&
    purpose !== "";

  return (
    <ConfirmModal
      open={open}
      onClose={onClose}
      title={t("compose.sendConfirmTitle")}
      tier="confirm"
      confirmLabel={t("compose.send")}
      confirmDisabled={!canSend}
      onConfirm={() => send.mutate()}
      pending={send.isPending}
      error={sendError}
    >
      <div className="compose-fields">
        <div className="compose-draftbar">
          <TextInput
            placeholder={t("compose.intent")}
            value={intent}
            onChange={(event) => setIntent(event.target.value)}
          />
          <Button
            small
            onClick={() => draft.mutate()}
            disabled={draft.isPending}
          >
            {draft.isPending ? t("compose.drafting") : t("compose.draftWithAi")}
          </Button>
        </div>
        {draftUnavailable && (
          <p className="t-caption">{t("compose.draftUnavailable")}</p>
        )}
        {draft.isError && !draftUnavailable && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {draft.error?.message}
          </p>
        )}

        <RecipientField label={t("compose.to")} values={to} onChange={setTo} />
        <RecipientField label={t("compose.cc")} values={cc} onChange={setCc} />
        <TextInput
          placeholder={t("compose.subject")}
          value={subject}
          onChange={(event) => setSubject(event.target.value)}
        />
        <textarea
          className="compose-body"
          placeholder={t("compose.body")}
          value={body}
          onChange={(event) => setBody(event.target.value)}
        />

        <label className="t-body compose-check">
          {t("compose.purpose")}
          <select
            value={purpose}
            onChange={(event) => setPurpose(event.target.value)}
          >
            <option value="">—</option>
            {purposes.data?.data.map((option) => (
              <option key={option.id} value={option.key}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
        <p className="t-caption">{t("compose.purposeHint")}</p>

        {to.length === 0 && (
          <p className="t-caption">{t("compose.emptyRecipients")}</p>
        )}
        {sendUnavailable && (
          <p className="t-caption">{t("compose.sendUnavailable")}</p>
        )}
        {blockedByConsent && (
          <div className="compose-consent-block" role="alert">
            <p className="t-body" style={{ fontWeight: 600 }}>
              {t("compose.consentBlockedTitle")}
            </p>
            <p className="t-body" style={{ color: "var(--danger)" }}>
              {t("compose.consentBlocked")}
            </p>
            {personId && (
              <a href={`#/contacts/${personId}`} className="link-button">
                {t("compose.consentGoto")}
              </a>
            )}
          </div>
        )}
        <p className="t-caption compose-caution">{t("compose.sendBody")}</p>
      </div>
    </ConfirmModal>
  );
}

// The per-row action cluster the 360 timelines mount in each entry's action
// slot. Reply opens the composer (email rows only — you don't reply to a
// note); Relink opens the relink dialog for any row that already carries a
// typed link (a freshly hand-logged note with no links has nothing to move).
// It owns the two open states so the timeline mapper stays presentational.
export function TimelineActions({
  activity,
  entityType,
  entityId,
  personId,
}: Readonly<{
  activity: Activity;
  entityType: RelinkKind;
  entityId: string;
  personId?: string;
}>) {
  const t = useT();
  const [reply, setReply] = useState(false);
  const [relink, setRelink] = useState(false);
  const canReply = activity.kind === "email";
  const canRelink = (activity.links?.length ?? 0) >= 1;
  if (!canReply && !canRelink) return null;
  return (
    <>
      {canReply && (
        <Button small onClick={() => setReply(true)}>
          {t("compose.reply")}
        </Button>
      )}
      {canRelink && (
        <Button small onClick={() => setRelink(true)}>
          {t("compose.relink")}
        </Button>
      )}
      {reply && (
        <ComposeModal
          activityId={activity.id}
          entityType={entityType}
          entityId={entityId}
          personId={personId}
          open={reply}
          onClose={() => setReply(false)}
        />
      )}
      {relink && (
        <RelinkModal
          activityId={activity.id}
          entityType={entityType}
          entityId={entityId}
          open={relink}
          onClose={() => setRelink(false)}
        />
      )}
    </>
  );
}
