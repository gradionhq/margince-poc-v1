import {
  ArrowRight,
  Building2,
  Check,
  Circle,
  FileSearch,
  Globe2,
  Info,
  PackageSearch,
  PenLine,
  ShieldCheck,
  UsersRound,
} from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { coldFieldLabel } from "./common";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];

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
  const t = useT();
  const running =
    props.pending ||
    props.read?.status === "queued" ||
    props.read?.status === "deferred" ||
    props.read?.status === "reading";
  const terminal = props.read ? terminalStatuses.has(props.read.status) : false;

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
        {props.mode === "website" && !props.read && !props.error && (
          <WebsitePrompt {...props} running={running} />
        )}
        {props.mode === "website" && props.error && (
          <CoreFailure detail={props.error} onManual={props.onChooseManual} />
        )}
        {props.mode === "website" && props.read && (
          <CoreReadProgress
            read={props.read}
            refreshing={props.refreshing}
            onManual={props.onChooseManual}
          />
        )}
      </MarginceCoreScene>

      {props.read && <ReadEvidence read={props.read} />}

      {props.mode === "website" &&
        (terminal || (props.read?.profile_fields.length ?? 0) > 0) && (
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
              disabled={(props.read?.profile_fields.length ?? 0) === 0}
              onClick={props.onContinue}
            >
              {t("ob.reviewFindings")} <ArrowRight aria-hidden />
            </Button>
          </div>
        )}
    </section>
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

function WebsitePrompt(
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

function CoreFailure({
  detail,
  onManual,
}: Readonly<{ detail: string; onManual: () => void }>) {
  const t = useT();
  return (
    <div className="ob-core-dialog" role="alert">
      <div className="ob-core-kicker">{t("ob.readStatus.failed")}</div>
      <h1>{t("ob.failTitle")}</h1>
      <p>{t("ob.coreFailedBody")}</p>
      <p className="ob-core-detail">{detail}</p>
      <button type="button" className="ob-core-link" onClick={onManual}>
        {t("ob.continueManual")}
      </button>
    </div>
  );
}

function CoreReadProgress({
  read,
  refreshing,
  onManual,
}: Readonly<{
  read: CompanySiteRead;
  refreshing: boolean;
  onManual: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const fetchedPages = read.pages.filter((page) => page.status === "fetched");
  const legalEntities = read.legal_entities ?? [];
  const pagesRead = terminalStatuses.has(read.status)
    ? fetchedPages.length
    : (read.pages_read ?? fetchedPages.length);
  const findings = read.profile_fields.length + read.facts.length;
  const host = new URL(read.root_url).hostname;
  const phase = read.phase ?? (read.status === "queued" ? "crawling" : null);
  const latestPage = latestFetchedPage(read);
  const presentation = coreReadPresentation(read, phase, host, findings, t);

  return (
    <div className="ob-core-dialog" aria-live="polite">
      <div className="ob-core-kicker">
        {refreshing && read.status !== "deferred" && (
          <span className="ob-core-live" aria-hidden />
        )}
        {t(`ob.readStatus.${read.status}`)}
      </div>
      <h1>{presentation.title}</h1>
      <p>{presentation.body}</p>
      <CoreJourney active={presentation.journeyStage} />
      {latestPage && read.status === "reading" && (
        <div className="ob-core-activity">
          <FileSearch aria-hidden />
          <span>
            {t("ob.coreReadingPage")} <b>{readablePage(latestPage.url)}</b>
          </span>
        </div>
      )}
      {read.status === "deferred" && read.next_attempt_at && (
        <p className="ob-core-detail">
          {t("deepread.resumesAt", {
            when: formatDateTime(read.next_attempt_at, locale, "Europe/Berlin"),
          })}
        </p>
      )}
      <div className="ob-core-metrics">
        <span>
          <b>{pagesRead}</b> {t("ob.pagesRead")}
        </span>
        <span>
          <b>{legalEntities.length}</b> {t("ob.legalEntitiesFound")}
        </span>
        <span>
          <b>{read.profile_fields.length}</b> {t("ob.profileFindings")}
        </span>
        <span>
          <b>{read.facts.length}</b> {t("ob.usefulFacts")}
        </span>
      </div>
      {manualFallbackStatuses.has(read.status) && (
        <button type="button" className="ob-core-link" onClick={onManual}>
          {t("ob.readManual")}
        </button>
      )}
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
