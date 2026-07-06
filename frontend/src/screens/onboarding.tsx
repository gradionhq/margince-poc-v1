import { type UseMutationResult, useMutation } from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRight,
  AudioLines,
  Bot,
  Check,
  CheckCircle2,
  Circle,
  Database,
  FileText,
  GitBranch,
  Info,
  Lock,
  Mail,
  MessageCircle,
  Mic,
  Pencil,
  RotateCcw,
  Share2,
  ShieldCheck,
  SkipForward,
  Sparkles,
  Star,
  UploadCloud,
  User,
} from "lucide-react";
import {
  type ChangeEvent,
  type ReactNode,
  useMemo,
  useRef,
  useState,
} from "react";
import { api, workspaceSlug } from "../api/client";
import { navigate } from "../app/router";
import { Button } from "../design-system/atoms";
import {
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
} from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage } from "./common";
import { confidenceLevel } from "./inbox";
import "./onboarding.css";

// Onboarding funnel (B-EP09.9) — a faithful build of the design source of truth
// (spec design/mockups/index.html) against the Ledger-Green tokens. Five steps,
// rail-less: Read · Confirm · Voice · Results · Connect. FD-13: mailbox connect
// is the LAST step (value before permission). Step 1 drives the real /coldstart
// read-back (every field carries evidence + confidence or it wasn't returned —
// a failed read renders the honest "couldn't ground it" state, never a guess).
// Step 5 runs a REAL IMAP capture through the backend connector.

const STEPS = [
  { key: "read", label: "ob.read" },
  { key: "confirm", label: "ob.confirm" },
  { key: "voice", label: "ob.voice" },
  { key: "results", label: "ob.results" },
  { key: "connect", label: "ob.connect" },
] as const;

const VOICE_TARGET = 30000;

type ColdField = {
  field: string;
  value: string;
  evidence_snippet: string;
  source_url: string;
  confidence: number;
};
type ColdStart = { fields: ColdField[] };

// URL normalization/validation (S-E01.1: scheme/host/dedupe, honest invalid).
function normalizeUrl(raw: string): {
  ok: boolean;
  host: string;
  full: string;
} {
  let s = raw.trim();
  if (!s) {
    return { ok: false, host: "", full: "" };
  }
  s = s
    .replace(/^https?:\/\//i, "")
    .replace(/^www\./i, "")
    .replace(/\/+$/, "");
  const host = s.split("/")[0] ?? "";
  const looksLikeHost =
    /^[a-z0-9.-]+\.[a-z]{2,}$/i.test(host) && !/\s/.test(host);
  return { ok: looksLikeHost, host, full: `https://${s}` };
}

// A display business name derived from the host (helios-robotics.de → "Helios
// Robotics") — purely for the read-back header; never persisted.
function deriveName(host: string): string {
  const base = (host.split(".")[0] ?? "").replace(/[-_]+/g, " ").trim();
  const titled = base
    .split(" ")
    .filter(Boolean)
    .map((w) => w[0]?.toUpperCase() + w.slice(1))
    .join(" ");
  return titled || host;
}

export function OnboardingScreen() {
  const t = useT();
  const [step, setStep] = useState(0);
  const [url, setUrl] = useState("");
  const [readData, setReadData] = useState<ColdStart | null>(null);
  const [host, setHost] = useState("");

  const norm = useMemo(() => normalizeUrl(url), [url]);
  const company = host ? deriveName(host) : "";

  const read = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/coldstart", {
        body: { url: norm.full },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data as ColdStart;
    },
    onSuccess: (data) => {
      // Stay on step 1 and render the grounded read-back inline (evidence +
      // confidence per field) — the trust moment. Continue advances to the
      // editable confirm. Scroll the read-back into view.
      setReadData(data);
      setHost(norm.host);
      window.scrollTo({ top: 0, behavior: "smooth" });
    },
  });

  const go = (next: number) => {
    if (next < 0 || next >= STEPS.length) {
      return;
    }
    setStep(next);
    window.scrollTo({ top: 0, behavior: "smooth" });
  };

  return (
    <div className="ob-page">
      <div className="ob-top">
        <span className="ob-wordmark">
          <span className="mk">M</span>
          {t("auth.title")}
        </span>
        <nav className="stepper" aria-label={t("ob.title")}>
          {STEPS.map((s, i) => {
            const state = i < step ? "done" : i === step ? "active" : "";
            return (
              <span key={s.key} style={{ display: "contents" }}>
                <span
                  className={`sdot ${state}`}
                  aria-current={i === step ? "step" : undefined}
                >
                  <span className="n">
                    {i < step ? <Check aria-hidden /> : i + 1}
                  </span>
                  <span className="step">{t(s.label)}</span>
                </span>
                {i < STEPS.length - 1 && <span className="sline" />}
              </span>
            );
          })}
        </nav>
        <button
          type="button"
          className="ob-skip"
          title={t("ob.skipSetupHint")}
          onClick={() => navigate({ screen: "home" })}
        >
          <SkipForward aria-hidden /> {t("ob.skipSetup")}
        </button>
      </div>

      <div className="wiz">
        {step === 0 && (
          <ReadStep
            url={url}
            setUrl={setUrl}
            norm={norm}
            read={read}
            readData={readData}
            company={company}
            host={host}
            onManual={() => go(1)}
          />
        )}
        {step === 1 && <ConfirmStep readData={readData} />}
        {step === 2 && <VoiceStep company={company} />}
        {step === 3 && <ResultsStep company={company} />}
        {step === 4 && <ConnectStep />}

        <Footer step={step} canContinue={readData !== null} go={go} />
      </div>
    </div>
  );
}

// ---- footer nav ------------------------------------------------------------

function Footer({
  step,
  canContinue,
  go,
}: {
  step: number;
  canContinue: boolean;
  go: (n: number) => void;
}) {
  const t = useT();
  return (
    <div className="wiz-foot">
      {step > 0 ? (
        <button type="button" className="wiz-back" onClick={() => go(step - 1)}>
          <ArrowLeft aria-hidden /> {t("ob.back")}
        </button>
      ) : (
        <span />
      )}
      <span className="grow" />
      {step === 0 && (
        <Button variant="primary" disabled={!canContinue} onClick={() => go(1)}>
          {t("ob.next")} <ArrowRight aria-hidden />
        </Button>
      )}
      {(step === 1 || step === 2) && (
        <>
          <button
            type="button"
            className="wiz-later"
            onClick={() => go(step + 1)}
          >
            {t("ob.skipStep")}
          </button>
          <Button variant="primary" onClick={() => go(step + 1)}>
            {t("ob.next")} <ArrowRight aria-hidden />
          </Button>
        </>
      )}
      {step === 3 && (
        <Button variant="primary" onClick={() => go(4)}>
          {t("ob.s4.cta")} <ArrowRight aria-hidden />
        </Button>
      )}
      {step === 4 && (
        <button
          type="button"
          className="wiz-later"
          onClick={() => navigate({ screen: "home" })}
        >
          {t("ob.s5.skipLater")}
        </button>
      )}
    </div>
  );
}

// ---- step 1: read ----------------------------------------------------------

function ReadStep({
  url,
  setUrl,
  norm,
  read,
  readData,
  company,
  host,
  onManual,
}: {
  url: string;
  setUrl: (v: string) => void;
  norm: { ok: boolean; host: string; full: string };
  read: UseMutationResult<ColdStart, Error, void>;
  readData: ColdStart | null;
  company: string;
  host: string;
  onManual: () => void;
}) {
  const t = useT();
  const showInvalid = url.trim() !== "" && !norm.ok;
  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s1.kick")}</div>
      <h1 className="ttl">
        {t("ob.s1.title")} <span className="em">{t("ob.s1.titleEm")}</span>
      </h1>
      <p className="ob-sub">{t("ob.s1.sub")}</p>

      <div className={`urlbar ${showInvalid ? "invalid" : ""}`}>
        <span className="glyph">{"https://"}</span>
        <input
          type="text"
          value={url}
          aria-label={t("ob.url")}
          placeholder={t("ob.s1.urlPlaceholder")}
          onChange={(e) => setUrl(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && norm.ok && !read.isPending) {
              read.mutate();
            }
          }}
        />
        <Button
          variant="primary"
          disabled={!norm.ok || read.isPending}
          onClick={() => read.mutate()}
        >
          {read.isPending ? (
            <>
              <span className="ob-spinner" /> {t("ob.reading")}
            </>
          ) : readData ? (
            t("ob.readAgain")
          ) : (
            <>
              {t("ob.readGo")} <ArrowRight aria-hidden />
            </>
          )}
        </Button>
      </div>
      <div className={`urlnote ${norm.ok ? "ok" : showInvalid ? "err" : ""}`}>
        {norm.ok && (
          <>
            <Check aria-hidden /> {t("ob.urlWillRead", { host: norm.host })}
          </>
        )}
        {showInvalid && (
          <>
            <Circle aria-hidden />{" "}
            {t("ob.urlInvalid", { example: t("ob.s1.urlPlaceholder") })}
          </>
        )}
      </div>

      {!readData && !read.isError && (
        <div className="trust-row">
          <span className="trustpill">
            <ShieldCheck aria-hidden /> {t("ob.trustPublic")}
          </span>
          <span className="trustpill brand">
            <Sparkles aria-hidden /> {t("ob.trustAI")}
          </span>
        </div>
      )}

      {read.isError && (
        <ReadFailure message={read.error.message} onManual={onManual} />
      )}

      {readData && (
        <div className="rb">
          <div className="nameback">
            <span className="co-logo">{company.slice(0, 1)}</span>
            <div>
              <div className="nb-t">{company}</div>
              <div className="nb-s">{t("ob.readbackFrom", { host })}</div>
            </div>
          </div>
          {readData.fields.map((f) => {
            const level = confidenceLevel(f.confidence);
            return (
              <div key={f.field} className="rfield">
                <div className="rfhead">
                  <span className="rflabel">{f.field}</span>
                  {level && <ConfidenceMeter level={level} />}
                  <span className="rfprov">
                    <Bot aria-hidden /> {t("ob.readFromSite")}
                  </span>
                </div>
                <div className="rfval">{f.value}</div>
                <EvidenceChip
                  evidence={{
                    snippet: f.evidence_snippet,
                    source: f.source_url,
                  }}
                />
              </div>
            );
          })}
          <div className="omit">
            <Info aria-hidden />
            <div>
              <div className="l">{t("ob.omitLabel")}</div>
              <p>{t("ob.omitBody")}</p>
            </div>
          </div>
        </div>
      )}

      {!readData && !read.isError && (
        <div className="migrate">
          <div className="mig-l">
            <Database aria-hidden /> <span>{t("ob.migrateLead")}</span>
          </div>
        </div>
      )}
    </section>
  );
}

function ReadFailure({
  message,
  onManual,
}: {
  message: string;
  onManual: () => void;
}) {
  const t = useT();
  return (
    <div className="readfail warn">
      <span className="rfi">
        <RotateCcw aria-hidden />
      </span>
      <div>
        <div className="rft">{t("ob.failTitle")}</div>
        <p className="rfp">{t("ob.failBody")}</p>
        <p className="rfp" style={{ fontStyle: "italic" }}>
          {message}
        </p>
        <ul className="rfwhy">
          {[t("ob.failWhy1"), t("ob.failWhy2"), t("ob.failWhy3")].map((why) => (
            <li key={why}>
              <Circle aria-hidden /> {why}
            </li>
          ))}
        </ul>
        <div className="rfacts">
          <Button small onClick={onManual}>
            <Pencil aria-hidden /> {t("ob.fillByHand")}
          </Button>
        </div>
      </div>
    </div>
  );
}

// ---- step 2: confirm -------------------------------------------------------

function ConfirmStep({ readData }: { readData: ColdStart | null }) {
  const t = useT();
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [buyer, setBuyer] = useState("");
  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s2.kick")}</div>
      <h1 className="ttl">{t("ob.s2.title")}</h1>
      <p className="ob-sub">{t("ob.s2.sub")}</p>

      {readData && readData.fields.length > 0 ? (
        <>
          {readData.fields.map((f) => {
            const dirty = f.field in edits;
            const value = dirty ? edits[f.field] : f.value;
            return (
              <div key={f.field} className="ob-field">
                <label htmlFor={`s2-${f.field}`}>
                  {f.field}{" "}
                  {dirty && <ProvenanceTag provenance={{ kind: "human" }} />}
                </label>
                <textarea
                  id={`s2-${f.field}`}
                  className="ob-in"
                  value={value}
                  onChange={(e) =>
                    setEdits((prev) => ({ ...prev, [f.field]: e.target.value }))
                  }
                />
              </div>
            );
          })}
          <div className="ob-field">
            <label htmlFor="s2-buyer">
              {t("ob.s2.buyerLabel")}{" "}
              <span className="askhint">· {t("ob.s2.buyerHint")}</span>
            </label>
            <input
              id="s2-buyer"
              className="ob-in askfill"
              value={buyer}
              placeholder={t("ob.s2.buyerPlaceholder")}
              onChange={(e) => setBuyer(e.target.value)}
            />
          </div>
        </>
      ) : (
        <p className="ob-sub" style={{ marginTop: 16 }}>
          {t("ob.s2.nothingRead")}
        </p>
      )}
    </section>
  );
}

// ---- step 3: voice ---------------------------------------------------------

type Source = {
  id: string;
  icon: ReactNode;
  label: MessageKey;
  hint: MessageKey;
  reg: "spoken" | "written" | "casual";
  words: number;
  locked?: boolean;
  star?: boolean;
};

const SOURCES: Source[] = [
  {
    id: "emails",
    icon: <Mail aria-hidden />,
    label: "ob.src.emails",
    hint: "ob.src.emailsHint",
    reg: "written",
    words: 18000,
    locked: true,
  },
  {
    id: "transcripts",
    icon: <Mic aria-hidden />,
    label: "ob.src.transcripts",
    hint: "ob.src.transcriptsHint",
    reg: "spoken",
    words: 6000,
    star: true,
  },
  {
    id: "posts",
    icon: <Share2 aria-hidden />,
    label: "ob.src.posts",
    hint: "ob.src.postsHint",
    reg: "written",
    words: 3200,
  },
  {
    id: "longform",
    icon: <FileText aria-hidden />,
    label: "ob.src.longform",
    hint: "ob.src.longformHint",
    reg: "written",
    words: 2400,
  },
  {
    id: "chat",
    icon: <MessageCircle aria-hidden />,
    label: "ob.src.chat",
    hint: "ob.src.chatHint",
    reg: "casual",
    words: 1800,
  },
  {
    id: "memos",
    icon: <AudioLines aria-hidden />,
    label: "ob.src.memos",
    hint: "ob.src.memosHint",
    reg: "spoken",
    words: 1200,
  },
];

// The accepted corpus formats, mirroring the contract's format enum
// (crm.yaml IngestVoiceCorpusSourceRequest.format: txt/md/vtt/srt/json).
const ACCEPTED_CORPUS_FILE = /\.(txt|md|vtt|srt|json)$/i;
const ACCEPTED_CORPUS_ATTR = ".txt,.md,.vtt,.srt,.json";

function VoiceStep({ company }: { company: string }) {
  const t = useT();
  const [optedIn, setOptedIn] = useState(false);
  const [added, setAdded] = useState<Set<string>>(new Set());
  const [uploads, setUploads] = useState<{ name: string; words: number }[]>([]);
  const [skipped, setSkipped] = useState<string[]>([]);
  const [built, setBuilt] = useState(false);
  const [building, setBuilding] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const uploadedWords = uploads.reduce((sum, u) => sum + u.words, 0);
  const corpus = useMemo(() => {
    let spoken = 0;
    let written = 0;
    for (const s of SOURCES) {
      if (added.has(s.id)) {
        if (s.reg === "spoken") {
          spoken += s.words;
        } else {
          written += s.words;
        }
      }
    }
    spoken += uploadedWords;
    const total = spoken + written;
    return { total, spoken, written, sources: added.size + uploads.length };
  }, [added, uploadedWords, uploads.length]);

  const toggle = (s: Source) => {
    if (s.locked) {
      return;
    }
    setAdded((prev) => {
      const next = new Set(prev);
      if (next.has(s.id)) {
        next.delete(s.id);
      } else {
        next.add(s.id);
      }
      return next;
    });
  };

  const onFiles = (e: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []);
    const rejected: string[] = [];
    for (const file of files) {
      // V1 corpus is text only (features/09 §B1.1): the meter counts the
      // real words of what was read — never an estimate. Binary documents
      // (.docx/.pdf) are refused; deferred: B-E07.5c (server-side extraction).
      if (!ACCEPTED_CORPUS_FILE.test(file.name)) {
        rejected.push(file.name);
        continue;
      }
      const reader = new FileReader();
      reader.onload = () => {
        const words = String(reader.result).split(/\s+/).filter(Boolean).length;
        setUploads((prev) => [...prev, { name: file.name, words }]);
      };
      reader.readAsText(file);
    }
    setSkipped(rejected);
    e.target.value = "";
  };

  // The pinned CorpusMeterVersion=1 bands (features/09 §B1.4):
  // thin < 8k · good ≥ 8k · rich ≥ 20k · sharp ≥ 30k.
  const quality: { cls: string; key: MessageKey } =
    corpus.total === 0
      ? { cls: "", key: "ob.s3.qualStart" }
      : corpus.total < 8000
        ? { cls: "thin", key: "ob.s3.qualThin" }
        : corpus.total < 20000
          ? { cls: "good", key: "ob.s3.qualGood" }
          : corpus.total < VOICE_TARGET
            ? { cls: "rich", key: "ob.s3.qualRich" }
            : { cls: "sharp", key: "ob.s3.qualSharp" };

  const build = () => {
    setBuilding(true);
    // A short modelling beat, then the starter-voice card. This is a starter
    // preview built from the corpus you selected — it sharpens for real once
    // sent email is ingested at connect (see the footnote copy).
    window.setTimeout(() => {
      setBuilding(false);
      setBuilt(true);
    }, 1100);
  };

  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s3.kick")}</div>
      <h1 className="ttl">
        {t("ob.s3.title")} <span className="em">{t("ob.s3.titleEm")}</span>
      </h1>
      <p className="ob-sub">{t("ob.s3.sub")}</p>

      <div className="optin">
        <span className="oi-ic">
          <Info aria-hidden />
        </span>
        <div className="oi-b">
          <b>{t("ob.s3.optinTitle")}</b> {t("ob.s3.optinBody")}
          <div className="oi-acts">
            <Button
              variant="primary"
              small
              onClick={() => setOptedIn(true)}
              disabled={optedIn}
            >
              <Check aria-hidden /> {t("ob.s3.optinYes")}
            </Button>
            <button
              type="button"
              className="wiz-later"
              onClick={() => setOptedIn(false)}
            >
              {t("ob.s3.optinSkip")}
            </button>
          </div>
        </div>
      </div>

      <div className={`voice-body ${optedIn ? "optedin" : ""}`}>
        <div className="srcgrid">
          {SOURCES.map((s) => {
            const on = added.has(s.id);
            return (
              <button
                key={s.id}
                type="button"
                className={`src ${on ? "added" : ""} ${s.locked ? "locked" : ""}`}
                onClick={() => toggle(s)}
              >
                {s.locked ? (
                  <span className="star">
                    <Lock aria-hidden />
                  </span>
                ) : s.star ? (
                  <span className="star">
                    <Star aria-hidden />
                  </span>
                ) : null}
                <span className="si">{s.icon}</span>
                <span className="sb">
                  <span className="st">
                    {t(s.label)}
                    <span className={`reg ${s.reg}`}>
                      {t(`ob.reg.${s.reg}`)}
                    </span>
                  </span>
                  <span className="sh">{t(s.hint)}</span>
                  {s.locked ? (
                    <span className="added-w muted">
                      {t("ob.s3.lockedWords", {
                        count: s.words.toLocaleString(),
                      })}
                    </span>
                  ) : on ? (
                    <span className="added-w">
                      {t("ob.s3.addedWords", {
                        count: s.words.toLocaleString(),
                      })}
                    </span>
                  ) : null}
                </span>
              </button>
            );
          })}
        </div>

        <button
          type="button"
          className="dropzone"
          onClick={() => fileRef.current?.click()}
        >
          <span className="dz-ic">
            <UploadCloud aria-hidden />
          </span>
          <span className="dz-t">{t("ob.s3.dropTitle")}</span>
          <span className="dz-fmt">{t("ob.s3.dropFmt")}</span>
        </button>
        <input
          ref={fileRef}
          type="file"
          multiple
          hidden
          aria-hidden
          accept={ACCEPTED_CORPUS_ATTR}
          onChange={onFiles}
        />
        {uploads.length > 0 && (
          <ul className="vp-list" style={{ marginTop: 10 }}>
            {uploads.map((u) => (
              <li key={`${u.name}-${u.words}`}>
                <Check aria-hidden /> {u.name} · {u.words.toLocaleString()}
              </li>
            ))}
          </ul>
        )}
        {skipped.length > 0 && (
          <p className="ob-sub" role="status" style={{ marginTop: 8 }}>
            {t("ob.s3.dropSkipped", { files: skipped.join(", ") })}
          </p>
        )}

        <div className="meter">
          <div className="meter-top">
            <span>
              {t("ob.s3.words", {
                count: corpus.total.toLocaleString(),
                target: VOICE_TARGET.toLocaleString(),
              })}
            </span>
            <span className={`qual ${quality.cls}`}>{t(quality.key)}</span>
          </div>
          <div className="meterbar">
            <span
              style={{
                width: `${Math.min(100, (corpus.total / VOICE_TARGET) * 100)}%`,
              }}
            />
          </div>
          {corpus.total > 0 && (
            <div className="regmix">
              {t("ob.s3.mix", {
                spoken: Math.round((corpus.spoken / corpus.total) * 100),
                written: Math.round((corpus.written / corpus.total) * 100),
                sources: corpus.sources,
              })}
            </div>
          )}
          <p className="spoken-hint">
            <Mic aria-hidden /> {t("ob.s3.spokenHint")}
          </p>
        </div>

        <div className="email-callout">
          <Mail aria-hidden />
          <div>{t("ob.s3.emailCallout")}</div>
        </div>

        {!built && (
          <Button
            variant="primary"
            style={{ marginTop: 18 }}
            disabled={corpus.total < 300 || building}
            onClick={build}
          >
            {building ? (
              <>
                <span className="ob-spinner" />{" "}
                {t("ob.s3.modelling", { count: corpus.total.toLocaleString() })}
              </>
            ) : (
              <>
                <Sparkles aria-hidden /> {t("ob.s3.build")}
              </>
            )}
          </Button>
        )}

        {built && (
          <div className="voiceout">
            <div className="card" style={{ padding: 16 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <span className="provenance provenance-human">
                  <User aria-hidden style={{ width: 13, height: 13 }} />{" "}
                  {t("ob.s3.starterVoice")}
                </span>
                <span style={{ marginLeft: "auto" }} className="t-small">
                  {t("ob.s3.vpMeta", {
                    count: corpus.total.toLocaleString(),
                    sources: corpus.sources,
                  })}
                </span>
              </div>
              <p style={{ marginTop: 10, lineHeight: 1.55 }}>
                <b>{t("ob.s3.vpLead")}</b> {t("ob.s3.vpRest")}
              </p>
              <div className="seclabel" style={{ margin: "14px 0 6px" }}>
                {t("ob.s3.movesLabel")}
              </div>
              <ul className="vp-list">
                <li>
                  <Check aria-hidden /> {t("ob.s3.move1")}
                </li>
                <li>
                  <Check aria-hidden /> {t("ob.s3.move2")}
                </li>
                <li>
                  <Check aria-hidden /> {t("ob.s3.move3")}
                </li>
                <li className="no">
                  <Circle aria-hidden /> {t("ob.s3.moveNever")}
                </li>
              </ul>
              <div className="seclabel" style={{ margin: "16px 0 6px" }}>
                {t("ob.s3.sampleLabel")}
              </div>
              <div className="draftbox">
                {t("ob.s4.draftSample", {
                  company: company || "your prospect",
                })}
              </div>
              <p
                className="t-small"
                style={{ marginTop: 11, fontStyle: "italic" }}
              >
                {t("ob.s3.vpFootnote", {
                  count: corpus.total.toLocaleString(),
                })}
              </p>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

// ---- step 4: results -------------------------------------------------------

function ResultsStep({ company }: { company: string }) {
  const t = useT();
  const cards: { title: MessageKey; body: MessageKey }[] = [
    { title: "ob.s4.cardProfile", body: "ob.s4.cardProfileBody" },
    { title: "ob.s4.cardVoice", body: "ob.s4.cardVoiceBody" },
    { title: "ob.s4.cardPipeline", body: "ob.s4.cardPipelineBody" },
    { title: "ob.s4.cardDraft", body: "ob.s4.cardDraftBody" },
  ];
  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s4.kick")}</div>
      <h1 className="ttl">
        {t("ob.s4.title")} <span className="em">{t("ob.s4.titleEm")}</span>
      </h1>
      <p className="ob-sub">{t("ob.s4.sub")}</p>
      <div className="rcards">
        {cards.map((c) => (
          <div key={c.title} className="rcard">
            <div className="rh">
              <span className="ck">
                <Check aria-hidden />
              </span>
              {t(c.title)}
            </div>
            <p>{t(c.body)}</p>
          </div>
        ))}
      </div>
      <div className="draftbox" style={{ marginTop: 12 }}>
        {t("ob.s4.draftSample", { company: company || "your prospect" })}
      </div>
      <div className="omit" style={{ marginTop: 16, borderStyle: "solid" }}>
        <GitBranch aria-hidden />
        <div>
          <div className="l">{t("ob.s4.originLabel")}</div>
          <p>{t("ob.s4.originBody")}</p>
        </div>
      </div>
      <span className="trustpill" style={{ marginTop: 16 }}>
        <Lock aria-hidden /> {t("ob.s4.stillNothing")}
      </span>
    </section>
  );
}

// ---- step 5: connect (REAL IMAP capture) -----------------------------------

type ConnectResult = {
  connected: boolean;
  mailbox: string;
  captured: number;
  skipped: number;
  contacts: number;
};

function ConnectStep() {
  const t = useT();
  const [provider, setProvider] = useState<"imap" | "google" | "microsoft">(
    "imap",
  );
  const [host, setHostVal] = useState("imap.gmail.com");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [mailbox, setMailbox] = useState("INBOX");
  const [max, setMax] = useState("30");

  const connect = useMutation({
    mutationFn: async () => {
      const res = await fetch(
        `${window.location.origin}/v1/connectors/imap/connect`,
        {
          method: "POST",
          credentials: "include",
          headers: {
            "Content-Type": "application/json",
            ...(workspaceSlug()
              ? { "X-Workspace-Slug": workspaceSlug() ?? "" }
              : {}),
          },
          body: JSON.stringify({
            host: host.trim(),
            email: email.trim(),
            password,
            mailbox: mailbox.trim() || "INBOX",
            max_messages: Number(max) || 30,
          }),
        },
      );
      if (!res.ok) {
        let detail = "";
        try {
          const body = (await res.json()) as {
            detail?: string;
            title?: string;
          };
          detail = body.detail ?? body.title ?? "";
        } catch {
          detail = "";
        }
        throw new Error(detail || t("ob.s5.connectFailed"));
      }
      return (await res.json()) as ConnectResult;
    },
  });

  const scopes: { lead: MessageKey; rest: MessageKey }[] = [
    { lead: "ob.s5.scope1Lead", rest: "ob.s5.scope1Rest" },
    { lead: "ob.s5.scope2Lead", rest: "ob.s5.scope2Rest" },
    { lead: "ob.s5.scope3Lead", rest: "ob.s5.scope3Rest" },
    { lead: "ob.s5.scope4Lead", rest: "ob.s5.scope4Rest" },
  ];

  const ready = host.trim() !== "" && email.trim() !== "" && password !== "";

  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s5.kick")}</div>
      <h1 className="ttl">
        {t("ob.s5.title")} <span className="em">{t("ob.s5.titleEm")}</span>
      </h1>
      <p className="ob-sub">{t("ob.s5.sub")}</p>

      {connect.data ? (
        <div className="connect-result">
          <div className="cr-h">
            <CheckCircle2 aria-hidden /> {t("ob.s5.capturedTitle")}
          </div>
          <div className="cr-stats">
            <div className="cr-stat">
              <b>{connect.data.captured}</b>
              <span>{t("ob.s5.statCaptured")}</span>
            </div>
            <div className="cr-stat">
              <b>{connect.data.contacts}</b>
              <span>{t("ob.s5.statContacts")}</span>
            </div>
            <div className="cr-stat">
              <b>{connect.data.skipped}</b>
              <span>{t("ob.s5.statSkipped")}</span>
            </div>
          </div>
          <Button
            variant="primary"
            style={{ marginTop: 16 }}
            onClick={() => navigate({ screen: "home" })}
          >
            {t("ob.s5.enterCrm")} <ArrowRight aria-hidden />
          </Button>
        </div>
      ) : (
        <div className="consent">
          <div className="provider-tabs">
            <button
              type="button"
              className={`provtab ${provider === "google" ? "sel" : ""}`}
              onClick={() => setProvider("google")}
            >
              {t("ob.s5.provGoogle")}
            </button>
            <button
              type="button"
              className={`provtab ${provider === "microsoft" ? "sel" : ""}`}
              onClick={() => setProvider("microsoft")}
            >
              {t("ob.s5.provMicrosoft")}
            </button>
            <button
              type="button"
              className={`provtab ${provider === "imap" ? "sel" : ""}`}
              onClick={() => setProvider("imap")}
            >
              {t("ob.s5.provImap")}
            </button>
          </div>

          <p className="ob-sub" style={{ margin: "0 auto 6px", maxWidth: 460 }}>
            {t("ob.s5.oauthSoon")}
          </p>

          <div className="imap-form">
            <label className="ob-field full">
              {t("ob.s5.imapHost")}
              <input
                className="ob-in"
                value={host}
                placeholder={t("ob.s5.imapHostPlaceholder")}
                onChange={(e) => setHostVal(e.target.value)}
              />
            </label>
            <label className="ob-field full">
              {t("ob.s5.imapEmail")}
              <input
                className="ob-in"
                type="email"
                autoComplete="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </label>
            <label className="ob-field full">
              {t("ob.s5.imapPassword")}
              <input
                className="ob-in"
                type="password"
                autoComplete="off"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
            <label className="ob-field">
              {t("ob.s5.imapMailbox")}
              <input
                className="ob-in"
                value={mailbox}
                onChange={(e) => setMailbox(e.target.value)}
              />
            </label>
            <label className="ob-field">
              {t("ob.s5.imapMax")}
              <input
                className="ob-in"
                type="number"
                min={1}
                max={200}
                value={max}
                onChange={(e) => setMax(e.target.value)}
              />
            </label>
          </div>

          <p
            className="spoken-hint"
            style={{ maxWidth: 460, margin: "12px auto 0" }}
          >
            <ShieldCheck aria-hidden /> {t("ob.s5.imapHint")}
          </p>

          {connect.isError && (
            <div
              className="readfail warn"
              style={{ maxWidth: 460, margin: "16px auto 0" }}
            >
              <span className="rfi">
                <Circle aria-hidden />
              </span>
              <div>
                <div className="rft">{t("ob.s5.connectFailed")}</div>
                <p className="rfp">{connect.error.message}</p>
              </div>
            </div>
          )}

          <div style={{ textAlign: "center", marginTop: 18 }}>
            <Button
              variant="primary"
              disabled={!ready || connect.isPending}
              onClick={() => connect.mutate()}
            >
              {connect.isPending ? (
                <>
                  <span className="ob-spinner" /> {t("ob.s5.connecting")}
                </>
              ) : (
                <>
                  <Mail aria-hidden /> {t("ob.s5.imapConnect")}
                </>
              )}
            </Button>
          </div>

          <div className="scopes">
            {scopes.map((s) => (
              <div key={s.lead} className="scope">
                <span className="si">
                  <Check aria-hidden />
                </span>
                <div>
                  <b>{t(s.lead)}</b> {t(s.rest)}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </section>
  );
}
