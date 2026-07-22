import { useMutation, useQuery } from "@tanstack/react-query";
import {
  ArrowRight,
  Building2,
  Check,
  Circle,
  ExternalLink,
  FileSearch,
  Globe2,
  Info,
  PackageSearch,
  PenLine,
  Send,
  ShieldCheck,
  Sparkles,
  UsersRound,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { MarginceWorkbench } from "../design-system/margince-workbench";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { coldFieldLabel, problemMessage } from "./common";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type AssistantProfile = components["schemas"]["AssistantProfile"];
type MessageReply = components["schemas"]["CompanySiteReadMessageReply"];
type AiRunSummary = components["schemas"]["AiRunSummary"];
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
  manualContent?: ReactNode;
  onWebsiteChange: (value: string) => void;
  onChooseWebsite: () => void;
  onChooseManual: () => void;
  onStart: () => void;
  onContinue: () => void;
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

  if (props.mode === "website") {
    return (
      <WebsiteWorkbench {...props} running={running} terminal={terminal} />
    );
  }

  return (
    <section className="ob-panel ob-read-panel">
      <MarginceCoreScene
        state={presenceState(props, running)}
        progress={coreProgress(props.read)}
        className="ob-core-scene"
      >
        {props.mode === null && (
          <CoreIntroduction
            onWebsite={props.onChooseWebsite}
            onManual={props.onChooseManual}
          />
        )}
        {props.mode === "manual" && props.manualContent}
      </MarginceCoreScene>
    </section>
  );
}

type ConversationEntry =
  | { role: "user"; message: string; id: string }
  | { role: "assistant"; reply: MessageReply; id: string };

function configuredModelLabel(
  runtime: AiRunSummary | undefined,
  profile: AssistantProfile | undefined,
  unavailable: string,
) {
  const models = runtime?.models
    .map((model) => model.configured_model)
    .filter((model, index, all) => model && all.indexOf(model) === index);
  if (models?.length) return models.join(" + ");
  if (profile?.providers.length) return profile.providers.join(" + ");
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
  const [draft, setDraft] = useState("");
  const [entries, setEntries] = useState<ConversationEntry[]>([]);
  const [applied, setApplied] = useState<Set<string>>(new Set());
  const profile = useQuery({
    queryKey: ["assistant-profile"],
    queryFn: async (): Promise<AssistantProfile> => {
      const { data, error } = await api.GET("/assistant/profile");
      if (error) throw new Error(problemMessage(error));
      return data;
    },
    staleTime: Number.POSITIVE_INFINITY,
  });
  const latestReply = [...entries]
    .reverse()
    .find(
      (entry): entry is Extract<ConversationEntry, { role: "assistant" }> =>
        entry.role === "assistant",
    );
  const runtime = latestReply?.reply.ai_runtime ?? props.read?.ai_runtime;
  const configuredModels = configuredModelLabel(
    runtime,
    profile.data,
    t("ob.ai.runtimeUnavailable"),
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

  const send = useMutation({
    mutationFn: async ({
      message,
      history,
    }: {
      message: string;
      history: ConversationTurn[];
    }): Promise<MessageReply> => {
      if (!props.read) throw new Error(t("ob.ai.readFirst"));
      const { data, error } = await api.POST(
        "/company/site-reads/{readId}/messages",
        {
          params: { path: { readId: props.read.id } },
          body: { message, history },
        },
      );
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

  const submitMessage = () => {
    const message = draft.trim();
    if (message && !send.isPending) {
      send.mutate({ message, history: conversationHistory(entries) });
    }
  };
  const artifact = props.read ? (
    <div className="mw-review">
      <div className="mw-review-heading">
        <span>{t("ob.ai.liveArtifact")}</span>
        <h2>{t("ob.ai.companyKnowledge")}</h2>
        <p>{t("ob.ai.companyKnowledgeBody")}</p>
      </div>
      <ReadEvidence read={props.read} />
      {(props.terminal || props.read.profile_fields.length > 0) && (
        <div className="read-actions">
          <button
            type="button"
            className="wiz-later"
            onClick={props.onChooseManual}
          >
            {t("ob.continueManual")}
          </button>
          <Button
            variant="primary"
            disabled={props.read.profile_fields.length === 0}
            onClick={props.onContinue}
          >
            {t("ob.reviewFindings")} <ArrowRight aria-hidden />
          </Button>
        </div>
      )}
    </div>
  ) : undefined;

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
              read={props.read}
              error={props.error}
              presentation={presentation}
              refreshing={props.refreshing}
              locale={locale}
              onManual={props.onChooseManual}
            />
          </AssistantBubble>

          {!props.read && !props.error && (
            <WebsiteComposer {...props} running={props.running} />
          )}
          {entries.map((entry) =>
            entry.role === "user" ? (
              <div className="mw-message-user" key={entry.id}>
                {entry.message}
              </div>
            ) : (
              <AssistantBubble key={entry.id}>
                <p>{entry.reply.message}</p>
                {entry.reply.citations.length > 0 && (
                  <div className="mw-citations">
                    {keyedCitations(entry.reply.citations).map(
                      ({ citation, key }) => (
                        <a
                          key={`${entry.id}:${key}`}
                          href={citation.url}
                          target="_blank"
                          rel="noreferrer"
                        >
                          {citation.label} <ExternalLink aria-hidden />
                        </a>
                      ),
                    )}
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
                        props.onApplyChanges(entry.reply.proposed_changes);
                        setApplied((current) => new Set(current).add(entry.id));
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
          )}
          {send.isPending && (
            <AssistantBubble>
              <p className="mw-thinking">
                <span aria-hidden /> {t("ob.ai.thinking")}
              </p>
            </AssistantBubble>
          )}
          {send.isError && (
            <p className="mw-send-error" role="alert">
              {send.error.message}
            </p>
          )}
        </div>

        {props.read && (
          <div className="mw-composer">
            <textarea
              value={draft}
              maxLength={2000}
              rows={2}
              placeholder={t("ob.ai.askPlaceholder")}
              aria-label={t("ob.ai.askPlaceholder")}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (
                  event.key === "Enter" &&
                  !event.shiftKey &&
                  !event.nativeEvent.isComposing
                ) {
                  event.preventDefault();
                  submitMessage();
                }
              }}
            />
            <Button
              variant="primary"
              aria-label={t("ob.ai.send")}
              disabled={!draft.trim() || send.isPending}
              onClick={submitMessage}
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
  read,
  error,
  presentation,
  refreshing,
  locale,
  onManual,
}: Readonly<{
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

function CoreIntroduction({
  onWebsite,
  onManual,
}: Readonly<{ onWebsite: () => void; onManual: () => void }>) {
  const t = useT();
  return (
    <div className="ob-core-dialog">
      <div className="ob-core-kicker">{t("ob.readKick")}</div>
      <h1>{t("ob.coreIntroTitle")}</h1>
      <p>{t("ob.coreIntroBody")}</p>
      <CoreJourney active={0} />
      <div className="ob-core-choices">
        <button type="button" onClick={onWebsite}>
          <Globe2 aria-hidden />
          <span>
            <b>{t("ob.readWebsite")}</b>
            <small>{t("ob.readWebsiteSub")}</small>
          </span>
        </button>
        <button type="button" onClick={onManual}>
          <PenLine aria-hidden />
          <span>
            <b>{t("ob.readManual")}</b>
            <small>{t("ob.readManualSub")}</small>
          </span>
        </button>
      </div>
      <p className="ob-core-trust">
        <ShieldCheck aria-hidden />
        <span>
          <b>{t("ob.readTrustTitle")}</b>
          {t("ob.readTrustBody")}
        </span>
      </p>
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
        {t("ob.continueManual")}
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
