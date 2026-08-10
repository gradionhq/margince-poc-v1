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
import {
  Badge,
  Button,
  Modal,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { useT } from "../i18n";
import { throwProblem } from "./common";

// The three surfaces the person page opens over itself: the composer, the
// research drawer, and the meeting brief.
//
// All three are the WIDE drawer — a rep works in them rather than glancing at
// them — and all three leave the page behind visible, because the record is
// the context that makes the drawer's content mean anything.

type Person360 = components["schemas"]["Person360"];
type PersonConsentGuard = components["schemas"]["PersonConsentGuard"];

// --- The composer (State D) ------------------------------------------------

export function PersonComposer({
  personId,
  view,
  guard,
  open,
  onClose,
}: Readonly<{
  personId: string;
  view: Person360;
  guard: PersonConsentGuard | undefined;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [sent, setSent] = useState(false);

  // The draft is fetched when the drawer opens, not on page load: it spends
  // the workspace's model budget, and a rep who never opens the composer
  // should not pay for prose nobody reads.
  const draft = useQuery({
    enabled: open,
    queryKey: ["personDraft", personId],
    queryFn: async () => {
      const { data, error } = await api.POST("/people/{id}/draft-email", {
        params: { path: { id: personId } },
        body: {},
      });
      if (error) {
        throwProblem(error);
      }
      if (data) {
        setSubject(data.subject);
        setBody(data.body);
      }
      return data;
    },
  });

  const email = guard?.entries.find((entry) => entry.channel === "email");
  const allowed = email?.verdict === "allowed";
  const recipient = view.person.emails?.[0]?.email ?? "";

  return (
    <Modal
      open={open}
      onClose={onClose}
      labelledBy="person-composer-title"
      size="wide"
      placement="right"
    >
      <div className="drawer-head">
        <div className="pe-drawer-title">
          <h2 id="person-composer-title">
            {t("person.composer.title", { name: view.person.full_name })}
          </h2>
          <Button small onClick={onClose} aria-label={t("person.drawer.close")}>
            <X size={16} aria-hidden="true" />
          </Button>
        </div>
        {/* The consent verdict leads, with its reason. A rep about to write
            needs to know whether they may send BEFORE they spend words on it. */}
        <div
          className={
            allowed
              ? "pe-consent-line"
              : "pe-consent-line pe-consent-line-blocked"
          }
        >
          <Check size={15} aria-hidden="true" />
          <span>{email?.reason ?? t("person.composer.consentUnknown")}</span>
        </div>
      </div>

      <div className="drawer-body">
        <label className="pe-field-label" htmlFor="composer-to">
          {t("person.composer.to")}
        </label>
        <TextInput id="composer-to" value={recipient} readOnly />

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
        <Textarea
          id="composer-body"
          rows={12}
          value={draft.isLoading ? t("person.composer.drafting") : body}
          onChange={(event) => setBody(event.target.value)}
        />

        {/* Why this draft: the reasoning is a SIBLING of the body, never part
            of it. A body that explained itself is a body the rep has to edit
            before sending. */}
        {draft.data?.reasoning && draft.data.reasoning.length > 0 && (
          <section className="pe-why">
            <h3 className="pe-card-title">{t("person.composer.why")}</h3>
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
        {/* Confirm-first is stated, not implied. The rep is told what the
            button will do before they press it, because "Send" that does not
            send is the surprise this notice exists to prevent. */}
        <span className="pe-confirm-note">
          <AlertTriangle size={14} aria-hidden="true" />
          {t("person.composer.confirmFirst")}
        </span>
        <Button
          variant="primary"
          disabled={!allowed || sent}
          onClick={() => setSent(true)}
        >
          <Send size={15} aria-hidden="true" />
          {sent ? t("person.composer.staged") : t("person.composer.reviewSend")}
        </Button>
      </div>
    </Modal>
  );
}

// --- The research drawer (State C) -----------------------------------------

export function PersonResearchDrawer({
  personId,
  personName,
  open,
  onClose,
}: Readonly<{
  personId: string;
  personName: string;
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
                    {claim.sources.map((source) => (
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
                    ))}
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
            <h3 className="pe-card-title">
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
