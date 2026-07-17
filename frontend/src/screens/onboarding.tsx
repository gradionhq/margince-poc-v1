import {
  type UseMutationResult,
  useMutation,
  useQuery,
} from "@tanstack/react-query";
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
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Button } from "../design-system/atoms";
import {
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
} from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { coldFieldLabel, problemMessage } from "./common";
import { confidenceLevel } from "./inbox";
import "./onboarding.css";

// Onboarding funnel (B-EP09.9) — a faithful build of the design source of truth
// (spec design/mockups/index.html) against the Ledger-Green tokens. Four steps,
// rail-less: Company · Voice · Results · Connect. FD-13: mailbox connect is the
// LAST step (value before permission). Step 1 is the company form: it is always
// fully visible and hand-fillable, and the website read-back
// (POST /coldstart/preview — writes nothing, stages nothing) is an optional
// accelerant that PRE-FILLS it. Confirm-first (🟡) is honoured by the form
// itself: the unsaved form is the staged state, and Save is the human's
// confirmation. Step 4 runs a REAL IMAP capture through the backend connector.

const STEPS = [
  { key: "company", label: "ob.company" },
  { key: "voice", label: "ob.voice" },
  { key: "results", label: "ob.results" },
  { key: "connect", label: "ob.connect" },
] as const;

const VOICE_TARGET = 30000;

type CompanyProfile = components["schemas"]["CompanyProfile"];
type ColdField = components["schemas"]["ColdStartField"];
type ColdReadback = components["schemas"]["ColdStartReadback"];

// The company form, grouped as it reads: who the company IS, then how it sells.
// Identity fields are one-liners; positioning fields are prose (textareas).
// website is absent here because it renders as the read bar at the top of the
// group — one control for one value, not two.
const IDENTITY_FIELDS = [
  "display_name",
  "legal_name",
  "register_vat",
  "registered_address",
  "industry",
] as const;
const POSITIONING_FIELDS = [
  "icp",
  "value_proposition",
  "usp",
  "buying_center",
  "buying_intents",
  "history",
] as const;

type CompanyFieldName =
  | "website"
  | (typeof IDENTITY_FIELDS)[number]
  | (typeof POSITIONING_FIELDS)[number];
type CompanyForm = Record<CompanyFieldName, string>;

// The read-back can only ground the contract's ColdStartField names — website
// and display_name are always the human's to give.
type Grounded = Partial<Record<ColdField["field"], ColdField>>;

// One state object, because the three parts move together: typing a value
// drops its site grounding (the value is the human's now) and marks it typed.
type CompanyDraft = {
  values: CompanyForm;
  grounded: Grounded;
  edited: ReadonlySet<CompanyFieldName>;
};

const EMPTY_FORM: CompanyForm = {
  display_name: "",
  website: "",
  legal_name: "",
  register_vat: "",
  registered_address: "",
  industry: "",
  icp: "",
  value_proposition: "",
  usp: "",
  buying_center: "",
  buying_intents: "",
  history: "",
};

const EMPTY_DRAFT: CompanyDraft = {
  values: EMPTY_FORM,
  grounded: {},
  edited: new Set(),
};

function formFromProfile(p: CompanyProfile): CompanyForm {
  return {
    display_name: p.display_name,
    website: p.website ?? "",
    legal_name: p.legal_name ?? "",
    register_vat: p.register_vat ?? "",
    registered_address: p.registered_address ?? "",
    industry: p.industry ?? "",
    icp: p.icp ?? "",
    value_proposition: p.value_proposition ?? "",
    usp: p.usp ?? "",
    buying_center: p.buying_center ?? "",
    buying_intents: p.buying_intents ?? "",
    history: p.history ?? "",
  };
}

// useCompany reads the installation's own company, or null when it has not
// saved one yet: GET /company 404s until a human does, and that 404 IS the
// onboarding signal — there is no separate "onboarded" flag that could drift
// from the records it claims to describe. The app shell's gate and this form
// share it, so one cache entry answers both and they cannot disagree.
export function useCompany(enabled: boolean) {
  return useQuery({
    queryKey: ["company"],
    enabled,
    queryFn: async (): Promise<CompanyProfile | null> => {
      const { data, error, response } = await api.GET("/company");
      if (error) {
        if (response.status === 404) {
          return null;
        }
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

// Pre-fill FILLS — it never clobbers. A field the human already typed into
// keeps their text and its "typed by you" provenance; a field the read-back
// didn't ground stays empty for manual entry (the no-guess gate).
function prefill(
  draft: CompanyDraft,
  fields: readonly ColdField[],
): CompanyDraft {
  const values = { ...draft.values };
  const grounded: Grounded = { ...draft.grounded };
  for (const f of fields) {
    if (values[f.field].trim() !== "") {
      continue;
    }
    values[f.field] = f.value;
    grounded[f.field] = f;
  }
  return { values, grounded, edited: draft.edited };
}

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
  // NOSONAR: rewriting this host check to a linear pattern changes its accept/reject
  // set (dotted-label edge cases); input is a bounded hostname, so backtracking is not a risk.
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

function stepState(index: number, current: number): "done" | "active" | "" {
  if (index < current) {
    return "done";
  }
  if (index === current) {
    return "active";
  }
  return "";
}

// The pinned CorpusMeterVersion=1 bands (features/09 §B1.4):
// thin < 8k · good ≥ 8k · rich ≥ 20k · sharp ≥ 30k.
function corpusQuality(total: number): { cls: string; key: MessageKey } {
  if (total === 0) {
    return { cls: "", key: "ob.s2.qualStart" };
  }
  if (total < 8000) {
    return { cls: "thin", key: "ob.s2.qualThin" };
  }
  if (total < 20000) {
    return { cls: "good", key: "ob.s2.qualGood" };
  }
  if (total < VOICE_TARGET) {
    return { cls: "rich", key: "ob.s2.qualRich" };
  }
  return { cls: "sharp", key: "ob.s2.qualSharp" };
}

export function OnboardingScreen() {
  const t = useT();
  const [step, setStep] = useState(0);
  const [voiceBuilt, setVoiceBuilt] = useState(false);
  // Company-step state lives HERE, not in the step component: stepping back
  // and forward must not destroy what the user typed.
  const [draft, setDraft] = useState<CompanyDraft>(EMPTY_DRAFT);
  const [nameAttempted, setNameAttempted] = useState(false);
  const [companySaved, setCompanySaved] = useState(false);

  const norm = useMemo(
    () => normalizeUrl(draft.values.website),
    [draft.values.website],
  );
  // The name the later steps speak to: what the human called the company, or —
  // until they've typed one — what the website host suggests.
  const company =
    draft.values.display_name.trim() || (norm.ok ? deriveName(norm.host) : "");

  // A returning admin edits rather than retypes.
  const existing = useCompany(true);

  // Seed once: after the first paint of the loaded profile the form belongs to
  // the human, and a re-render must not overwrite their typing.
  const seeded = useRef(false);
  useEffect(() => {
    if (seeded.current || !existing.data) {
      return;
    }
    seeded.current = true;
    setDraft({
      values: formFromProfile(existing.data),
      grounded: {},
      edited: new Set(),
    });
    setCompanySaved(true);
  }, [existing.data]);

  const read = useMutation({
    mutationFn: async (): Promise<ColdReadback> => {
      const { data, error } = await api.POST("/coldstart/preview", {
        body: { url: norm.full },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      // Nothing was written or staged: the read-back only pre-fills the form
      // the human is already looking at. Each filled field keeps its evidence
      // + confidence until the human edits it.
      setDraft((prev) => prefill(prev, data.fields));
      globalThis.scrollTo({ top: 0, behavior: "smooth" });
    },
  });

  const go = (next: number) => {
    if (next < 0 || next >= STEPS.length) {
      return;
    }
    setStep(next);
    globalThis.scrollTo({ top: 0, behavior: "smooth" });
  };

  const setField = (field: CompanyFieldName, value: string) =>
    setDraft((prev) => {
      // Typing into a pre-filled field makes the value the human's assertion —
      // it stops claiming the site's snippet as its evidence.
      const grounded = { ...prev.grounded };
      if (field in grounded) {
        delete grounded[field as ColdField["field"]];
      }
      return {
        values: { ...prev.values, [field]: value },
        grounded,
        edited: new Set(prev.edited).add(field),
      };
    });

  // Save IS the confirmation: PUT /company writes the form the human read and
  // approved. Nothing was staged, so there is nothing to approve elsewhere.
  const save = useMutation({
    mutationFn: async (): Promise<CompanyProfile> => {
      const { data, error } = await api.PUT("/company", {
        body: {
          ...draft.values,
          display_name: draft.values.display_name.trim(),
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (profile) => {
      setCompanySaved(true);
      // The server owns the stored shape (a full URL is reduced to its bare
      // domain) — show what was actually saved, not what was typed.
      setDraft((prev) => ({ ...prev, values: formFromProfile(profile) }));
      go(1);
    },
  });

  const nameMissing = draft.values.display_name.trim() === "";
  const saveCompany = () => {
    setNameAttempted(true);
    if (nameMissing) {
      return;
    }
    save.mutate();
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
            const state = stepState(i, step);
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
          <CompanyStep
            draft={draft}
            setField={setField}
            norm={norm}
            read={read}
            saved={companySaved}
            saveError={save.isError ? save.error.message : null}
            nameError={nameAttempted && nameMissing}
          />
        )}
        {step === 1 && (
          <VoiceStep company={company} onBuilt={() => setVoiceBuilt(true)} />
        )}
        {step === 2 && (
          <ResultsStep
            company={company}
            voiceBuilt={voiceBuilt}
            profileSaved={companySaved}
          />
        )}
        {step === 3 && <ConnectStep />}

        <Footer
          step={step}
          go={go}
          onSaveCompany={saveCompany}
          savePending={save.isPending}
        />
      </div>
    </div>
  );
}

// ---- footer nav ------------------------------------------------------------

function Footer({
  step,
  go,
  onSaveCompany,
  savePending,
}: Readonly<{
  step: number;
  go: (n: number) => void;
  onSaveCompany: () => void;
  savePending: boolean;
}>) {
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
      {/* Continue on the company step SAVES it — there is no way past step 1
          with an unsaved company, and no way to save a nameless one. */}
      {step === 0 && (
        <Button
          variant="primary"
          disabled={savePending}
          onClick={onSaveCompany}
        >
          {savePending ? (
            <>
              <span className="ob-spinner" /> {t("ob.s1.saving")}
            </>
          ) : (
            <>
              {t("ob.next")} <ArrowRight aria-hidden />
            </>
          )}
        </Button>
      )}
      {step === 1 && (
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
      {step === 2 && (
        <Button variant="primary" onClick={() => go(3)}>
          {t("ob.s3.cta")} <ArrowRight aria-hidden />
        </Button>
      )}
    </div>
  );
}

// ---- step 1: company -------------------------------------------------------

function CompanyStep({
  draft,
  setField,
  norm,
  read,
  saved,
  saveError,
  nameError,
}: Readonly<{
  draft: CompanyDraft;
  setField: (field: CompanyFieldName, value: string) => void;
  norm: { ok: boolean; host: string; full: string };
  read: UseMutationResult<ColdReadback, Error, void>;
  saved: boolean;
  saveError: string | null;
  nameError: boolean;
}>) {
  const t = useT();
  const grounded = Object.keys(draft.grounded).length > 0;

  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s1.kick")}</div>
      <h1 className="ttl">
        {t("ob.s1.title")} <span className="em">{t("ob.s1.titleEm")}</span>
      </h1>
      <p className="ob-sub">{t("ob.s1.sub")}</p>

      <WebsiteReadBar
        website={draft.values.website}
        setWebsite={(v) => setField("website", v)}
        norm={norm}
        read={read}
        anyGrounded={grounded}
      />

      {!grounded && !read.isError && (
        <div className="trust-row">
          <span className="trustpill">
            <ShieldCheck aria-hidden /> {t("ob.trustPublic")}
          </span>
          <span className="trustpill brand">
            <Sparkles aria-hidden /> {t("ob.trustAI")}
          </span>
        </div>
      )}

      {!grounded && !read.isError && (
        <div className="migrate">
          <div className="mig-l">
            <Database aria-hidden /> <span>{t("ob.migrateLead")}</span>
          </div>
        </div>
      )}

      {read.isError && <ReadFailure message={read.error.message} />}

      {grounded && (
        <div className="omit">
          <Info aria-hidden />
          <div>
            <div className="l">{t("ob.s1.omitLabel")}</div>
            <p>{t("ob.s1.omitBody")}</p>
          </div>
        </div>
      )}

      {saved && (
        <p className="ob-sub" style={{ margin: "14px 0 0" }}>
          <CheckCircle2
            aria-hidden
            style={{ width: 14, height: 14, verticalAlign: "-2px" }}
          />{" "}
          {t("ob.s1.savedNote")}
        </p>
      )}

      {saveError && (
        <div className="readfail warn" style={{ marginTop: "var(--space-3)" }}>
          <span className="rfi">
            <Circle aria-hidden />
          </span>
          <div>
            <div className="rft">{t("ob.s1.saveFailed")}</div>
            <p className="rfp">{saveError}</p>
          </div>
        </div>
      )}

      <div className="seclabel" style={{ margin: "22px 0 0" }}>
        {t("ob.s1.identityLabel")}
      </div>
      {IDENTITY_FIELDS.map((field) => (
        <CompanyFormField
          key={field}
          field={field}
          value={draft.values[field]}
          grounded={groundingOf(draft, field)}
          edited={draft.edited.has(field)}
          error={
            field === "display_name" && nameError
              ? t("ob.s1.nameRequired")
              : null
          }
          onChange={(v) => setField(field, v)}
        />
      ))}

      <div className="seclabel" style={{ margin: "26px 0 0" }}>
        {t("ob.s1.positioningLabel")}
      </div>
      {POSITIONING_FIELDS.map((field) => (
        <CompanyFormField
          key={field}
          field={field}
          value={draft.values[field]}
          grounded={groundingOf(draft, field)}
          edited={draft.edited.has(field)}
          error={null}
          multiline
          onChange={(v) => setField(field, v)}
        />
      ))}
    </section>
  );
}

// The website field, with the optional read-back action on it: the company's
// website is a form value like any other — reading it is a shortcut into the
// form below, never a step of its own.
function WebsiteReadBar({
  website,
  setWebsite,
  norm,
  read,
  anyGrounded,
}: Readonly<{
  website: string;
  setWebsite: (v: string) => void;
  norm: { ok: boolean; host: string; full: string };
  read: UseMutationResult<ColdReadback, Error, void>;
  anyGrounded: boolean;
}>) {
  const t = useT();
  const showInvalid = website.trim() !== "" && !norm.ok;

  let readButtonLabel: ReactNode;
  if (read.isPending) {
    readButtonLabel = (
      <>
        <span className="ob-spinner" /> {t("ob.reading")}
      </>
    );
  } else if (anyGrounded) {
    readButtonLabel = t("ob.readAgain");
  } else {
    readButtonLabel = (
      <>
        {t("ob.readGo")} <ArrowRight aria-hidden />
      </>
    );
  }

  let urlNoteClass = "";
  if (norm.ok) {
    urlNoteClass = "ok";
  } else if (showInvalid) {
    urlNoteClass = "err";
  }

  return (
    <>
      <div className={`urlbar ${showInvalid ? "invalid" : ""}`}>
        <span className="glyph">{"https://"}</span>
        <input
          type="text"
          value={website}
          aria-label={t("ob.url")}
          placeholder={t("ob.s1.urlPlaceholder")}
          onChange={(e) => setWebsite(e.target.value)}
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
          {readButtonLabel}
        </Button>
      </div>
      <div className={`urlnote ${urlNoteClass}`}>
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
    </>
  );
}

// A field the read-back grounded and the human has not touched still carries
// the site's evidence; anything else is the human's own.
function groundingOf(
  draft: CompanyDraft,
  field: CompanyFieldName,
): ColdField | null {
  return draft.grounded[field as ColdField["field"]] ?? null;
}

function CompanyFormField({
  field,
  value,
  grounded,
  edited,
  error,
  multiline,
  onChange,
}: Readonly<{
  field: CompanyFieldName;
  value: string;
  grounded: ColdField | null;
  edited: boolean;
  error: string | null;
  multiline?: boolean;
  onChange: (v: string) => void;
}>) {
  const t = useT();
  const id = `co-${field}`;
  const level = grounded ? confidenceLevel(grounded.confidence) : null;
  return (
    <div className="ob-field">
      <label htmlFor={id}>
        {coldFieldLabel(field, t)} {level && <ConfidenceMeter level={level} />}
        {grounded && (
          <span className="rfprov">
            <Bot aria-hidden /> {t("ob.readFromSite")}
          </span>
        )}
        {edited && <ProvenanceTag provenance={{ kind: "human" }} />}
      </label>
      {multiline ? (
        <textarea
          id={id}
          className="ob-in"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : (
        <input
          id={id}
          className="ob-in"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {grounded && (
        <EvidenceChip
          evidence={{
            snippet: grounded.evidence_snippet,
            // source_url is carried only by url-sourced evidence; text and
            // self-description evidence names its origin instead of linking.
            source: grounded.source_url ?? t("ob.readFromSite"),
          }}
        />
      )}
      {error && (
        <div className="urlnote err">
          <Circle aria-hidden /> {error}
        </div>
      )}
    </div>
  );
}

function ReadFailure({ message }: Readonly<{ message: string }>) {
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
      </div>
    </div>
  );
}

// ---- step 2: voice ---------------------------------------------------------

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

function VoiceStep({
  company,
  onBuilt,
}: Readonly<{ company: string; onBuilt: () => void }>) {
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
      file.text().then((text) => {
        const words = text.split(/\s+/).filter(Boolean).length;
        setUploads((prev) => [...prev, { name: file.name, words }]);
      });
    }
    setSkipped(rejected);
    e.target.value = "";
  };

  const quality = corpusQuality(corpus.total);

  // The modelling beat must die with the step: a timer surviving unmount
  // would flip the parent's voiceBuilt after the user navigated away and
  // make step 4 claim a voice that was never built.
  const buildTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (buildTimer.current !== null) {
        globalThis.clearTimeout(buildTimer.current);
      }
    },
    [],
  );

  const build = () => {
    setBuilding(true);
    // A short modelling beat, then the starter-voice card. This is a starter
    // preview built from the corpus you selected — it sharpens for real once
    // sent email is ingested at connect (see the footnote copy).
    buildTimer.current = globalThis.setTimeout(() => {
      setBuilding(false);
      setBuilt(true);
      onBuilt();
    }, 1100);
  };

  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s2.kick")}</div>
      <h1 className="ttl">
        {t("ob.s2.title")} <span className="em">{t("ob.s2.titleEm")}</span>
      </h1>
      <p className="ob-sub">{t("ob.s2.sub")}</p>

      <div className="optin">
        <span className="oi-ic">
          <Info aria-hidden />
        </span>
        <div className="oi-b">
          <b>{t("ob.s2.optinTitle")}</b> {t("ob.s2.optinBody")}
          <div className="oi-acts">
            <Button
              variant="primary"
              small
              onClick={() => setOptedIn(true)}
              disabled={optedIn}
            >
              <Check aria-hidden /> {t("ob.s2.optinYes")}
            </Button>
            <button
              type="button"
              className="wiz-later"
              onClick={() => setOptedIn(false)}
            >
              {t("ob.s2.optinSkip")}
            </button>
          </div>
        </div>
      </div>

      <div className={`voice-body ${optedIn ? "optedin" : ""}`}>
        <div className="srcgrid">
          {SOURCES.map((s) => {
            const on = added.has(s.id);
            let mark: ReactNode = null;
            if (s.locked) {
              mark = (
                <span className="star">
                  <Lock aria-hidden />
                </span>
              );
            } else if (s.star) {
              mark = (
                <span className="star">
                  <Star aria-hidden />
                </span>
              );
            }
            let words: ReactNode = null;
            if (s.locked) {
              words = (
                <span className="added-w muted">
                  {t("ob.s2.lockedWords", { count: s.words.toLocaleString() })}
                </span>
              );
            } else if (on) {
              words = (
                <span className="added-w">
                  {t("ob.s2.addedWords", { count: s.words.toLocaleString() })}
                </span>
              );
            }
            return (
              <button
                key={s.id}
                type="button"
                className={`src ${on ? "added" : ""} ${s.locked ? "locked" : ""}`}
                onClick={() => toggle(s)}
              >
                {mark}
                <span className="si">{s.icon}</span>
                <span className="sb">
                  <span className="st">
                    {t(s.label)}
                    <span className={`reg ${s.reg}`}>
                      {t(`ob.reg.${s.reg}`)}
                    </span>
                  </span>
                  <span className="sh">{t(s.hint)}</span>
                  {words}
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
          <span className="dz-t">{t("ob.s2.dropTitle")}</span>
          <span className="dz-fmt">{t("ob.s2.dropFmt")}</span>
        </button>
        <input
          ref={fileRef}
          type="file"
          multiple
          hidden
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
          <output className="ob-sub" style={{ display: "block", marginTop: 8 }}>
            {t("ob.s2.dropSkipped", { files: skipped.join(", ") })}
          </output>
        )}

        <div className="meter">
          <div className="meter-top">
            <span>
              {t("ob.s2.words", {
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
              {t("ob.s2.mix", {
                spoken: Math.round((corpus.spoken / corpus.total) * 100),
                written: Math.round((corpus.written / corpus.total) * 100),
                sources: corpus.sources,
              })}
            </div>
          )}
          <p className="spoken-hint">
            <Mic aria-hidden /> {t("ob.s2.spokenHint")}
          </p>
        </div>

        <div className="email-callout">
          <Mail aria-hidden />
          <div>{t("ob.s2.emailCallout")}</div>
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
                {t("ob.s2.modelling", { count: corpus.total.toLocaleString() })}
              </>
            ) : (
              <>
                <Sparkles aria-hidden /> {t("ob.s2.build")}
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
                  {t("ob.s2.starterVoice")}
                </span>
                <span style={{ marginLeft: "auto" }} className="t-small">
                  {t("ob.s2.vpMeta", {
                    count: corpus.total.toLocaleString(),
                    sources: corpus.sources,
                  })}
                </span>
              </div>
              <p style={{ marginTop: 10, lineHeight: 1.55 }}>
                <b>{t("ob.s2.vpLead")}</b> {t("ob.s2.vpRest")}
              </p>
              <div className="seclabel" style={{ margin: "14px 0 6px" }}>
                {t("ob.s2.movesLabel")}
              </div>
              <ul className="vp-list">
                <li>
                  <Check aria-hidden /> {t("ob.s2.move1")}
                </li>
                <li>
                  <Check aria-hidden /> {t("ob.s2.move2")}
                </li>
                <li>
                  <Check aria-hidden /> {t("ob.s2.move3")}
                </li>
                <li className="no">
                  <Circle aria-hidden /> {t("ob.s2.moveNever")}
                </li>
              </ul>
              <div className="seclabel" style={{ margin: "16px 0 6px" }}>
                {t("ob.s2.sampleLabel")}
              </div>
              <div className="draftbox">
                {t("ob.s3.draftSample", {
                  company: company || "your prospect",
                })}
              </div>
              <p
                className="t-small"
                style={{ marginTop: 11, fontStyle: "italic" }}
              >
                {t("ob.s2.vpFootnote", {
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

// ---- step 3: results -------------------------------------------------------

function ResultsStep({
  company,
  voiceBuilt,
  profileSaved,
}: Readonly<{ company: string; voiceBuilt: boolean; profileSaved: boolean }>) {
  const t = useT();
  // The cards tell the truth about what the funnel actually did: a skipped
  // voice step gets the honest "starter voice" card, not a claim that drafts
  // already sound like the user — and a profile that was never confirmed is
  // named unsaved, not claimed as captured.
  const cards: { title: MessageKey; body: MessageKey }[] = [
    {
      title: "ob.s3.cardProfile",
      body: profileSaved
        ? "ob.s3.cardProfileBody"
        : "ob.s3.cardProfileSkippedBody",
    },
    {
      title: "ob.s3.cardVoice",
      body: voiceBuilt ? "ob.s3.cardVoiceBody" : "ob.s3.cardVoiceSkippedBody",
    },
    { title: "ob.s3.cardPipeline", body: "ob.s3.cardPipelineBody" },
    {
      title: voiceBuilt ? "ob.s3.cardDraft" : "ob.s3.cardDraftExample",
      body: "ob.s3.cardDraftBody",
    },
  ];
  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s3.kick")}</div>
      <h1 className="ttl">
        {t("ob.s3.title")} <span className="em">{t("ob.s3.titleEm")}</span>
      </h1>
      <p className="ob-sub">{t("ob.s3.sub")}</p>
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
        {t("ob.s3.draftSample", { company: company || "your prospect" })}
      </div>
      <p className="t-small" style={{ marginTop: 8, fontStyle: "italic" }}>
        {t("ob.s3.exampleTag")}
      </p>
      <div className="omit" style={{ marginTop: 16, borderStyle: "solid" }}>
        <GitBranch aria-hidden />
        <div>
          <div className="l">{t("ob.s3.originLabel")}</div>
          <p>{t("ob.s3.originBody")}</p>
        </div>
      </div>
      <span className="trustpill" style={{ marginTop: 16 }}>
        <Lock aria-hidden /> {t("ob.s3.stillNothing")}
      </span>
    </section>
  );
}

// ---- step 4: connect (REAL IMAP capture) -----------------------------------

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
        `${globalThis.location.origin}/v1/connectors/imap/connect`,
        {
          method: "POST",
          credentials: "include",
          headers: {
            "Content-Type": "application/json",
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
        throw new Error(detail || t("ob.s4.connectFailed"));
      }
      return (await res.json()) as ConnectResult;
    },
  });

  const scopes: { lead: MessageKey; rest: MessageKey }[] = [
    { lead: "ob.s4.scope1Lead", rest: "ob.s4.scope1Rest" },
    { lead: "ob.s4.scope2Lead", rest: "ob.s4.scope2Rest" },
    { lead: "ob.s4.scope3Lead", rest: "ob.s4.scope3Rest" },
    { lead: "ob.s4.scope4Lead", rest: "ob.s4.scope4Rest" },
  ];

  const ready = host.trim() !== "" && email.trim() !== "" && password !== "";

  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s4.kick")}</div>
      <h1 className="ttl">
        {t("ob.s4.title")} <span className="em">{t("ob.s4.titleEm")}</span>
      </h1>
      <p className="ob-sub">{t("ob.s4.sub")}</p>

      {connect.data ? (
        <div className="connect-result">
          <div className="cr-h">
            <CheckCircle2 aria-hidden /> {t("ob.s4.capturedTitle")}
          </div>
          <div className="cr-stats">
            <div className="cr-stat">
              <b>{connect.data.captured}</b>
              <span>{t("ob.s4.statCaptured")}</span>
            </div>
            <div className="cr-stat">
              <b>{connect.data.contacts}</b>
              <span>{t("ob.s4.statContacts")}</span>
            </div>
            <div className="cr-stat">
              <b>{connect.data.skipped}</b>
              <span>{t("ob.s4.statSkipped")}</span>
            </div>
          </div>
          <Button
            variant="primary"
            style={{ marginTop: 16 }}
            onClick={() => navigate({ screen: "home" })}
          >
            {t("ob.s4.enterCrm")} <ArrowRight aria-hidden />
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
              {t("ob.s4.provGoogle")}
            </button>
            <button
              type="button"
              className={`provtab ${provider === "microsoft" ? "sel" : ""}`}
              onClick={() => setProvider("microsoft")}
            >
              {t("ob.s4.provMicrosoft")}
            </button>
            <button
              type="button"
              className={`provtab ${provider === "imap" ? "sel" : ""}`}
              onClick={() => setProvider("imap")}
            >
              {t("ob.s4.provImap")}
            </button>
          </div>

          <p className="ob-sub" style={{ margin: "0 auto 6px", maxWidth: 460 }}>
            {t("ob.s4.oauthSoon")}
          </p>

          <div className="imap-form">
            <label className="ob-field full">
              {t("ob.s4.imapHost")}
              <input
                className="ob-in"
                value={host}
                placeholder={t("ob.s4.imapHostPlaceholder")}
                onChange={(e) => setHostVal(e.target.value)}
              />
            </label>
            <label className="ob-field full">
              {t("ob.s4.imapEmail")}
              <input
                className="ob-in"
                type="email"
                autoComplete="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </label>
            <label className="ob-field full">
              {t("ob.s4.imapPassword")}
              <input
                className="ob-in"
                type="password"
                autoComplete="off"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
            <label className="ob-field">
              {t("ob.s4.imapMailbox")}
              <input
                className="ob-in"
                value={mailbox}
                onChange={(e) => setMailbox(e.target.value)}
              />
            </label>
            <label className="ob-field">
              {t("ob.s4.imapMax")}
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
            <ShieldCheck aria-hidden /> {t("ob.s4.imapHint")}
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
                <div className="rft">{t("ob.s4.connectFailed")}</div>
                <p className="rfp">{connect.error.message}</p>
              </div>
            </div>
          )}

          <div className="connect-acts">
            <Button
              variant="primary"
              disabled={!ready || connect.isPending}
              onClick={() => connect.mutate()}
            >
              {connect.isPending ? (
                <>
                  <span className="ob-spinner" /> {t("ob.s4.connecting")}
                </>
              ) : (
                <>
                  <Mail aria-hidden /> {t("ob.s4.imapConnect")}
                </>
              )}
            </Button>
            <Button onClick={() => navigate({ screen: "home" })}>
              <SkipForward aria-hidden /> {t("ob.s4.skipLater")}
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
