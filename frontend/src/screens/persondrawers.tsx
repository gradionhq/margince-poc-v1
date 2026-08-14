import { useMutation, useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  Check,
  ExternalLink,
  Send,
  Sparkles,
  X,
} from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, Modal, TextInput } from "../design-system/atoms";
import { RichText } from "../design-system/richtext";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import { refusalOf, SendRefusal } from "./compose";
import { useConsentPurposes } from "./consent";
import { PersonProviderSection } from "./personprovider";

// Only http(s) may become a link. A provider-supplied URL is untrusted input,
// and a javascript: or data: href executes the moment a reader clicks it.
function isWebUrl(href: string): boolean {
  try {
    const scheme = new URL(href).protocol;
    return scheme === "https:" || scheme === "http:";
  } catch {
    // Not parseable as an absolute URL. A relative href would resolve against
    // OUR origin, which a provider's source document never is.
    return false;
  }
}

// The three surfaces the person page opens over itself: the composer, the
// research drawer, and the meeting brief.
//
// All three are the WIDE drawer — a rep works in them rather than glancing at
// them — and all three leave the page behind visible, because the record is
// the context that makes the drawer's content mean anything.

type Person360 = components["schemas"]["Person360"];
type PersonConsentGuard = components["schemas"]["PersonConsentGuard"];

// --- The composer (State D) ------------------------------------------------

// What the send button says, in the three states it can be in. A sent message
// keeps saying so: the button is disabled afterwards, and a label that returned
// to "Send" would invite a second copy of a mail already delivered.
function sendPhase(
  send: Readonly<{ isPending: boolean; isSuccess: boolean }>,
  t: ReturnType<typeof useT>,
): string {
  if (send.isSuccess) {
    return t("person.composer.sent");
  }
  return send.isPending
    ? t("person.composer.sending")
    : t("person.composer.send");
}

// The steering field and the button that uses it.
//
// Its own component because the composer around it is at the complexity
// ceiling, and because these two controls are ONE action: what the mail should
// be about, and the request to write it.
function DraftBar({
  intent,
  onIntentChange,
  pending,
  error,
  onDraft,
}: Readonly<{
  intent: string;
  onIntentChange: (next: string) => void;
  pending: boolean;
  error: unknown;
  onDraft: () => void;
}>) {
  const t = useT();
  return (
    <>
      {/* Steering, then the draft. The order matters: a rep who types what the
          mail is about before pressing the button gets a draft about that, and
          a rep who presses it first gets the record's own reading. */}
      <label className="pe-field-label" htmlFor="composer-intent">
        {t("person.composer.intent")}
      </label>
      <div className="pe-draft-row">
        <TextInput
          id="composer-intent"
          value={intent}
          placeholder={t("person.composer.intentHint")}
          onChange={(event) => onIntentChange(event.target.value)}
        />
        <Button small disabled={pending} onClick={onDraft}>
          <Sparkles size={14} aria-hidden="true" />
          {pending
            ? t("person.composer.drafting")
            : t("person.composer.draftWithAi")}
        </Button>
      </div>
      {error != null && (
        <p className="pe-send-error">{problemMessageOf(error, t)}</p>
      )}
    </>
  );
}

// The guard entry that governs THIS send.
//
// Consent is decided per purpose, so the guard carries one email entry per
// purpose — business correspondence, transactional, marketing. "May I email
// them" therefore has no answer until the rep says what for, and reading the
// first email entry would let whichever purpose sorts first speak for all of
// them: a send the server permits would be refused here, under a reason
// belonging to a purpose nobody chose.
function emailGuardFor(guard: PersonConsentGuard | undefined, purpose: string) {
  if (!purpose) {
    return undefined;
  }
  return guard?.entries.find(
    (entry) => entry.channel === "email" && entry.purpose_key === purpose,
  );
}

// Everything a send needs before the button may be pressed. Separate from the
// consent verdict beside it: one asks whether the message is complete, the
// other whether it may go at all, and a rep denied for the wrong reason cannot
// tell which they must fix.
function canSend(
  fields: Readonly<{
    recipient: string;
    subject: string;
    body: string;
    purpose: string;
  }>,
): boolean {
  return (
    fields.recipient !== "" &&
    fields.subject.trim() !== "" &&
    fields.body.trim() !== "" &&
    fields.purpose !== ""
  );
}

// The consent purpose this send falls under. Its own component because the
// composer is at the complexity ceiling, and because the list comes from the
// server: a workspace's purposes are configuration, not a fixed enum.
function PurposePicker({
  purpose,
  onChange,
}: Readonly<{ purpose: string; onChange: (next: string) => void }>) {
  const t = useT();
  const purposes = useConsentPurposes();
  return (
    <>
      <label className="pe-field-label" htmlFor="composer-purpose">
        {t("person.composer.purpose")}
      </label>
      <Select
        aria-label={t("person.composer.purpose")}
        options={[
          { value: "", label: "—" },
          ...(purposes.data?.data ?? []).map((entry) => ({
            value: entry.key,
            label: entry.label,
          })),
        ]}
        value={purpose}
        onChange={onChange}
      />
    </>
  );
}

// Why a send was refused, in terms the rep can act on.
//
// The three named refusals are the SAME ones the company composer meets, so
// they are named once rather than twice: a consent gate that says which record
// to open, a mailbox whose grant predates sending, and an unsubscribe token
// belonging to one recipient. Anything else falls back to the problem's own
// message, which is still better than "something went wrong".
function SendFailure({
  error,
  personId,
}: Readonly<{ error: unknown; personId: string }>) {
  const t = useT();
  if (error == null) {
    return null;
  }
  const refusal = refusalOf(error);
  if (refusal !== null) {
    return <SendRefusal refusal={refusal} personId={personId} />;
  }
  return <p className="pe-send-error">{problemMessageOf(error, t)}</p>;
}

// What to do about a send this person's consent state refuses.
//
// A verdict on its own is a dead end: the composer said why it would not send
// and offered nothing, so a rep who hit it had to ask somebody. Three moves
// exist and this names the ones that apply — switch to a purpose that IS
// allowed for this person, open their consent record, or wait for them to
// write, which lifts business correspondence on its own.
//
// It renders nothing until a purpose is chosen, because "no purpose picked" is
// not a refusal and offering a way out of it would read as one.
function ConsentWayOut({
  purpose,
  guard,
}: Readonly<{
  purpose: string;
  guard: PersonConsentGuard | undefined;
}>) {
  const t = useT();
  const entry = emailGuardFor(guard, purpose);
  if (purpose === "" || entry === undefined || entry.verdict === "allowed") {
    return null;
  }
  return (
    <div className="pe-consent-wayout">
      <p className="t-caption">{t("person.composer.blockedLead")}</p>
      <ul className="pe-consent-moves">
        <li className="t-caption">{t("person.composer.blockedRewrite")}</li>
        <li className="t-caption">
          {t("person.composer.blockedRecordConsent")}
        </li>
      </ul>
    </div>
  );
}

// A plain-text draft as paragraphs, for the editor to open on.
//
// The drafting prompts forbid markup, so what arrives is text with blank lines
// between paragraphs. Rendering it as one block would lose those breaks the
// moment the rep touches the formatting toolbar.
function paragraphsFrom(text: string): string {
  const escaped = (line: string) =>
    line
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;");
  return text
    .split(/\n{2,}/)
    .map((block) => block.trim())
    .filter((block) => block !== "")
    .map((block) => `<p>${escaped(block).replaceAll("\n", "<br>")}</p>`)
    .join("");
}

// The composer's own head: who it is to, and whether it may go at all.
//
// Its own component because the verdict line has three states a reader must be
// able to tell apart — allowed, refused, and no purpose chosen yet — and the
// composer around it is at the complexity ceiling.
function ComposerHead({
  name,
  allowed,
  reason,
  purpose,
  guard,
  onClose,
}: Readonly<{
  name: string;
  allowed: boolean;
  reason: string | undefined;
  purpose: string;
  guard: PersonConsentGuard | undefined;
  onClose: () => void;
}>) {
  const t = useT();
  return (
    <div className="drawer-head">
      <div className="pe-drawer-title">
        <h2 id="person-composer-title">
          {t("person.composer.title", { name })}
        </h2>
        <Button small onClick={onClose} aria-label={t("person.drawer.close")}>
          <X size={16} aria-hidden="true" />
        </Button>
      </div>
      {/* The consent verdict leads, with its reason. A rep about to write
          needs to know whether they may send BEFORE they spend words on it —
          and until a purpose is chosen the honest line is that the question
          has not been asked yet, not a verdict borrowed from another purpose. */}
      <div
        className={
          allowed
            ? "pe-consent-line"
            : "pe-consent-line pe-consent-line-blocked"
        }
      >
        <Check size={15} aria-hidden="true" />
        <span>
          {reason ??
            (purpose
              ? t("person.composer.consentUnknown")
              : t("person.composer.consentPickPurpose"))}
        </span>
      </div>
      <ConsentWayOut purpose={purpose} guard={guard} />
    </div>
  );
}

export function PersonComposer({
  personId,
  view,
  guard,
  open,
  intent: initialIntent,
  onClose,
}: Readonly<{
  personId: string;
  view: Person360;
  guard: PersonConsentGuard | undefined;
  open: boolean;
  // What the rep (or the moment action that opened this) wants the draft to be
  // about. A rung that fired knows WHY — "their reply is overdue", "the meeting
  // needs an agenda" — and that reason shaped nothing until it arrived here.
  intent?: string;
  onClose: () => void;
}>) {
  const t = useT();
  const [subject, setSubject] = useState("");
  // The two renderings of one message. body is what a text client receives and
  // what every existing gate reads; html is the markup alternative beside it.
  const [body, setBody] = useState("");
  const [html, setHtml] = useState("");
  // Keyed on the intent the caller supplied, so a SECOND moment action opening
  // the same composer replaces the first one's intent instead of leaving the
  // rep with the reason the previous button had. useState alone seeds once and
  // would silently ignore every later action.
  const [intent, setIntent] = useState(initialIntent ?? "");
  const [seededFrom, setSeededFrom] = useState(initialIntent ?? "");
  if ((initialIntent ?? "") !== seededFrom) {
    setSeededFrom(initialIntent ?? "");
    setIntent(initialIntent ?? "");
  }
  const [purpose, setPurpose] = useState("");

  // One spelling of "this composer holds no message", for the two moments that
  // need it: the recipient changing, and a send succeeding.
  const clearMessage = () => {
    setSubject("");
    setBody("");
    setHtml("");
    setPurpose("");
  };

  // The composer belongs to ONE recipient. The person page reuses this
  // component when the contact changes — same element, new id — so without this
  // the subject and both renderings written for A stay loaded while the To
  // line, the links and the consent verdict all switch to B. A rep who pressed
  // Send would disclose A's message to B.
  const [wroteFor, setWroteFor] = useState(personId);
  if (wroteFor !== personId) {
    setWroteFor(personId);
    clearMessage();
  }

  // Drafting is a BUTTON, not something the drawer does to you. Opening a
  // composer to write two sentences yourself should not spend the workspace's
  // model budget, and a draft that arrives unasked is one the rep has to read
  // before they can ignore it. The company composer has always worked this way.
  const draft = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/people/{id}/draft-email", {
        params: { path: { id: personId } },
        body: intent.trim() ? { intent: intent.trim() } : {},
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (written) => {
      // A draft arriving after the rep started writing must not replace their
      // words: they asked for a suggestion, not for their own sentence to be
      // taken away. An empty composer is the case this is for.
      if (written && subject.trim() === "" && body.trim() === "") {
        setSubject(written.subject);
        setBody(written.body);
        // The model writes PLAIN text by contract, so the markup alternative
        // starts as that text in paragraphs rather than one run-on block. A
        // rep formats from there; nothing is invented on their behalf.
        setHtml(paragraphsFrom(written.body));
      }
    },
  });

  const email = emailGuardFor(guard, purpose);
  const allowed = email?.verdict === "allowed";
  const recipient = view.person.emails?.[0]?.email ?? "";

  // The send itself. It was a setState — the button said "Staged for approval"
  // and no request left the browser, so every draft written here was
  // unsendable. A person-started message opens a new conversation, so it is
  // POST /emails (the anchored /activities/{id}/send-email answers one), and it
  // carries the person as its link.
  const send = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/emails", {
        body: {
          subject,
          body,
          // Omitted entirely when the rep formatted nothing: an empty markup
          // part would make every plain send multipart for no reader's gain.
          html_body: html.trim() === "" ? undefined : html,
          to: [recipient],
          consent_purpose: purpose,
          links: [{ entity_type: "person" as const, entity_id: personId }],
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    // The message left. Clearing it readies the composer for the next one and
    // is what stops the same words going out twice — send.isSuccess no longer
    // gates the button, because it stays true for the life of the mutation and
    // left a rep who sent one message unable to write another.
    onSuccess: clearMessage,
  });

  const sendable = allowed && canSend({ recipient, subject, body, purpose });

  return (
    <Modal
      open={open}
      onClose={onClose}
      labelledBy="person-composer-title"
      size="wide"
      placement="right"
    >
      <ComposerHead
        name={view.person.full_name}
        allowed={allowed}
        reason={email?.reason}
        purpose={purpose}
        guard={guard}
        onClose={onClose}
      />

      <div className="drawer-body">
        <label className="pe-field-label" htmlFor="composer-to">
          {t("person.composer.to")}
        </label>
        <TextInput id="composer-to" value={recipient} readOnly />

        <PurposePicker purpose={purpose} onChange={setPurpose} />

        <DraftBar
          intent={intent}
          onIntentChange={setIntent}
          pending={draft.isPending}
          error={draft.isError ? draft.error : null}
          onDraft={() => draft.mutate()}
        />

        <label className="pe-field-label" htmlFor="composer-subject">
          {t("person.composer.subject")}
        </label>
        <TextInput
          id="composer-subject"
          value={subject}
          onChange={(event) => setSubject(event.target.value)}
        />

        <label className="pe-field-label" htmlFor="composer-body">
          {t("person.composer.body")}
        </label>
        {/* Both renderings travel. The wire sends multipart/alternative, so a
            composer that kept only the markup would leave the plain part —
            which a text client, a screen reader and a spam filter all read —
            saying something else. */}
        <RichText
          id="composer-body"
          value={html}
          onChange={(next) => {
            setHtml(next.html);
            setBody(next.text);
          }}
          label={t("person.composer.body")}
          labels={{
            bold: t("richtext.bold"),
            italic: t("richtext.italic"),
            bulletList: t("richtext.bulletList"),
            numberList: t("richtext.numberList"),
            link: t("richtext.link"),
            linkPrompt: t("richtext.linkPrompt"),
          }}
          rows={12}
        />

        {/* Why this draft: the reasoning is a SIBLING of the body, never part
            of it. A body that explained itself is a body the rep has to edit
            before sending. */}
        {draft.data?.reasoning && draft.data.reasoning.length > 0 && (
          <section className="pe-why">
            {/* An h3, not the design system's SectionHeader: this block sits
                INSIDE a dialog whose own title is the h2, and a heading at the
                same level would read to a screen reader as a sibling of the
                dialog rather than a part of it. */}
            <h3 className="pe-section-title">{t("person.composer.why")}</h3>
            <ul className="pe-why-list">
              {draft.data.reasoning.map((reason) => (
                <li key={`${reason.kind}-${reason.label}`}>{reason.label}</li>
              ))}
            </ul>
          </section>
        )}

        <p className="pe-disclosure">
          <Sparkles size={13} aria-hidden="true" />
          {t("person.composer.aiDisclosure")}
        </p>
      </div>

      <div className="drawer-foot">
        {/* The human's own click IS the approval for their own send (ADR-0055),
            so this says what the button does rather than promising a second
            step that never comes. */}
        <span className="pe-confirm-note">
          <AlertTriangle size={14} aria-hidden="true" />
          {t("person.composer.sendNote")}
        </span>
        <SendFailure
          error={send.isError ? send.error : null}
          personId={personId}
        />
        <Button
          variant="primary"
          disabled={!sendable || send.isPending}
          onClick={() => send.mutate()}
        >
          <Send size={15} aria-hidden="true" />
          {sendPhase(send, t)}
        </Button>
      </div>
    </Modal>
  );
}

// --- The research drawer (State C) -----------------------------------------

export function PersonResearchDrawer({
  personId,
  personName,
  providerProfile,
  open,
  onClose,
}: Readonly<{
  personId: string;
  personName: string;
  // What a licensed provider was PAID to tell us about this person
  // (ADR-0101). Passed in rather than fetched here: the page already holds
  // the assembled 360, and a second read could disagree with what it shows.
  providerProfile?: components["schemas"]["PersonProviderProfile"];
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const [dismissed, setDismissed] = useState<ReadonlySet<number>>(new Set());

  const run = useQuery({
    enabled: open,
    queryKey: ["personResearch", personId],
    queryFn: async () => {
      const { data, error } = await api.POST("/people/{id}/research", {
        params: { path: { id: personId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const save = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST("/people/{id}/research/save", {
        params: { path: { id: personId } },
        body: { claims: [] },
      });
      if (error) {
        throwProblem(error);
      }
    },
  });

  const claims = (run.data?.claims ?? []).filter(
    (claim) => !dismissed.has(claim.ordinal),
  );

  return (
    <Modal
      open={open}
      onClose={onClose}
      labelledBy="person-research-title"
      size="wide"
      placement="right"
    >
      <div className="drawer-head">
        <div className="pe-drawer-title">
          <h2 id="person-research-title">
            {t("person.research.title", { name: personName })}
          </h2>
          <Button small onClick={onClose} aria-label={t("person.drawer.close")}>
            <X size={16} aria-hidden="true" />
          </Button>
        </div>
        <Badge>{t("person.research.publicOnly")}</Badge>
      </div>

      <div className="drawer-body">
        {/* What was BOUGHT sits above what a public read found: it cost
            money, it is the firmer of the two, and a rep looking somebody up
            should see it before a page crawl's guesses. */}
        <PersonProviderSection personId={personId} profile={providerProfile} />

        {run.isLoading && (
          <p className="pe-prose">{t("person.research.running")}</p>
        )}

        {/* The honest empty state. Nothing was asked and nothing was read, so
            the drawer says so rather than showing an empty result that reads
            as "a provider looked and found nothing". */}
        {run.data?.state === "not_connected" && (
          <p className="pe-prose">{t("person.research.notConnected")}</p>
        )}

        {run.data?.state === "ready" && (
          <>
            <p className="pe-staged-notice">
              {t("person.research.staged", { name: personName })}
            </p>
            <p className="pe-today-foot">
              {t("person.research.stats", {
                sources: run.data.sources_read ?? 0,
                claims: claims.length,
              })}
            </p>
            {claims.map((claim) => (
              <article className="pe-claim" key={claim.ordinal}>
                <span className="pe-claim-ordinal">{claim.ordinal}</span>
                <div>
                  <p className="pe-claim-body">{claim.body}</p>
                  <div className="pe-chiprow">
                    {/* A source URL comes from a THIRD-PARTY provider, so it
                        is untrusted: an unchecked href admits javascript: and
                        data: schemes, which execute on click. Only http(s)
                        becomes a link; anything else renders as inert text so
                        the reader still sees what was claimed, without a
                        clickable payload. */}
                    {claim.sources.map((source) =>
                      isWebUrl(source.url) ? (
                        <a
                          key={source.url}
                          className="pe-memory-channel"
                          href={source.url}
                          target="_blank"
                          rel="noreferrer"
                        >
                          {source.label}
                          <ExternalLink size={12} aria-hidden="true" />
                        </a>
                      ) : (
                        <span key={source.url} className="pe-memory-channel">
                          {source.label}
                        </span>
                      ),
                    )}
                    <Badge
                      tone={claim.confidence === "high" ? "success" : "warn"}
                    >
                      {claim.confidence}
                    </Badge>
                  </div>
                </div>
                <Button
                  small
                  onClick={() =>
                    setDismissed((prior) => new Set(prior).add(claim.ordinal))
                  }
                >
                  {t("person.research.dismiss")}
                </Button>
              </article>
            ))}
          </>
        )}
      </div>

      <div className="drawer-foot">
        <span className="pe-disclosure">
          {t("person.research.evidenceOrOmit")}
        </span>
        <div className="pe-drawer-actions">
          <Button onClick={onClose}>{t("person.research.discard")}</Button>
          <Button
            variant="primary"
            disabled={claims.length === 0 || save.isPending}
            onClick={() => save.mutate()}
          >
            {t("person.research.save", { count: claims.length })}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// --- The meeting brief -----------------------------------------------------

export function PersonMeetingBrief({
  activityId,
  open,
  onClose,
}: Readonly<{
  activityId: string | null;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const brief = useQuery({
    enabled: open && activityId != null,
    queryKey: ["meetingBrief", activityId],
    queryFn: async () => {
      const { data, error } = await api.GET("/activities/{id}/meeting-brief", {
        params: { path: { id: activityId ?? "" } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  return (
    <Modal
      open={open}
      onClose={onClose}
      labelledBy="person-meeting-title"
      size="wide"
      placement="right"
    >
      <div className="drawer-head">
        <div className="pe-drawer-title">
          <h2 id="person-meeting-title">{t("person.meeting.title")}</h2>
          <Button small onClick={onClose} aria-label={t("person.drawer.close")}>
            <X size={16} aria-hidden="true" />
          </Button>
        </div>
      </div>
      <div className="drawer-body">
        {brief.isLoading && (
          <p className="pe-prose">{t("person.meeting.loading")}</p>
        )}
        {brief.data?.sections.map((section) => (
          <section className="pe-brief-section" key={section.kind}>
            {/* h3 for the same reason as the composer's own section above: the
                dialog's title is the h2 these sit under. */}
            <h3 className="pe-section-title">
              {t(`person.meeting.${section.kind}` as never)}
            </h3>
            {section.sentences.map((sentence) => (
              <p className="pe-prose" key={sentence.text}>
                {sentence.text}
              </p>
            ))}
          </section>
        ))}
      </div>
      <div className="drawer-foot">
        <span className="pe-disclosure">
          {t("person.meeting.assembledNow")}
        </span>
        <Button onClick={onClose}>{t("person.drawer.close")}</Button>
      </div>
    </Modal>
  );
}
