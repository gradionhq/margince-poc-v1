import {
  ArrowRight,
  Check,
  Circle,
  FileSearch,
  Globe2,
  Info,
  PenLine,
  RotateCcw,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
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

// The website arm deliberately renders every progressive server state in one
// place so the user never loses the manual escape or confirmation boundary.
// biome-ignore lint/complexity/noExcessiveCognitiveComplexity: explicit server-state branches are the trust surface
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
      <div className="read-hero-mark" aria-hidden>
        <Sparkles />
      </div>
      <div className="kick">{t("ob.readKick")}</div>
      <h1 className="ttl">{t("ob.readTitle")}</h1>
      <p className="ob-sub read-intro">{t("ob.readSub")}</p>

      <fieldset className="read-paths">
        <legend className="seclabel">{t("ob.readChoice")}</legend>
        <button
          type="button"
          className={`read-path ${props.mode === "website" ? "selected" : ""}`}
          aria-pressed={props.mode === "website"}
          onClick={props.onChooseWebsite}
        >
          <span className="read-path-icon website">
            <Globe2 aria-hidden />
          </span>
          <span>
            <b>{t("ob.readWebsite")}</b>
            <small>{t("ob.readWebsiteSub")}</small>
          </span>
          <ArrowRight aria-hidden />
        </button>
        <button
          type="button"
          className={`read-path ${props.mode === "manual" ? "selected" : ""}`}
          aria-pressed={props.mode === "manual"}
          onClick={props.onChooseManual}
        >
          <span className="read-path-icon manual">
            <PenLine aria-hidden />
          </span>
          <span>
            <b>{t("ob.readManual")}</b>
            <small>{t("ob.readManualSub")}</small>
          </span>
          <ArrowRight aria-hidden />
        </button>
      </fieldset>

      {props.mode === "website" && (
        <div className="read-workspace">
          <div
            className={`urlbar ${props.website && !props.norm.ok ? "invalid" : ""}`}
          >
            <span className="glyph">{t("ob.urlScheme")}</span>
            <input
              value={props.website}
              aria-label={t("ob.url")}
              placeholder={t("ob.s1.urlPlaceholder")}
              disabled={running}
              onChange={(event) => props.onWebsiteChange(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter" && props.norm.ok && !running) {
                  props.onStart();
                }
              }}
            />
            <Button
              variant="primary"
              disabled={!props.norm.ok || running}
              onClick={props.onStart}
            >
              {running ? (
                <>
                  <span className="ob-spinner" /> {t("ob.reading")}
                </>
              ) : props.read ? (
                <>
                  <RotateCcw aria-hidden /> {t("ob.readAgain")}
                </>
              ) : (
                <>
                  <FileSearch aria-hidden /> {t("ob.readGo")}
                </>
              )}
            </Button>
          </div>
          <div className={`urlnote ${props.norm.ok ? "ok" : ""}`}>
            {props.norm.ok && (
              <>
                <Check aria-hidden />{" "}
                {t("ob.urlWillRead", { host: props.norm.host })}
              </>
            )}
          </div>

          {props.error && (
            <div className="readfail warn" role="alert">
              <RotateCcw aria-hidden />
              <div>
                <div className="rft">{t("ob.failTitle")}</div>
                <p className="rfp">{props.error}</p>
                <button
                  type="button"
                  className="wiz-later"
                  onClick={props.onChooseManual}
                >
                  {t("ob.continueManual")}
                </button>
              </div>
            </div>
          )}

          {props.read && (
            <ReadProgress read={props.read} refreshing={props.refreshing} />
          )}

          {(terminal || (props.read?.profile_fields.length ?? 0) > 0) && (
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
        </div>
      )}

      {!props.mode && (
        <div className="read-trust">
          <ShieldCheck aria-hidden />
          <span>
            <b>{t("ob.readTrustTitle")}</b>
            {t("ob.readTrustBody")}
          </span>
        </div>
      )}
    </section>
  );
}

function ReadProgress({
  read,
  refreshing,
}: Readonly<{ read: CompanySiteRead; refreshing: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  const fetchedPages = read.pages.filter((page) => page.status === "fetched");
  const skippedPages = read.pages.filter((page) => page.status !== "fetched");
  const findings = read.profile_fields.length + read.facts.length;
  // The page LIST lands only with the finished read; pages_read is the
  // counter the worker advances as each page commits. Reading the list
  // while the read is still running shows a frozen 0 for the whole crawl,
  // so prefer the live counter until the terminal write supersedes it.
  const pagesRead = terminalStatuses.has(read.status)
    ? fetchedPages.length
    : (read.pages_read ?? fetchedPages.length);
  const activePhase =
    read.phase ?? (read.status === "queued" ? "crawling" : null);

  return (
    <div className="read-progress" aria-live="polite">
      <div className="read-progress-head">
        <div>
          <span className={`read-status ${read.status}`}>
            {t(`ob.readStatus.${read.status}`)}
          </span>
          <h2>
            {t("ob.readingHost", { host: new URL(read.root_url).hostname })}
          </h2>
        </div>
        {read.status !== "deferred" &&
          (refreshing ||
            read.status === "reading" ||
            read.status === "queued") && (
            <span className="reading-live">
              <span className="ob-spinner" /> {t("ob.live")}
            </span>
          )}
      </div>

      {read.status === "deferred" && (
        <p className="read-deferral">
          {read.status_detail}
          {read.next_attempt_at && (
            <>
              {" "}
              {t("deepread.resumesAt", {
                when: formatDateTime(
                  read.next_attempt_at,
                  locale,
                  "Europe/Berlin",
                ),
              })}
            </>
          )}
        </p>
      )}

      <div className="read-phases">
        <Phase
          label={t("ob.phaseDiscover")}
          done={read.pages.length > 0}
          active={activePhase === "crawling"}
        />
        <Phase
          label={t("ob.phaseExtract")}
          done={findings > 0}
          active={activePhase === "extracting"}
        />
        <Phase
          label={t("ob.phaseReady")}
          done={terminalStatuses.has(read.status)}
          active={false}
        />
      </div>

      <div className="read-metrics">
        <div>
          <b>{pagesRead}</b>
          <span>{t("ob.pagesRead")}</span>
        </div>
        <div>
          <b>{read.profile_fields.length}</b>
          <span>{t("ob.profileFindings")}</span>
        </div>
        <div>
          <b>{read.facts.length}</b>
          <span>{t("ob.usefulFacts")}</span>
        </div>
      </div>

      {read.profile_fields.length > 0 && (
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

function Phase({
  label,
  done,
  active,
}: Readonly<{ label: string; done: boolean; active: boolean }>) {
  return (
    <div
      className={`read-phase ${done ? "done" : ""} ${active ? "active" : ""}`}
    >
      <span>{done ? <Check aria-hidden /> : <Circle aria-hidden />}</span>
      <b>{label}</b>
    </div>
  );
}
