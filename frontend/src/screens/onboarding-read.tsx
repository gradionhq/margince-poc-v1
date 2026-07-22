import { useMutation, useQuery } from "@tanstack/react-query";
import {
  Building2,
  Check,
  Circle,
  ExternalLink,
  FileSearch,
  Info,
  PackageSearch,
  Send,
  ShieldCheck,
  Sparkles,
  UsersRound,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import type { MarginceCoreState } from "../design-system/margince-core";
import { MarginceWorkbench } from "../design-system/margince-workbench";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { coldFieldLabel, problemMessage } from "./common";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type AiProfile = components["schemas"]["AiProfile"];
type OnboardingCompanyDraft = components["schemas"]["OnboardingCompanyDraft"];
type MessageReply = components["schemas"]["OnboardingCompanyMessageReply"];
type ConversationTurn =
  components["schemas"]["CompanySiteReadConversationTurn"];
type Translate = ReturnType<typeof useT>;
export type SuggestedCompanyChange =
  components["schemas"]["CompanySiteReadSuggestedChange"];

type ReadCompanyStepProps = Readonly<{
  mode: "website" | "manual" | null;
  website: string;
  norm: { ok: boolean; host: string; full: string };
  read: CompanySiteRead | null;
  pending: boolean;
  refreshing: boolean;
  error: string | null;
  companyDraft: OnboardingCompanyDraft;
  manualContent?: ReactNode;
  reviewContent?: ReactNode;
  confirmPending: boolean;
  confirmDisabled: boolean;
  onWebsiteChange: (value: string) => void;
  onChooseManual: () => void;
  onStart: () => void;
  onConfirm: () => void;
  onApplyChanges: (changes: SuggestedCompanyChange[]) => void;
}>;

const terminalStatuses = new Set<CompanySiteRead["status"]>([
  "ready",
  "partial",
  "failed",
  "confirmed",
  "abandoned",
]);
const successfulStatuses = new Set<CompanySiteRead["status"]>([
  "ready",
  "confirmed",
]);
const failedStatuses = new Set<CompanySiteRead["status"]>([
  "failed",
  "abandoned",
]);
const manualFallbackStatuses = new Set<CompanySiteRead["status"]>([
  "queued",
  "reading",
  "deferred",
  "failed",
  "abandoned",
]);
function presenceState(
  props: ReadCompanyStepProps,
  running: boolean,
): MarginceCoreState {
  if (
    props.mode === "website" &&
    (props.error || props.read?.status === "failed")
  ) {
    return "error";
  }
  if (running && props.read?.status !== "deferred") {
    return "working";
  }
  if (props.read?.status === "deferred") {
    return "quiet";
  }
  if (
    props.read?.status === "ready" ||
    props.read?.status === "partial" ||
    props.read?.status === "confirmed"
  ) {
    return "success";
  }
  return props.mode ? "listening" : "idle";
}

function coreProgress(read: CompanySiteRead | null): number | undefined {
  if (!read) {
    return undefined;
  }
  if (terminalStatuses.has(read.status)) {
    return 1;
  }
  if (read.phase === "extracting") {
    return 0.84;
  }
  return Math.max(0.08, Math.min(0.78, (read.pages_read ?? 0) / 40));
}

// The Core owns conversation and honest progress. Dense evidence stays directly
// below it, where quotes and controls remain readable instead of being squeezed
// into a decorative shape.
export function ReadCompanyStep(props: ReadCompanyStepProps) {
  const running =
    props.pending ||
    props.read?.status === "queued" ||
    props.read?.status === "deferred" ||
    props.read?.status === "reading";
  const terminal = props.read ? terminalStatuses.has(props.read.status) : false;

  return <WebsiteWorkbench {...props} running={running} terminal={terminal} />;
}

type ConversationEntry =
  | { role: "user"; message: string; id: string }
  | { role: "assistant"; reply: MessageReply; id: string };

const tierKeys = {
  local_small: "ob.ai.tier.localSmall",
  cheap_cloud: "ob.ai.tier.cheapCloud",
  premium: "ob.ai.tier.premium",
  local_large: "ob.ai.tier.localLarge",
} as const;

function configuredModelLabel(
  profile: AiProfile | undefined,
  unavailable: string,
  t: Translate,
) {
  const configured = profile?.configured_models
    ?.map(
      (binding) =>
        `${binding.provider}/${binding.model} · ${t(tierKeys[binding.tier])}`,
    )
    .filter((binding, index, all) => binding && all.indexOf(binding) === index);
  if (configured?.length) return configured.join(" + ");
  if (profile?.providers?.length) return profile.providers.join(" + ");
  return unavailable;
}

function workbenchStatus(props: ReadCompanyStepProps, t: Translate) {
  if (props.error) return t("ob.readStatus.failed");
  if (props.read) return t(`ob.readStatus.${props.read.status}`);
  return t("ob.ai.ready");
}

function WebsiteWorkbench(
  props: ReadCompanyStepProps &
    Readonly<{ running: boolean; terminal: boolean }>,
) {
  const t = useT();
  const { locale } = useLocale();
  const conversation = useCompanyConversation(
    props.mode,
    locale,
    t("ob.ai.readFirst"),
    props.companyDraft,
  );
  const [applied, setApplied] = useState<Set<string>>(new Set());
  const profile = useQuery({
    queryKey: ["ai-profile"],
    queryFn: async (): Promise<AiProfile> => {
      const { data, error } = await api.GET("/ai/profile");
      if (error) throw new Error(problemMessage(error));
      return data;
    },
    staleTime: Number.POSITIVE_INFINITY,
  });
  const latestReply = [...conversation.entries]
    .reverse()
    .find(
      (entry): entry is Extract<ConversationEntry, { role: "assistant" }> =>
        entry.role === "assistant",
    );
  // The dossier keeps accumulating calls while a website read runs. A chat
  // reply is only a point-in-time copy, so it must not freeze the live total.
  const readRuntime = props.read?.ai_runtime;
  const replyRuntime = latestReply?.reply.ai_runtime;
  const runtime =
    replyRuntime &&
    (!readRuntime || replyRuntime.call_attempts >= readRuntime.call_attempts)
      ? replyRuntime
      : readRuntime;
  const configuredModels = configuredModelLabel(
    profile.data,
    t("ob.ai.runtimeUnavailable"),
    t,
  );
  const state = presenceState(props, props.running);
  const presentation = props.read
    ? coreReadPresentation(
        props.read,
        props.read.phase ?? null,
        new URL(props.read.root_url).hostname,
        props.read.profile_fields.length + props.read.facts.length,
        t,
      )
    : null;

  const artifact = <CompanyArtifact {...props} />;

  return (
    <section className="ob-panel ob-read-panel ob-workbench-panel">
      <MarginceWorkbench
        state={state}
        progress={coreProgress(props.read)}
        eyebrow={t("ob.ai.identity")}
        title={t("ob.ai.role")}
        status={workbenchStatus(props, t)}
        configured={configuredModels}
        locale={locale}
        runtime={runtime}
        runtimeLabels={{
          configured: t("ob.ai.configured"),
          used: t("ob.ai.modelsUsed"),
          route: t("ob.ai.route"),
          calls: t("ob.ai.calls"),
          tokens: t("ob.ai.tokens"),
          latency: t("ob.ai.latency"),
          estimatedCost: t("ob.ai.estimatedCost"),
          partial: t("ob.ai.partialEstimate"),
          awaiting: t("ob.ai.awaitingModel"),
          unavailable: t("ob.ai.notAvailableYet"),
        }}
        artifact={artifact}
      >
        <div className="mw-thread" aria-live="polite">
          <AssistantBubble>
            <WebsiteStatusMessage
              mode={props.mode}
              read={props.read}
              error={props.error}
              presentation={presentation}
              refreshing={props.refreshing}
              locale={locale}
              onManual={props.onChooseManual}
            />
          </AssistantBubble>

          {!props.read &&
            (props.mode === "manual"
              ? props.manualContent
              : !props.error && (
                  <WebsiteComposer {...props} running={props.running} />
                ))}
          <ConversationEntries
            entries={conversation.entries}
            applied={applied}
            onApply={props.onApplyChanges}
            onApplied={(entryID) =>
              setApplied((current) => new Set(current).add(entryID))
            }
          />
          {conversation.send.isPending && (
            <AssistantBubble>
              <p className="mw-thinking">
                <span aria-hidden /> {t("ob.ai.thinking")}
              </p>
            </AssistantBubble>
          )}
          {conversation.send.isError && (
            <p className="mw-send-error" role="alert">
              {conversation.send.error.message}
            </p>
          )}
        </div>

        {props.mode && (
          <div className="mw-composer">
            <textarea
              value={conversation.draft}
              maxLength={2000}
              rows={2}
              placeholder={t("ob.ai.askPlaceholder")}
              aria-label={t("ob.ai.askPlaceholder")}
              onChange={(event) => conversation.setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (
                  event.key === "Enter" &&
                  !event.shiftKey &&
                  !event.nativeEvent.isComposing
                ) {
                  event.preventDefault();
                  conversation.submit();
                }
              }}
            />
            <Button
              variant="primary"
              aria-label={t("ob.ai.send")}
              disabled={
                !conversation.draft.trim() || conversation.send.isPending
              }
              onClick={conversation.submit}
            >
              <Send aria-hidden />
            </Button>
            <small>{t("ob.ai.reviewBoundary")}</small>
          </div>
        )}
      </MarginceWorkbench>
    </section>
  );
}

function CompanyArtifact(props: ReadCompanyStepProps) {
  const t = useT();
  if (!props.reviewContent) return undefined;
  return (
    <div className="mw-review">
      <div className="mw-review-heading">
        <span>{t("ob.ai.liveArtifact")}</span>
        <h2>{t("ob.ai.companyKnowledge")}</h2>
        <p>
          {t(
            props.mode === "manual"
              ? "ob.ai.companyKnowledgeManualBody"
              : "ob.ai.companyKnowledgeBody",
          )}
        </p>
      </div>
      {props.read && <ReadEvidence read={props.read} />}
      {props.reviewContent}
      <div className="mw-confirm-company">
        <p>{t("ob.ai.confirmBoundary")}</p>
        <Button
          variant="primary"
          disabled={props.confirmDisabled || props.confirmPending}
          onClick={props.onConfirm}
        >
          {props.confirmPending ? (
            <>
              <span className="ob-spinner" /> {t("ob.s1.saving")}
            </>
          ) : (
            <>
              <Check aria-hidden /> {t("ob.ai.confirmCompany")}
            </>
          )}
        </Button>
      </div>
    </div>
  );
}

function useCompanyConversation(
  mode: ReadCompanyStepProps["mode"],
  locale: "en" | "de",
  readFirstMessage: string,
  companyDraft: OnboardingCompanyDraft,
) {
  const [draft, setDraft] = useState("");
  const [entries, setEntries] = useState<ConversationEntry[]>([]);
  const send = useMutation({
    mutationFn: async ({
      message,
      history,
    }: {
      message: string;
      history: ConversationTurn[];
    }): Promise<MessageReply> => {
      if (!mode) throw new Error(readFirstMessage);
      const { data, error } = await api.POST("/onboarding/company/messages", {
        body: { message, history, locale, company_draft: companyDraft },
      });
      if (error) throw new Error(problemMessage(error));
      return data;
    },
    onMutate: ({ message }) => {
      setEntries((current) => [
        ...current,
        { role: "user", message, id: crypto.randomUUID() },
      ]);
      setDraft("");
    },
    onSuccess: (reply) => {
      setEntries((current) => [
        ...current,
        { role: "assistant", reply, id: crypto.randomUUID() },
      ]);
    },
  });
  const submit = () => {
    const message = draft.trim();
    if (message && !send.isPending) {
      send.mutate({ message, history: conversationHistory(entries) });
    }
  };
  return { draft, setDraft, entries, send, submit };
}

function ConversationEntries({
  entries,
  applied,
  onApply,
  onApplied,
}: Readonly<{
  entries: ReadonlyArray<ConversationEntry>;
  applied: ReadonlySet<string>;
  onApply: (changes: SuggestedCompanyChange[]) => void;
  onApplied: (entryID: string) => void;
}>) {
  const t = useT();
  return entries.map((entry) =>
    entry.role === "user" ? (
      <div className="mw-message-user" key={entry.id}>
        {entry.message}
      </div>
    ) : (
      <AssistantBubble key={entry.id}>
        <p>{entry.reply.message}</p>
        {entry.reply.citations.length > 0 && (
          <div className="mw-citations">
            {keyedCitations(entry.reply.citations).map(({ citation, key }) => (
              <a
                key={`${entry.id}:${key}`}
                href={citation.url}
                target="_blank"
                rel="noreferrer"
              >
                {citation.label} <ExternalLink aria-hidden />
              </a>
            ))}
          </div>
        )}
        {entry.reply.proposed_changes.length > 0 && (
          <div className="mw-proposal">
            <div>
              <Sparkles aria-hidden />
              <strong>{t("ob.ai.suggestedChanges")}</strong>
            </div>
            <ul>
              {keyedSuggestedChanges(entry.reply.proposed_changes).map(
                ({ change, key }) => (
                  <li key={`${entry.id}:${key}`}>
                    <span>{coldFieldLabel(change.field, t)}</span>
                    <strong>{change.value}</strong>
                    <small>{change.reason}</small>
                  </li>
                ),
              )}
            </ul>
            <Button
              small
              variant="primary"
              disabled={applied.has(entry.id)}
              onClick={() => {
                onApply(entry.reply.proposed_changes);
                onApplied(entry.id);
              }}
            >
              {applied.has(entry.id)
                ? t("ob.ai.applied")
                : t("ob.ai.applyChanges")}
            </Button>
          </div>
        )}
      </AssistantBubble>
    ),
  );
}

function keyedSuggestedChanges(changes: ReadonlyArray<SuggestedCompanyChange>) {
  const occurrences = new Map<string, number>();
  return changes.map((change) => {
    const identity = JSON.stringify([
      change.field,
      change.value,
      change.reason,
    ]);
    const occurrence = (occurrences.get(identity) ?? 0) + 1;
    occurrences.set(identity, occurrence);
    return { change, key: `${identity}:${occurrence}` };
  });
}

function keyedCitations(
  citations: ReadonlyArray<MessageReply["citations"][number]>,
) {
  const occurrences = new Map<string, number>();
  return citations.map((citation) => {
    const identity = JSON.stringify([citation.label, citation.url]);
    const occurrence = (occurrences.get(identity) ?? 0) + 1;
    occurrences.set(identity, occurrence);
    return { citation, key: `${identity}:${occurrence}` };
  });
}

function conversationHistory(
  entries: ReadonlyArray<ConversationEntry>,
): ConversationTurn[] {
  return entries
    .slice(-8)
    .map((entry) =>
      entry.role === "user"
        ? { role: "user", message: entry.message }
        : { role: "assistant", message: entry.reply.message },
    );
}

function AssistantBubble({ children }: Readonly<{ children: ReactNode }>) {
  const t = useT();
  return (
    <div className="mw-message-assistant">
      <span
        className="mw-speaker"
        role="img"
        aria-label={t("ob.ai.speakerName")}
      >
        <span aria-hidden>{t("ob.ai.speaker")}</span>
      </span>
      <div>{children}</div>
    </div>
  );
}

function WebsiteStatusMessage({
  mode,
  read,
  error,
  presentation,
  refreshing,
  locale,
  onManual,
}: Readonly<{
  mode: ReadCompanyStepProps["mode"];
  read: CompanySiteRead | null;
  error: string | null;
  presentation: ReturnType<typeof coreReadPresentation> | null;
  refreshing: boolean;
  locale: "de" | "en";
  onManual: () => void;
}>) {
  const t = useT();
  if (error) {
    return (
      <>
        <h2>{t("ob.failTitle")}</h2>
        <p>{t("ob.coreFailedBody")}</p>
        <p className="mw-error-detail">{error}</p>
        <button type="button" className="ob-core-link" onClick={onManual}>
          {t("ob.continueManual")}
        </button>
      </>
    );
  }
  if (!read || !presentation) {
    if (mode === "manual") {
      return (
        <>
          <h2>{t("ob.coreIntroTitle")}</h2>
          <p>{t("ob.coreIntroBody")}</p>
          <CoreJourney active={0} />
        </>
      );
    }
    return (
      <>
        <h2>{t("ob.coreWebsiteTitle")}</h2>
        <p>{t("ob.coreWebsiteBody")}</p>
        <CoreJourney active={0} />
      </>
    );
  }
  return (
    <>
      <h2>{presentation.title}</h2>
      <p>{presentation.body}</p>
      <ReadActivity read={read} refreshing={refreshing} />
      {read.status === "deferred" && read.next_attempt_at && (
        <p className="mw-resume">
          {t("deepread.resumesAt", {
            when: formatDateTime(read.next_attempt_at, locale, "Europe/Berlin"),
          })}
        </p>
      )}
      {manualFallbackStatuses.has(read.status) && (
        <button type="button" className="ob-core-link" onClick={onManual}>
          {t("ob.readManual")}
        </button>
      )}
      {successfulStatuses.has(read.status) && (
        <button type="button" className="ob-core-link" onClick={onManual}>
          {t("ob.continueManual")}
        </button>
      )}
    </>
  );
}

function ReadActivity({
  read,
  refreshing,
}: Readonly<{ read: CompanySiteRead; refreshing: boolean }>) {
  const t = useT();
  const latestPage = latestFetchedPage(read);
  const legalCount = read.legal_entities?.length ?? 0;
  const findingCount = read.profile_fields.length + read.facts.length;
  return (
    <div className="mw-read-activity">
      {latestPage && read.status === "reading" && (
        <p>
          {refreshing && <span className="ob-core-live" aria-hidden />}
          <FileSearch aria-hidden /> {t("ob.coreReadingPage")}{" "}
          <strong>{readablePage(latestPage.url)}</strong>
        </p>
      )}
      <div>
        <span>
          <b>{read.pages_read ?? 0}</b> {t("ob.pagesRead")}
        </span>
        <span>
          <b>{legalCount}</b> {t("ob.legalEntitiesFound")}
        </span>
        <span>
          <b>{findingCount}</b>{" "}
          {t(findingCount === 1 ? "ob.ai.finding" : "ob.ai.findings")}
        </span>
      </div>
    </div>
  );
}

function WebsiteComposer(
  props: ReadCompanyStepProps & Readonly<{ running: boolean }>,
) {
  const t = useT();
  return (
    <div className="ob-core-dialog">
      <div className="ob-core-kicker">{t("ob.coreLegalKicker")}</div>
      <h1>{t("ob.coreWebsiteTitle")}</h1>
      <p>{t("ob.coreWebsiteBody")}</p>
      <CoreJourney active={0} />
      <div
        className={`ob-core-url ${props.website && !props.norm.ok ? "invalid" : ""}`}
      >
        <span>{t("ob.urlScheme")}</span>
        <input
          value={props.website}
          aria-label={t("ob.url")}
          placeholder={t("ob.s1.urlPlaceholder")}
          disabled={props.running}
          onChange={(event) => props.onWebsiteChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && props.norm.ok && !props.running) {
              props.onStart();
            }
          }}
        />
      </div>
      {props.norm.ok && (
        <p className="ob-core-url-note">
          <Check aria-hidden /> {t("ob.urlWillRead", { host: props.norm.host })}
        </p>
      )}
      <Button
        variant="primary"
        disabled={!props.norm.ok || props.running}
        onClick={props.onStart}
      >
        <FileSearch aria-hidden /> {t("ob.readGo")}
      </Button>
      <button
        type="button"
        className="ob-core-link"
        onClick={props.onChooseManual}
      >
        {t("ob.readManual")}
      </button>
    </div>
  );
}

type Translator = ReturnType<typeof useT>;

function latestFetchedPage(read: CompanySiteRead) {
  return [...read.pages].reverse().find((page) => page.status === "fetched");
}

function coreReadPresentation(
  read: CompanySiteRead,
  phase: string | null,
  host: string,
  findings: number,
  t: Translator,
) {
  if (read.status === "deferred") {
    return {
      title: t("ob.coreDeferredBody"),
      body: read.status_detail ?? t("ob.coreDeferredBody"),
      journeyStage: 0,
    };
  }
  if (failedStatuses.has(read.status)) {
    return {
      title: t("ob.failTitle"),
      body: t("ob.coreFailedBody"),
      journeyStage: 0,
    };
  }
  if (successfulStatuses.has(read.status)) {
    return {
      title: t("ob.coreReady", { count: findings }),
      body: t("ob.coreReadyBody"),
      journeyStage: 3,
    };
  }
  if (read.status === "partial") {
    return {
      title: t("ob.corePartial", { count: findings }),
      body: t("ob.coreReadyBody"),
      journeyStage: 3,
    };
  }
  if (phase === "extracting") {
    return {
      title: t("ob.coreBusinessReading"),
      body: t("ob.coreBusinessReadingBody"),
      journeyStage: 1,
    };
  }
  return {
    title:
      phase === "crawling"
        ? t("ob.coreLegalReading", { host })
        : t("ob.corePreparing", { host }),
    body: t("ob.coreLegalReadingBody"),
    journeyStage: 0,
  };
}

function CoreJourney({ active }: Readonly<{ active: number }>) {
  const t = useT();
  const stages = [
    { icon: Building2, label: t("ob.corePathLegal") },
    { icon: PackageSearch, label: t("ob.corePathOffer") },
    { icon: UsersRound, label: t("ob.corePathCustomer") },
  ];
  return (
    <ol className="ob-core-journey" aria-label={t("ob.corePathLabel")}>
      {stages.map((stage, index) => {
        const Icon = stage.icon;
        const state =
          index < active ? "done" : index === active ? "active" : "waiting";
        return (
          <li key={stage.label} data-state={state}>
            <i>
              {state === "done" ? <Check aria-hidden /> : <Icon aria-hidden />}
            </i>
            {stage.label}
          </li>
        );
      })}
    </ol>
  );
}

function readablePage(rawURL: string) {
  const pageURL = new URL(rawURL);
  const path = pageURL.pathname.replace(/\/$/, "");
  return path === "" ? pageURL.hostname : `${pageURL.hostname}${path}`;
}

function ReadEvidence({ read }: Readonly<{ read: CompanySiteRead }>) {
  const t = useT();
  const legalEntities = read.legal_entities ?? [];
  const skippedPages = read.pages.filter((page) => page.status !== "fetched");
  if (
    legalEntities.length === 0 &&
    read.profile_fields.length === 0 &&
    read.facts.length === 0 &&
    skippedPages.length === 0 &&
    read.warnings.length === 0
  ) {
    return null;
  }
  return (
    <div className="core-findings">
      {legalEntities.length > 0 && (
        <section className="legal-preview">
          <h2>{t("ob.legalFoundTitle")}</h2>
          <p>{t("ob.legalFoundBody")}</p>
          <div className="legal-preview-grid">
            {legalEntities.map((entity) => (
              <article
                key={`${entity.name}:${entity.register_number ?? ""}:${entity.source_url}`}
                className="legal-preview-card"
              >
                <div className="finding-label">
                  <ShieldCheck aria-hidden /> {t("ob.legalEntity")}
                </div>
                <strong>{entity.name}</strong>
                {entity.registered_address && (
                  <span>{entity.registered_address}</span>
                )}
                {entity.register_number && (
                  <small>{entity.register_number}</small>
                )}
              </article>
            ))}
          </div>
        </section>
      )}
      {read.profile_fields.length > 0 && (
        <>
          <h2>{t("ob.coreFindingsTitle")}</h2>
          <p>{t("ob.coreFindingsBody")}</p>
          <div className="finding-grid">
            {read.profile_fields.map((field) => (
              <article key={field.field} className="finding-card">
                <div className="finding-label">
                  <Check aria-hidden /> {coldFieldLabel(field.field, t)}
                  <span>{Math.round(field.confidence * 100)}%</span>
                </div>
                <strong>{field.value}</strong>
                <blockquote>“{field.evidence_snippet}”</blockquote>
              </article>
            ))}
          </div>
        </>
      )}
      {read.facts.length > 0 && (
        <section className="live-fact-preview">
          <h2>{t("ob.factsTitle")}</h2>
          <div className="finding-grid">
            {read.facts.slice(0, 12).map((fact) => (
              <article
                key={`${fact.category}:${fact.field}:${fact.value_key}`}
                className="finding-card"
              >
                <div className="finding-label">
                  <Sparkles aria-hidden /> {coldFieldLabel(fact.field, t)}
                  <span>{Math.round(fact.confidence * 100)}%</span>
                </div>
                <strong>{fact.value}</strong>
                <blockquote>“{fact.evidence_snippet}”</blockquote>
              </article>
            ))}
          </div>
        </section>
      )}
      {(skippedPages.length > 0 || read.warnings.length > 0) && (
        <details className="read-coverage">
          <summary>
            <Info aria-hidden /> {t("ob.coverageDetails")}
          </summary>
          <ul>
            {skippedPages.map((page) => (
              <li key={page.url}>
                <Circle aria-hidden /> {page.url} — {page.reason ?? page.status}
              </li>
            ))}
            {read.warnings.map((warning) => (
              <li key={warning}>
                <Info aria-hidden /> {warning}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
