import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  Checkbox,
  SectionHeader,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import { Select, type SelectOption } from "../design-system/select";
import { useT } from "../i18n";
import {
  isConsentNotGranted,
  ProblemError,
  problemFieldErrorsOf,
  problemMessageOf,
  throwProblem,
} from "./common";
import { useOrganization360 } from "./company360";
import { useConsentPurposes } from "./consent";
import { useVoiceProfile } from "./voice-profile";
import "./compose.css";

// The composer surface for the three already-routed ops (draftEmail /
// sendEmail / relinkActivity): a human's edit-then-confirm reply, and a
// mis-captured activity's relink. Pure frontend — every op is live, audited,
// and typed on the backend; this file only calls them.

type Activity = components["schemas"]["Activity"];
type ConsentPurpose = components["schemas"]["ConsentPurpose"];
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
      if (error) throwProblem(error);
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
        throwProblem({ title: t("compose.relinkTarget") });
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
      if (error) throwProblem(error);
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
      error={mutation.isError ? problemMessageOf(mutation.error, t) : null}
    >
      <div className="compose-fields">
        <RecordPicker
          label={t("compose.relinkTarget")}
          searchTargets={search}
          onPick={setTarget}
          selected={target}
        />
        <Checkbox
          className="t-body"
          label={t("compose.relinkReplace")}
          checked={replace}
          onChange={(event) => setReplace(event.target.checked)}
        />
        <p className="t-caption">{t("compose.relinkReplaceHint")}</p>
      </div>
    </ConfirmModal>
  );
}

// Fill the form from a served draft, without ever clobbering a field the rep
// already edited.
//
// The reference, the disclosure and the reasons all describe the WORDS they
// were served with, so all three ride on exactly the condition that applies
// the body. A re-draft over text the rep already wrote keeps that text —
// adopting the newer reference would report a stranger's draft as the rep's
// own edit of it, the newer disclosure would credit a model with words a
// human typed, and the newer reasons would explain a draft nobody is looking
// at.
function fillFromDraft(
  result: Extract<DraftResult, { available: true }>,
  form: Readonly<{
    subject: string;
    body: string;
    toEmpty: boolean;
    setSubject: (next: string) => void;
    setBody: (next: string) => void;
    setTo: (next: string[]) => void;
    setDraftRef: (next: string | null) => void;
    setProvenance: (next: DraftProvenance) => void;
    setReasoning: (next: components["schemas"]["AccountDraftReason"][]) => void;
  }>,
) {
  const drafted = result.draft;
  if (!form.subject) {
    form.setSubject(drafted.subject);
  }
  if (!form.body) {
    form.setBody(drafted.body);
    form.setDraftRef(drafted.draft_ref ?? null);
    form.setProvenance({
      // Absent reads as false, which the contract says outright: a missing
      // flag may not silently become a missing disclosure, but it also may
      // not claim a model wrote text no model touched.
      ai_generated: drafted.ai_generated ?? false,
      ai_disclosure: drafted.ai_disclosure,
      voice_profile_version: drafted.voice_profile_version,
    });
    form.setReasoning(result.reasoning ?? []);
  }
  if (form.toEmpty && drafted.to?.length) {
    form.setTo(drafted.to);
  }
}

// The reply-side draft: it answers the message it is anchored to, so it needs
// nothing but that activity and the caller's optional steering.
async function draftFromActivity({
  activityId,
  intent,
  t,
}: Readonly<{
  activityId: string;
  intent: string;
  t: ReturnType<typeof useT>;
}>): Promise<DraftResult> {
  const { data, error, response } = await api.POST(
    "/activities/{id}/draft-email",
    {
      params: { path: { id: activityId } },
      body: intent.trim() ? { intent: intent.trim() } : {},
    },
  );
  if (response.status === 501) return { available: false as const };
  // Success is the real 2xx WITH a draft body, never merely the absence of an
  // error: openapi-fetch reports a falsy `error` (and undefined `data`) for a
  // bodiless non-2xx (a gateway 502/503/504), which would otherwise fall
  // through as a fabricated draft and crash the fill on undefined fields.
  if (!response.ok || !data) {
    throwProblem(error || { title: t("compose.actionFailed") });
  }
  return { available: true as const, draft: data };
}

// The account-started draft (ADR-0087/A132). It grounds itself in the account
// rather than in a message, so it needs the recipient named before it can say
// anything — that is the one thing this path knows that an empty compose box
// does not.
//
// It answers the same `{available, draft}` shape the reply path does, so the
// fill below cannot tell them apart and the two origins cannot drift into
// different clobber rules.
async function draftFromAccount({
  entityType,
  entityId,
  recipientId,
  dealId,
  intent,
  t,
}: Readonly<{
  entityType: RelinkKind;
  entityId: string;
  recipientId: string;
  dealId: string;
  intent: string;
  t: ReturnType<typeof useT>;
}>): Promise<DraftResult> {
  // Only a company page can ground one: a person or a deal has no 360 to
  // write from, and grounding a message to a contact in some nearby account
  // would be a conversation the rep never chose.
  if (entityType !== "organization" || !recipientId) {
    return { available: false as const };
  }
  const { data, error, response } = await api.POST(
    "/organizations/{id}/draft-email",
    {
      params: { path: { id: entityId } },
      body: {
        person_id: recipientId,
        ...(dealId ? { deal_id: dealId } : {}),
        ...(intent.trim() ? { intent: intent.trim() } : {}),
      },
    },
  );
  if (response.status === 501) return { available: false as const };
  if (!response.ok || !data) {
    throwProblem(error || { title: t("compose.actionFailed") });
  }
  return { available: true as const, draft: data, reasoning: data.reasoning };
}

// What either drafting path answers.
//
// The two wire shapes are not the same — only the account draft carries
// `reasoning` and `generated_by` — so this names the fields the fill actually
// reads and nothing else. Intersecting the two contract types instead would
// make every optional field of one optional on both, and the fill would stop
// noticing when a required field went missing.
type DraftResult =
  | { available: false }
  | {
      available: true;
      draft: Pick<EmailDraft, "subject" | "body" | "to"> &
        Partial<
          Pick<
            EmailDraft,
            | "draft_ref"
            | "ai_generated"
            | "ai_disclosure"
            | "voice_profile_version"
          >
        >;
      // Present only on the account-started path; a reply explains itself by
      // the message it is answering.
      reasoning?: components["schemas"]["AccountDraftReason"][];
    };

// The account-started path's own state: who the draft is grounded on, which
// open deal it is about, and what the server said it wrote from.
//
// Held together because the three move together — a new recipient invalidates
// the reasons as surely as an emptied body does — and held OUT of ComposeModal
// because that component already carries the send, the consent gate, the
// refusal vocabulary and the voice-rejection flow.
function useAccountGrounding(
  personId: string | undefined,
  onGroundingChanged: () => void,
) {
  const [recipientId, setRecipientId] = useState(personId ?? "");
  const [dealId, setDealId] = useState("");
  const [reasoning, setReasoning] = useState<
    components["schemas"]["AccountDraftReason"][]
  >([]);
  // Changing who the draft is to, or which deal it is about, retires the draft
  // that was written for the previous pair. Leaving it would show a message
  // addressed to B carrying A's words, A's disclosure and A's reasons — and
  // re-drafting could not repair it, because the fill never clobbers a
  // non-empty field.
  const reground = (apply: (next: string) => void) => (next: string) => {
    apply(next);
    setReasoning([]);
    onGroundingChanged();
  };
  return {
    recipientId,
    setRecipientId: reground(setRecipientId),
    dealId,
    setDealId: reground(setDealId),
    reasoning,
    setReasoning,
  };
}

// A freeform email-chip input: typed address + Enter/comma (or blur) adds a
// chip, the X icon removes it. No client-side email regex beyond type=email —
// the server is the authority (422 on a malformed address), so this never
// rejects what the backend might accept.
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

// The account-started path's two choices: who this is to, and which open deal
// it is about.
//
// Both are read off the account's own 360 rather than a fresh search, for the
// reason the whole draft is: the endpoint grounds itself in the caller's view
// of the account, so a contact this picker offers that the view does not carry
// would be one the draft then refuses.
function AccountDraftContext({
  orgId,
  recipientId,
  onRecipientChange,
  dealId,
  onDealChange,
}: Readonly<{
  orgId: string;
  recipientId: string;
  onRecipientChange: (next: string) => void;
  dealId: string;
  onDealChange: (next: string) => void;
}>) {
  const t = useT();
  const query = useOrganization360(orgId);
  // An overlay workspace has no native 360 to ground from; the endpoint
  // refuses there too, so the pickers simply have nothing to offer.
  const view = query.data?.state === "ready" ? query.data.view : undefined;
  const contacts = view?.people?.data ?? [];
  const deals = view?.deals?.data ?? [];
  // No contact on the account is an honest dead end for a GROUNDED draft, and
  // saying so beats an empty picker the rep tries and cannot use. They can
  // still type an address into To and write the mail themselves.
  if (query.isSuccess && contacts.length === 0) {
    return <p className="t-caption">{t("compose.noGroundableRecipient")}</p>;
  }
  return (
    <>
      <label className="t-body compose-check">
        {t("compose.draftTo")}
        <Select
          aria-label={t("compose.draftTo")}
          options={[
            { value: "", label: t("compose.draftToUnset") },
            ...contacts.map((contact) => ({
              value: contact.person_id,
              label: contact.full_name,
            })),
          ]}
          value={recipientId}
          onChange={onRecipientChange}
        />
      </label>
      {deals.length > 0 && (
        <label className="t-body compose-check">
          {t("compose.relatedTo")}
          <Select
            aria-label={t("compose.relatedTo")}
            options={[
              { value: "", label: t("compose.relatedToNone") },
              ...deals.map((deal) => ({
                value: deal.deal_id,
                label: deal.name,
              })),
            ]}
            value={dealId}
            onChange={onDealChange}
          />
        </label>
      )}
    </>
  );
}

// A reason's chip opens the record it names. Only the two kinds that HAVE a
// screen are routed: a fact or a profile field has a receipt rather than a
// page, and this dialog is the wrong place to open one over.
function openCited(entityType: string, entityId: string) {
  if (entityType === "deal") {
    navigate({ screen: "deals", id: entityId });
  }
  if (entityType === "person") {
    navigate({ screen: "contacts", id: entityId });
  }
}

// What the draft was written from, in the two shapes State D draws: a "Based
// on" line naming the inputs in order, and a row of "Why this draft?" chips.
//
// Both render the SAME reasons — they are one answer read two ways, which is
// why the server sends parts rather than a sentence. The line is for scanning
// before reading the draft; the chips are for checking one input after.
//
// A reason carrying evidence is pressable and opens the record it names. One
// without — the rep's own instruction — is flat, because there is nothing to
// open and a chip that looks pressable and is not is worse than a plain one.
function DraftReasons({
  reasons,
  onOpenRecord,
}: Readonly<{
  reasons: readonly components["schemas"]["AccountDraftReason"][];
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  if (reasons.length === 0) {
    return null;
  }
  return (
    <div className="compose-reasons">
      <p className="t-caption">
        {t("compose.basedOn", {
          inputs: reasons.map((reason) => reason.label).join(" · "),
        })}
      </p>
      <p className="t-caption">{t("compose.whyThisDraft")}</p>
      <ul className="chips">
        {reasons.map((reason) => (
          <li key={`${reason.kind}:${reason.label}`}>
            {reason.evidence_ref && onOpenRecord ? (
              <button
                type="button"
                className="link-button"
                onClick={() =>
                  onOpenRecord(
                    reason.evidence_ref?.entity_type ?? "",
                    reason.evidence_ref?.entity_id ?? "",
                  )
                }
              >
                {reason.label}
              </button>
            ) : (
              reason.label
            )}
          </li>
        ))}
      </ul>
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
      {/* Both failures appear without any navigation, so they are announced
          rather than merely coloured: a rep who cannot see the line has to be
          told the draft or the rejection did not land, on the same terms the
          send refusals are announced. */}
      {!unavailable && draft.error && (
        <p
          className="t-caption"
          role="alert"
          style={{ color: "var(--danger)" }}
        >
          {draft.error}
        </p>
      )}
      {discard?.error && (
        <p
          className="t-caption"
          role="alert"
          style={{ color: "var(--danger)" }}
        >
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

// The purpose list the rep chooses from. The unset entry is a real OPTION
// rather than the select's placeholder: a placeholder is only a face for an
// unset value, and a rep who picked a purpose has to be able to come back to
// none before sending. Its face stays the em dash the field has always shown —
// a glyph, with no words to translate.
function purposeOptions(
  purposes: readonly ConsentPurpose[] | undefined,
): SelectOption[] {
  return [
    { value: "", label: "—" },
    ...(purposes ?? []).map((purpose) => ({
      value: purpose.key,
      label: purpose.label,
    })),
  ];
}

// Send preconditions differ by wire shape: mail needs an addressee and a
// subject on top of a body; a channel reply carries neither (design §9.3 —
// the recipient is resolved server-side, and a channel has no subject line),
// so only the words and the consent purpose gate it.
// The one place the send's ORIGIN is chosen, so the three transports cannot
// drift into different bodies. An anchor sends the reply the composer has
// always sent; no anchor sends the account-started twin, which carries the
// records it is filed under instead of inheriting them from a conversation
// (ADR-0087 §1). A channel reply is anchor-only by nature — there is no such
// thing as starting a Telegram thread with a stranger from a company page —
// so it keeps the anchored path and never reaches the account arm.
async function sendFrom(args: {
  activityId?: string;
  isTelegram: boolean;
  mail: {
    subject: string;
    body: string;
    to: string[];
    cc?: string[];
    draft_ref?: string;
    consent_purpose: string;
  };
  channelBody: { body: string; consent_purpose: string };
  links: { entity_type: RelinkKind; entity_id: string }[];
}) {
  if (args.isTelegram) {
    if (!args.activityId) {
      // A channel reply answers a conversation the person opened; there is no
      // way to start one with a stranger from a company page. Falling through
      // to the mail arm would post a channel message as an account EMAIL —
      // wrong transport, wrong body shape, and a message the rep never meant
      // to send by mail. No caller constructs this today; the refusal is what
      // keeps that true.
      throw new Error(
        "compose: a channel reply needs the conversation it answers",
      );
    }
    return api.POST("/activities/{id}/send-message", {
      params: { path: { id: args.activityId } },
      body: args.channelBody,
    });
  }
  if (args.activityId) {
    return api.POST("/activities/{id}/send-email", {
      params: { path: { id: args.activityId } },
      body: args.mail,
    });
  }
  return api.POST("/emails", { body: { ...args.mail, links: args.links } });
}

function canSendCompose(
  isTelegram: boolean,
  fields: { to: string[]; subject: string; body: string; purpose: string },
): boolean {
  if (isTelegram) {
    return fields.body.trim() !== "" && fields.purpose !== "";
  }
  return (
    fields.to.length > 0 &&
    fields.subject.trim() !== "" &&
    fields.body.trim() !== "" &&
    fields.purpose !== ""
  );
}

// The mail-only half of the composer: AI drafting (there is no draft-message
// endpoint for a channel) plus the recipient/subject inputs a channel reply's
// request shape has no room for. Kept as its own component so a channel
// reply — which renders none of this — doesn't inherit its branching.
function MailOnlyFields({
  intent,
  onIntentChange,
  draft,
  discard,
  draftUnavailable,
  provenance,
  voiceMaturity,
  to,
  onToChange,
  cc,
  onCcChange,
  subject,
  onSubjectChange,
  rejectionInFlight,
}: Readonly<{
  intent: string;
  onIntentChange: (next: string) => void;
  draft: PendingAction;
  discard: PendingAction | null;
  draftUnavailable: boolean;
  provenance: DraftProvenance | null;
  voiceMaturity: VoiceProfile["maturity"] | undefined;
  to: string[];
  onToChange: (next: string[]) => void;
  cc: string[];
  onCcChange: (next: string[]) => void;
  subject: string;
  onSubjectChange: (next: string) => void;
  rejectionInFlight: boolean;
}>) {
  const t = useT();
  return (
    <>
      <DraftBar
        intent={intent}
        onIntentChange={onIntentChange}
        draft={draft}
        discard={discard}
        unavailable={draftUnavailable}
      />
      {provenance && (
        <DraftDisclosure provenance={provenance} maturity={voiceMaturity} />
      )}
      <RecipientField
        label={t("compose.to")}
        values={to}
        onChange={onToChange}
      />
      <RecipientField
        label={t("compose.cc")}
        values={cc}
        onChange={onCcChange}
      />
      <TextInput
        placeholder={t("compose.subject")}
        value={subject}
        disabled={rejectionInFlight}
        onChange={(event) => onSubjectChange(event.target.value)}
      />
    </>
  );
}

// The two mail-only send-time notices: a shared-unsubscribe-token risk and an
// empty recipient list. Neither concept exists on a channel reply — there is
// no addressee list to warn about — so this renders nothing there.
function MailSendNotices({
  to,
  cc,
  purpose,
}: Readonly<{ to: string[]; cc: string[]; purpose: string }>) {
  const t = useT();
  return (
    <>
      {sharedUnsubscribeAhead(to, cc, purpose) && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {t("compose.multiRecipientWarning")}
        </p>
      )}
      {to.length === 0 && (
        <p className="t-caption">{t("compose.emptyRecipients")}</p>
      )}
    </>
  );
}

// The 🟡 confirm-first composer (draftEmail + sendEmail), extended for a
// captured messaging channel (sendMessage): on a `kind === "telegram"`
// activity the reply posts /activities/{id}/send-message instead, dropping
// subject and Cc — a channel has no concept of either, and the recipient is
// never named by the caller (design §9.3): the server resolves it from the
// conversation's own channel identity. Everything else — the confirm-first
// interaction, AI drafting (mail-only; there is no draft-message endpoint),
// the 409 consent rendering, and the post-send timeline refresh — is shared.
// Draft with AI fills the fields; the human edits and confirms; the human's
// own click IS the approval (ADR-0055), so the human REST path sends no
// X-Approval-Token and no Idempotency-Key — that plumbing is the
// agent/passport path. The 409 consent gate is the whole reason this surface
// exists: the default-deny suppression (A22/ADR-0011) has never been visible
// to a user before.
// The drafting call, both origins, lifted out of ComposeModal — which already
// carries the send, the consent gate, the refusal vocabulary and the
// voice-rejection flow, and was over the complexity bar with this inside it.
//
// The two paths answer the same shape on purpose, so the fill below cannot
// tell them apart and the origins cannot drift into different clobber rules.
function useDraftMutation({
  activityId,
  entityType,
  entityId,
  intent,
  account,
  onUnavailable,
  onDrafted,
  resetUnavailable,
  t,
}: Readonly<{
  activityId?: string;
  entityType: RelinkKind;
  entityId: string;
  intent: string;
  account: ReturnType<typeof useAccountGrounding>;
  onUnavailable: () => void;
  onDrafted: (result: Extract<DraftResult, { available: true }>) => void;
  resetUnavailable: () => void;
  t: ReturnType<typeof useT>;
}>) {
  return useMutation({
    mutationFn: async (): Promise<DraftResult> => {
      resetUnavailable();
      // A reply answers the message it is anchored to; an account-started
      // message has none, so it is grounded in the account itself and needs
      // the recipient named first.
      if (activityId) {
        return draftFromActivity({ activityId, intent, t });
      }
      return draftFromAccount({
        entityType,
        entityId,
        recipientId: account.recipientId,
        dealId: account.dealId,
        intent,
        t,
      });
    },
    onSuccess: (result) => {
      if (!result.available) {
        onUnavailable();
        return;
      }
      onDrafted(result);
    },
  });
}

// biome-ignore lint/complexity/noExcessiveCognitiveComplexity: this modal was already at the ceiling; the account-started origin (ADR-0087/A132) adds three necessary branches — the recipient/deal pickers, the grounded-draft gate and the drawer placement. The drafting call, the form fill, the body edit and both draft controls are already extracted; what is left is one dialog's own wiring, and splitting the send/consent/refusal/voice flow apart from the fields it gates would scatter it.
export function ComposeModal({
  activityId,
  entityType,
  entityId,
  personId,
  kind,
  open,
  onClose,
}: Readonly<{
  // Absent means this is an ACCOUNT-STARTED send: a new conversation with no
  // prior message to anchor to (ADR-0087 §1). It is the same send either way —
  // only the origin differs — so the drafting, consent, refusal and provenance
  // behaviour below is shared rather than forked. A reply keeps threading and
  // inherits its links; an account-started message roots its own thread and
  // files itself under the record it was started from.
  activityId?: string;
  entityType: RelinkKind;
  entityId: string;
  personId?: string;
  // Undefined (or any non-channel kind) keeps the mail behaviour this modal
  // already had before a channel existed — every mail test renders this
  // component without ever naming a kind.
  kind?: Activity["kind"];
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const purposes = useConsentPurposes();
  const voiceProfile = useVoiceProfile();
  const isTelegram = kind === "telegram";
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
  // Retiring the previous pair's draft is the composer's job, not the
  // grounding hook's: the body, the recipients, the reference and the
  // disclosure all live here.
  //
  // Only DRAFTED text is cleared. `draftRef` and `provenance` are set only by
  // the fill, so their presence is what says the words on screen came from a
  // draft rather than from the rep — and a rep who typed their own message
  // and then picked a different contact must not lose it.
  const account = useAccountGrounding(personId, () => {
    if (!provenance && !draftRef) {
      return;
    }
    setBody("");
    setSubject("");
    setTo([]);
    setDraftRef(null);
    setProvenance(null);
  });
  // Whether this composer can ground a draft in an account: an account-started
  // message on a company page, over mail. A channel reply resolves its
  // recipient server-side and has no draft endpoint at all.
  const groundable =
    !activityId && entityType === "organization" && !isTelegram;

  // An emptied body no longer holds the served draft, so everything that
  // describes those words goes with it: the reference would bind the next
  // send's outcome to text the rep deleted, the disclosure would announce a
  // model draft over an empty field, and the reasons would explain a draft
  // that is no longer there. The fill re-adopts all three on the next draft.
  const editBody = (next: string) => {
    setBody(next);
    if (next) {
      return;
    }
    setDraftRef(null);
    setProvenance(null);
    account.setReasoning([]);
  };

  const draft = useDraftMutation({
    activityId,
    entityType,
    entityId,
    intent,
    account,
    onUnavailable: () => setDraftUnavailable(true),
    onDrafted: (result) =>
      fillFromDraft(result, {
        subject,
        body,
        toEmpty: to.length === 0,
        setSubject,
        setBody,
        setTo,
        setDraftRef,
        setProvenance,
        setReasoning: account.setReasoning,
      }),
    resetUnavailable: () => setDraftUnavailable(false),
    t,
  });

  // Rejecting a draft is a judgment the rep makes, so it has its own control
  // and never rides on closing the dialog. It also may not be guessed at: the
  // reference is deterministic and the drafted signal is inserted once, so a
  // rejection recorded because someone navigated away would silently stand in
  // for the real outcome of an identical draft that is later sent.
  const rejectable = rejectionTarget(draftRef, voiceProfile.data?.id ?? null);
  const discard = useMutation({
    mutationFn: async (rejected: { profileId: string; draftRef: string }) => {
      const { error, response } = await api.POST(
        "/voice-profiles/{id}/draft-rejections",
        {
          params: { path: { id: rejected.profileId } },
          body: { draft_ref: rejected.draftRef },
        },
      );
      // The rejection landed only on a real 2xx. openapi-fetch reports a falsy
      // `error` for a bodiless non-2xx (a gateway 502/503/504), and treating
      // that as success would clear the draft off this surface while the
      // server's signal is still open — the rep would be told their verdict
      // was recorded when it never left the building.
      if (!response.ok) {
        throwProblem(error || { title: t("compose.actionFailed") });
      }
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
      // No X-Approval-Token, no Idempotency-Key on either path: the human's
      // own click IS the approval on the REST path (ADR-0055).
      const mail = {
        subject,
        body,
        to,
        cc: cc.length ? cc : undefined,
        draft_ref: draftRef ?? undefined,
        consent_purpose: purpose,
      };
      const { data, error, response } = await sendFrom({
        activityId,
        isTelegram,
        mail,
        channelBody: { body, consent_purpose: purpose },
        links: [{ entity_type: entityType, entity_id: entityId }],
      });
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
    send.isError && refusal === null ? problemMessageOf(send.error, t) : null;
  const canSend = canSendCompose(isTelegram, { to, subject, body, purpose });
  // While a rejection is in flight the draft it names is being disposed of, so
  // nothing else on this surface may act on that draft: sending would race the
  // rejection for the signal, and re-drafting would hand the rep words the
  // rejection's clear-down is about to wipe. The drafted text is frozen for the
  // same span — a failed rejection hands its reference back, and a reference
  // may only ever name the words on screen, never words typed while it was
  // away.
  const rejectionInFlight = discard.isPending;
  // The two draft controls, assembled here so the JSX below reads as a layout
  // rather than as a list of conditions.
  //
  // An account-started draft grounds itself on the recipient, so there is
  // nothing to draft until one is chosen: the button is disabled with the
  // picker directly above it, rather than running and coming back with a
  // refusal about a field already on screen.
  const draftControl = {
    run: () => draft.mutate(),
    pending: draft.isPending,
    disabled:
      draft.isPending ||
      rejectionInFlight ||
      (groundable && !account.recipientId),
    error: draft.isError ? problemMessageOf(draft.error, t) : null,
  };
  // The mirror of the send gate: a rejection may not be started against a
  // draft already on its way out.
  // The account-started path's two additions to the form, built here so the
  // JSX below reads as a layout rather than as a list of conditions.
  //
  // The pickers come FIRST because a grounded draft with no recipient has no
  // relationship to stand on; the reasons come above the body because they are
  // what the body is standing on, and a reader checks the inputs before the
  // prose rather than after it.
  const accountContext = groundable ? (
    <AccountDraftContext
      orgId={entityId}
      recipientId={account.recipientId}
      onRecipientChange={account.setRecipientId}
      dealId={account.dealId}
      onDealChange={account.setDealId}
    />
  ) : null;
  const accountReasons = (
    <DraftReasons reasons={account.reasoning} onOpenRecord={openCited} />
  );
  const discardControl = rejectable
    ? {
        run: () => discard.mutate(rejectable),
        pending: discard.isPending,
        disabled: send.isPending,
        error: discard.isError ? problemMessageOf(discard.error, t) : null,
      }
    : null;
  return (
    <ConfirmModal
      open={open}
      onClose={onClose}
      title={t(
        isTelegram
          ? "compose.sendMessageConfirmTitle"
          : "compose.sendConfirmTitle",
      )}
      tier="confirm"
      // The rep is about to send irreversibly, so the body they are
      // confirming has to be readable at a glance rather than through a
      // five-line porthole — and the Send button has to sit above the fold,
      // not below a scroll.
      size="wide"
      // A DRAWER for the account-started message, so the account it is about
      // stays on screen beside it (mockup State D). A reply keeps the centred
      // box: its context is the thread in the dialog, not the page behind.
      placement={groundable ? "right" : "center"}
      confirmLabel={t("compose.send")}
      confirmDisabled={!canSend || rejectionInFlight}
      onConfirm={() => send.mutate()}
      pending={send.isPending}
      error={sendError}
    >
      <div className="compose-fields">
        {accountContext}
        {/* AI drafting is mail-only — there is no draft-message endpoint, and
            a channel reply's recipient is resolved server-side, so neither
            the draft controls nor the To/Cc/Subject fields apply to it. */}
        {!isTelegram && (
          <MailOnlyFields
            intent={intent}
            onIntentChange={setIntent}
            draft={draftControl}
            discard={discardControl}
            draftUnavailable={draftUnavailable}
            provenance={provenance}
            voiceMaturity={voiceProfile.data?.maturity}
            to={to}
            onToChange={setTo}
            cc={cc}
            onCcChange={setCc}
            subject={subject}
            onSubjectChange={setSubject}
            rejectionInFlight={rejectionInFlight}
          />
        )}
        {accountReasons}
        <Textarea
          className="compose-body"
          aria-label={t("compose.body")}
          placeholder={t("compose.body")}
          value={body}
          disabled={rejectionInFlight}
          onChange={(event) => editBody(event.target.value)}
        />

        <label className="t-body compose-check">
          {t("compose.purpose")}
          <Select
            aria-label={t("compose.purpose")}
            options={purposeOptions(purposes.data?.data)}
            value={purpose}
            onChange={setPurpose}
          />
        </label>
        <p className="t-caption">{t("compose.purposeHint")}</p>

        {!isTelegram && <MailSendNotices to={to} cc={cc} purpose={purpose} />}
        {sendUnavailable && (
          <p className="t-caption">{t("compose.sendUnavailable")}</p>
        )}
        <SendRefusal refusal={refusal} personId={personId} />
        <p className="t-caption">
          {t(isTelegram ? "compose.sendMessageBody" : "compose.sendBody")}
        </p>
      </div>
    </ConfirmModal>
  );
}

// A channel reply can only land on a live, unblocked identity, and the
// failure otherwise arrives after the rep has already written the message —
// worse than never offering the box (design §9.3). Reachability is read off
// the person the row's own timeline names: `["person", personId]` is the same
// query key the 360 screen already fetches under, so this rides its cache
// instead of opening a second request. A caller that never learned a personId
// (e.g. a deal timeline, which has no single person to check) gets the
// pre-existing behaviour of always offering the reply — this only ever turns
// the action OFF, never on, for a row it cannot verify.
function useTelegramReachable(
  isChannel: boolean,
  personId: string | undefined,
) {
  const person = useQuery({
    queryKey: ["person", personId],
    queryFn: async () => {
      const { data, error } = await api.GET("/people/{id}", {
        params: { path: { id: personId as string } },
      });
      if (error) throwProblem(error);
      return data;
    },
    enabled: isChannel && personId != null,
  });
  if (!isChannel || personId == null) return true;
  return (person.data?.reachability ?? []).some(
    (channel) => channel.provider === "telegram" && channel.reachable,
  );
}

// The per-row action cluster the 360 timelines mount in each entry's action
// slot.
//
// Reply, because a send anchored to something that was never mail carries no
// RFC822 identity to thread against and simply starts a conversation — which is
// how the backend already reads it. Gating the composer on an email row instead
// makes a fresh workspace, whose only rows are logged notes, unable to send at
// all. A `telegram` row carries the opposite gate: it is withheld, not always
// offered, when the person behind it cannot be reached (see
// useTelegramReachable above).
//
// Relink, because an activity shown on a 360 timeline is by construction already
// linked to the entity whose timeline renders it, so re-associating it to the
// right record is always meaningful — and the Activity list payload carries no
// `links` to gate on regardless. It is offered unconditionally.
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
  const reachable = useTelegramReachable(
    activity.kind === "telegram",
    personId,
  );
  return (
    <>
      {reachable && (
        <Button small onClick={() => setReply(true)}>
          {t("compose.reply")}
        </Button>
      )}
      <Button small onClick={() => setRelink(true)}>
        {t("compose.relink")}
      </Button>
      {reply && (
        <ComposeModal
          activityId={activity.id}
          entityType={entityType}
          entityId={entityId}
          personId={personId}
          kind={activity.kind}
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
