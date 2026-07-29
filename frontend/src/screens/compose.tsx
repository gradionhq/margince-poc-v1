import { useMutation, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import { useT } from "../i18n";
import {
  isConsentNotGranted,
  ProblemError,
  problemFieldErrorsOf,
  problemMessage,
  throwProblem,
} from "./common";
import { useConsentPurposes } from "./consent";
import { useVoiceProfile } from "./voice-profile";
import "./compose.css";

// The composer surface for the three already-routed ops (draftEmail /
// sendEmail / relinkActivity): a human's edit-then-confirm reply, and a
// mis-captured activity's relink. Pure frontend — every op is live, audited,
// and typed on the backend; this file only calls them.

type Activity = components["schemas"]["Activity"];
type EmailDraft = components["schemas"]["EmailDraft"];
type VoiceProfile = components["schemas"]["VoiceProfile"];

// What a drafting call reported about the text it produced. Held apart from the
// fields it filled because the disclosure is owed for the call that put model
// output on this surface, whatever the human then does to the words.
type DraftProvenance = Pick<
  EmailDraft,
  "ai_generated" | "ai_disclosure" | "voice_profile_version"
>;

// The link targets a relink can point at (relinkActivity's entity_type enum,
// minus `activity` — a relink never points at another activity). Reused by
// ComposeModal and TimelineActions so the whole surface speaks one vocabulary.
export type RelinkKind =
  | "person"
  | "organization"
  | "deal"
  | "lead"
  | "project";

// The relink target is chosen via cross-object search (/search covers every
// kind; the per-entity list endpoints don't all expose `q`). Each candidate's
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
// chip, the X icon removes it. No client-side email regex beyond type=email —
// the server is the authority (422 on a malformed address), so this never rejects
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
              <X size={14} aria-hidden />
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

// The Art. 50 disclosure for a model-produced draft, in the card treatment the
// offer surface's banner already uses. The server's disclosure line is a
// compliance string rendered verbatim, never reworded; a response that omits it
// still discloses, because a missing line may not silently become a missing
// disclosure.
//
// The voice tag names the PROFILE version that styled the draft, and the
// provisional label reports what that profile is today. Neither implies a
// weaker draft: nothing gates drafting on maturity, so a provisional profile
// styles this text exactly as a fuller one would.
//
// Both hang off the served version, because maturity is a corpus-word band
// that reaches `provisional` while the profile is still only collecting — and
// a profile with nothing built yet leaves the version null and styles nothing.
// Reporting a voice's maturity over a draft no voice touched would overstate
// this surface's own provenance, which Art. 50 does not permit.
function DraftDisclosure({
  provenance,
  maturity,
}: Readonly<{
  provenance: DraftProvenance;
  maturity: VoiceProfile["maturity"] | undefined;
}>) {
  const t = useT();
  if (!provenance.ai_generated) {
    return null;
  }
  return (
    <section
      className="card compose-disclosure"
      data-testid="ai-disclosure-banner"
    >
      <SectionHeader title={t("compose.aiDisclosureTitle")} />
      <p className="t-body">
        {provenance.ai_disclosure || t("compose.aiDisclosureFallback")}
      </p>
      {provenance.voice_profile_version != null && (
        <>
          <p className="t-caption">
            {t("compose.voiceVersion", { n: provenance.voice_profile_version })}
          </p>
          {maturity === "provisional" && (
            <>
              <Badge>{t("compose.provisional")}</Badge>
              <p className="t-caption">{t("compose.provisionalHint")}</p>
            </>
          )}
        </>
      )}
    </section>
  );
}

// One control's worth of mutation state, flattened so a presentational child
// renders a pending/failed action without speaking react-query. `disabled` is
// wider than `pending`: a control is also barred while a sibling action that
// would contradict it is in flight.
type PendingAction = Readonly<{
  run: () => void;
  pending: boolean;
  disabled: boolean;
  error: string | null;
}>;

// The drafting controls: steer the model, draft, and — only once a served voice
// draft is on screen — reject it. `discard` is null when there is nothing a
// rejection could name, which is what keeps the judgment from being offered
// where it would have no subject.
function DraftBar({
  intent,
  onIntentChange,
  draft,
  discard,
  unavailable,
}: Readonly<{
  intent: string;
  onIntentChange: (next: string) => void;
  draft: PendingAction;
  discard: PendingAction | null;
  unavailable: boolean;
}>) {
  const t = useT();
  return (
    <>
      <div className="compose-draftbar">
        <TextInput
          placeholder={t("compose.intent")}
          value={intent}
          onChange={(event) => onIntentChange(event.target.value)}
        />
        <Button small onClick={draft.run} disabled={draft.disabled}>
          {draft.pending ? t("compose.drafting") : t("compose.draftWithAi")}
        </Button>
        {discard && (
          <Button small onClick={discard.run} disabled={discard.disabled}>
            {t("compose.discardDraft")}
          </Button>
        )}
      </div>
      {discard && <p className="t-caption">{t("compose.discardDraftHint")}</p>}
      {unavailable && (
        <p className="t-caption">{t("compose.draftUnavailable")}</p>
      )}
      {!unavailable && draft.error && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {draft.error}
        </p>
      )}
      {discard?.error && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {discard.error}
        </p>
      )}
    </>
  );
}

// The ways a send is refused for a reason the rep can act on, as opposed to
// failing. Anything outside this list keeps the server's own message on the
// modal's generic error line: inventing copy for a condition this surface does
// not understand would put words in the server's mouth.
type Refusal = "consent" | "mailbox" | "sharedUnsubscribe" | null;

// The consent gate is a sentinel-mapped 409 and names itself at the top level;
// the two pre-flight refusals are 422s, where the top-level code is only ever
// "validation_error" and the rule that fired is the field + code pair the
// server asserted. Matching the field too keeps the copy tied to the input it
// is about: "reconnect your mailbox" is an answer about `from`, and would be
// wrong advice if some later rule refused `recipients` under the same code.
function refusalOf(error: unknown): Refusal {
  if (error instanceof ProblemError && isConsentNotGranted(error.problem)) {
    return "consent";
  }
  for (const { field, code } of problemFieldErrorsOf(error)) {
    if (field === "from" && code === "mailbox_not_send_capable") {
      return "mailbox";
    }
    if (field === "recipients" && code === "shared_unsubscribe_token") {
      return "sharedUnsubscribe";
    }
  }
  return null;
}

// Each refusal states the condition and where it is resolved. The consent gate
// is the default-deny suppression (A22/ADR-0011) this surface exists to make
// visible. A mailbox connected before this product could send holds a read-only
// grant and the provider will not widen one in place, so reconnecting is the
// whole fix. And a message carrying an unsubscribe link carries ONE recipient's
// consent credential, so it may only ever have one addressee.
function SendRefusal({
  refusal,
  personId,
}: Readonly<{ refusal: Refusal; personId?: string }>) {
  const t = useT();
  if (refusal === "consent") {
    return (
      <div className="compose-refusal" role="alert">
        <p className="t-body">
          <strong>{t("compose.consentBlockedTitle")}</strong>
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
    );
  }
  if (refusal === "mailbox") {
    return (
      <div className="compose-refusal" role="alert">
        <p className="t-body">{t("compose.mailboxNotSendCapable")}</p>
        <a href="#/settings/integrations" className="link-button">
          {t("compose.mailboxNotSendCapableGoto")}
        </a>
      </div>
    );
  }
  if (refusal === "sharedUnsubscribe") {
    return (
      <div className="compose-refusal" role="alert">
        <p className="t-body">{t("compose.sharedUnsubscribeToken")}</p>
      </div>
    );
  }
  return null;
}

// What rejecting a draft needs: the reference to name and the profile that
// served it. The pair IS the offer — non-null exactly when the judgment has a
// subject, and the request's own arguments when it is made.
function rejectionTarget(
  draftRef: string | null,
  voiceProfileId: string | null,
): { profileId: string; draftRef: string } | null {
  if (draftRef === null || voiceProfileId === null) {
    return null;
  }
  return { profileId: voiceProfileId, draftRef };
}

// sharedUnsubscribeAhead predicts the refusal above from what is on the form.
// Every purpose but the locked transactional one renders an unsubscribe link,
// and that link is one addressee's own consent record, so a second addressee is
// refused outright. This mirrors the server rule (which remains the authority)
// only to move a certain refusal ahead of the irreversible click.
const TRANSACTIONAL_PURPOSE = "transactional";

function sharedUnsubscribeAhead(
  to: string[],
  cc: string[],
  purpose: string,
): boolean {
  if (purpose === "" || purpose === TRANSACTIONAL_PURPOSE) {
    return false;
  }
  const addressees = new Set(
    [...to, ...cc].map((address) => address.trim().toLowerCase()),
  );
  return addressees.size > 1;
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
  const voiceProfile = useVoiceProfile();
  const [to, setTo] = useState<string[]>([]);
  const [cc, setCc] = useState<string[]>([]);
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [intent, setIntent] = useState("");
  const [purpose, setPurpose] = useState("");
  const [provenance, setProvenance] = useState<DraftProvenance | null>(null);
  // The served voice draft the body in this form came from. It is what lets the
  // server say whether the rep sent the draft or rewrote it, so it may only ever
  // name the text actually on screen.
  const [draftRef, setDraftRef] = useState<string | null>(null);
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
      // Success is the real 2xx WITH a draft body, never merely the absence of
      // an error: openapi-fetch reports a falsy `error` (and undefined `data`)
      // for a bodiless non-2xx (a gateway 502/503/504), which would otherwise
      // fall through as a fabricated draft and crash the fill on undefined
      // fields. Requiring `data` here also re-narrows it for the fill below.
      if (!response.ok || !data) {
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
      if (!body) {
        // The reference and the disclosure both describe the words they were
        // served with, so both ride on exactly the condition that applies
        // them. A re-draft over text the rep already wrote keeps that text —
        // adopting the newer reference would report a stranger's draft as the
        // rep's own edit of it, and adopting the newer disclosure would credit
        // a model, and a voice version, with words a human typed.
        setBody(drafted.body);
        setDraftRef(drafted.draft_ref ?? null);
        setProvenance({
          ai_generated: drafted.ai_generated,
          ai_disclosure: drafted.ai_disclosure,
          voice_profile_version: drafted.voice_profile_version,
        });
      }
      if (to.length === 0 && drafted.to?.length) setTo(drafted.to);
    },
  });

  // Rejecting a draft is a judgment the rep makes, so it has its own control
  // and never rides on closing the dialog. It also may not be guessed at: the
  // reference is deterministic and the drafted signal is inserted once, so a
  // rejection recorded because someone navigated away would silently stand in
  // for the real outcome of an identical draft that is later sent.
  const rejectable = rejectionTarget(draftRef, voiceProfile.data?.id ?? null);
  const discard = useMutation({
    mutationFn: async (rejected: { profileId: string; draftRef: string }) => {
      const { error } = await api.POST(
        "/voice-profiles/{id}/draft-rejections",
        {
          params: { path: { id: rejected.profileId } },
          body: { draft_ref: rejected.draftRef },
        },
      );
      if (error) throw new Error(problemMessage(error));
    },
    onMutate: () => {
      // A rejection and a send are contradictory verdicts on one draft, and
      // whichever reaches the signal first owns it for good. Dropping the
      // reference the moment the judgment leaves means a send that starts
      // anyway carries no draft at all: an unrecorded send, never a message
      // that actually went out filed as rejected. The control withdraws with
      // it, which is where a succeeding rejection leaves the surface anyway.
      setDraftRef(null);
    },
    onError: (_error, rejected) => {
      // The rejection never landed, so the signal is still open and the words
      // on screen are still the ones it named. Restoring the reference keeps
      // the judgment retryable and lets a send that follows report honestly.
      setDraftRef(rejected.draftRef);
    },
    onSuccess: () => {
      // The rejected words leave with the judgment; the recipients the rep
      // addressed are their own work and stay.
      setSubject("");
      setBody("");
      setProvenance(null);
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
            draft_ref: draftRef ?? undefined,
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

  // A refusal is a distinct product state, not a generic failure: it keeps the
  // form open under copy naming the rep's next move, and the raw server detail
  // must not appear alongside it.
  const refusal = refusalOf(send.error);
  const sendError =
    send.isError && refusal === null ? send.error.message : null;
  const canSend =
    to.length > 0 &&
    subject.trim() !== "" &&
    body.trim() !== "" &&
    purpose !== "";
  // While a rejection is in flight the draft it names is being disposed of, so
  // nothing else on this surface may act on that draft: sending would race the
  // rejection for the signal, and re-drafting would hand the rep words the
  // rejection's clear-down is about to wipe.
  const rejectionInFlight = discard.isPending;

  return (
    <ConfirmModal
      open={open}
      onClose={onClose}
      title={t("compose.sendConfirmTitle")}
      tier="confirm"
      confirmLabel={t("compose.send")}
      confirmDisabled={!canSend || rejectionInFlight}
      onConfirm={() => send.mutate()}
      pending={send.isPending}
      error={sendError}
    >
      <div className="compose-fields">
        <DraftBar
          intent={intent}
          onIntentChange={setIntent}
          draft={{
            run: () => draft.mutate(),
            pending: draft.isPending,
            disabled: draft.isPending || rejectionInFlight,
            error: draft.isError ? draft.error.message : null,
          }}
          discard={
            rejectable
              ? {
                  run: () => discard.mutate(rejectable),
                  pending: discard.isPending,
                  // The mirror of the send gate: a rejection may not be
                  // started against a draft already on its way out.
                  disabled: send.isPending,
                  error: discard.isError ? discard.error.message : null,
                }
              : null
          }
          unavailable={draftUnavailable}
        />
        {provenance && (
          <DraftDisclosure
            provenance={provenance}
            maturity={voiceProfile.data?.maturity}
          />
        )}

        <RecipientField label={t("compose.to")} values={to} onChange={setTo} />
        <RecipientField label={t("compose.cc")} values={cc} onChange={setCc} />
        <TextInput
          placeholder={t("compose.subject")}
          value={subject}
          onChange={(event) => setSubject(event.target.value)}
        />
        <textarea
          className="textarea compose-body"
          placeholder={t("compose.body")}
          value={body}
          onChange={(event) => {
            const next = event.target.value;
            setBody(next);
            // An emptied body no longer holds the served draft, and the fill
            // rule above will re-adopt on the next draft. Keeping the old
            // reference across the gap would bind the next send's outcome to
            // words the rep deleted, and keeping the disclosure would announce
            // a model draft over an empty field the rep is about to fill.
            if (!next) {
              setDraftRef(null);
              setProvenance(null);
            }
          }}
        />

        <label className="t-body compose-check">
          {t("compose.purpose")}
          <select
            className="input"
            aria-label={t("compose.purpose")}
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

        {sharedUnsubscribeAhead(to, cc, purpose) && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {t("compose.multiRecipientWarning")}
          </p>
        )}
        {to.length === 0 && (
          <p className="t-caption">{t("compose.emptyRecipients")}</p>
        )}
        {sendUnavailable && (
          <p className="t-caption">{t("compose.sendUnavailable")}</p>
        )}
        <SendRefusal refusal={refusal} personId={personId} />
        <p className="t-caption">{t("compose.sendBody")}</p>
      </div>
    </ConfirmModal>
  );
}

// The per-row action cluster the 360 timelines mount in each entry's action
// slot. Both actions are offered on every row.
//
// Reply, because a send anchored to something that was never mail carries no
// RFC822 identity to thread against and simply starts a conversation — which is
// how the backend already reads it. Gating the composer on an email row instead
// makes a fresh workspace, whose only rows are logged notes, unable to send at
// all.
//
// Relink, because an activity shown on a 360 timeline is by construction already
// linked to the entity whose timeline renders it, so re-associating it to the
// right record is always meaningful — and the Activity list payload carries no
// `links` to gate on regardless.
//
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
  return (
    <>
      <Button small onClick={() => setReply(true)}>
        {t("compose.reply")}
      </Button>
      <Button small onClick={() => setRelink(true)}>
        {t("compose.relink")}
      </Button>
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
