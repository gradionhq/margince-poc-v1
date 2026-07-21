import {
  type UseMutationResult,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRight,
  AudioLines,
  Bot,
  Check,
  CheckCircle2,
  Circle,
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
import { navigate, useRoute } from "../app/router";
import { Button, TextInput } from "../design-system/atoms";
import {
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
} from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { Wordmark } from "./auth";
import { BackfillPanel } from "./backfill";
import { coldFieldLabel, problemMessage } from "./common";
import {
  ManualCompanySetup,
  useCompanyContextCapabilities,
} from "./company-context";
import { confidenceLevel } from "./inbox";
import { ReadCompanyStep } from "./onboarding-read";
import "./onboarding.css";

const STEPS = [
  { key: "read", label: "ob.read" },
  { key: "confirm", label: "ob.confirm" },
  { key: "voice", label: "ob.voice" },
  { key: "results", label: "ob.results" },
  { key: "connect", label: "ob.connect" },
] as const;

const VOICE_TARGET = 30000;

type CompanyProfile = components["schemas"]["CompanyProfile"];
type ColdField = components["schemas"]["ColdStartField"];
type CompanySiteReadLegalEntity =
  components["schemas"]["CompanySiteReadLegalEntity"];
type ColdReadback = components["schemas"]["ColdStartReadback"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type OnboardingState = components["schemas"]["OnboardingState"];
type PutOnboardingState = components["schemas"]["PutOnboardingStateRequest"];
type SourceMode = "website" | "manual";

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
  "offer_summary",
  "icp",
  "value_proposition",
  "usp",
  "customer_pains",
  "desired_outcomes",
  "buying_center",
  "buying_intents",
  "common_objections",
  "sales_motion",
  "history",
] as const;

type CompanyFieldName =
  | "website"
  | (typeof IDENTITY_FIELDS)[number]
  | (typeof POSITIONING_FIELDS)[number];
type CompanyForm = Record<CompanyFieldName, string>;

// The universal semantic minimum is enough to tell later product calls who the
// company is, what it sells, and to whom. Legal and registry details stay
// optional until a workflow with a real invoicing or jurisdictional need asks.
const REQUIRED_FIELDS = [
  "display_name",
  "offer_summary",
  "icp",
] as const satisfies readonly CompanyFieldName[];

// The read-back can only ground the contract's ColdStartField names —
// website is always the human's to give.
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
  offer_summary: "",
  icp: "",
  value_proposition: "",
  usp: "",
  customer_pains: "",
  desired_outcomes: "",
  buying_center: "",
  buying_intents: "",
  common_objections: "",
  sales_motion: "",
  history: "",
};

const EMPTY_DRAFT: CompanyDraft = {
  values: EMPTY_FORM,
  grounded: {},
  edited: new Set(),
};

function orEmpty(value: string | null | undefined): string {
  return value ?? "";
}

function formFromProfile(p: CompanyProfile): CompanyForm {
  return {
    display_name: p.display_name,
    website: orEmpty(p.website),
    legal_name: orEmpty(p.legal_name),
    register_vat: orEmpty(p.register_vat),
    registered_address: orEmpty(p.registered_address),
    industry: orEmpty(p.industry),
    offer_summary: orEmpty(p.offer_summary),
    icp: orEmpty(p.icp),
    value_proposition: orEmpty(p.value_proposition),
    usp: orEmpty(p.usp),
    customer_pains: orEmpty(p.customer_pains),
    desired_outcomes: orEmpty(p.desired_outcomes),
    buying_center: orEmpty(p.buying_center),
    buying_intents: orEmpty(p.buying_intents),
    common_objections: orEmpty(p.common_objections),
    sales_motion: orEmpty(p.sales_motion),
    history: orEmpty(p.history),
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

// Pre-fill never clobbers a HUMAN value, but each read replaces the MACHINE
// ones wholesale: a value the site grounded and nobody edited belongs to the
// previous read, and keeping it would leave site A's claims (and their
// evidence) standing after the human reads site B. So machine-owned fields are
// cleared first, then the new read fills what it can quote — a field the new
// site does not ground goes back to empty for manual entry (the no-guess
// gate), and a field the human typed or edited keeps their text throughout.
function prefill(
  draft: CompanyDraft,
  fields: readonly ColdField[],
): CompanyDraft {
  const values = { ...draft.values };
  const grounded: Grounded = { ...draft.grounded };
  // Everything still in `grounded` is machine-owned by construction — typing
  // into a field drops its grounding (setField) — so clearing the set is
  // exactly "forget the previous read".
  for (const field of Object.keys(grounded) as ColdField["field"][]) {
    values[field] = "";
    delete grounded[field];
  }
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

function stepState(index: number, current: number): "done" | "active" | "" {
  if (index < current) {
    return "done";
  }
  if (index === current) {
    return "active";
  }
  return "";
}

function optionalDraftValue(value: string): string | null {
  const trimmed = value.trim();
  return trimmed === "" ? null : value;
}

function onboardingDraftPayload(values: CompanyForm) {
  return {
    display_name: optionalDraftValue(values.display_name),
    offer_summary: optionalDraftValue(values.offer_summary),
    icp: optionalDraftValue(values.icp),
    value_proposition: optionalDraftValue(values.value_proposition),
    usp: optionalDraftValue(values.usp),
    customer_pains: optionalDraftValue(values.customer_pains),
    desired_outcomes: optionalDraftValue(values.desired_outcomes),
    buying_center: optionalDraftValue(values.buying_center),
    buying_intents: optionalDraftValue(values.buying_intents),
    common_objections: optionalDraftValue(values.common_objections),
    sales_motion: optionalDraftValue(values.sales_motion),
    legal_name: optionalDraftValue(values.legal_name),
    registered_address: optionalDraftValue(values.registered_address),
    register_vat: optionalDraftValue(values.register_vat),
    industry: optionalDraftValue(values.industry),
    history: optionalDraftValue(values.history),
  };
}

function formFromWizardState(state: OnboardingState): CompanyForm {
  return {
    ...EMPTY_FORM,
    ...Object.fromEntries(
      Object.entries(state.company_draft).map(([key, value]) => [
        key,
        value ?? "",
      ]),
    ),
    website: state.website_url ?? "",
  };
}

class WizardStateWriteError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

async function writeWizardState(body: PutOnboardingState) {
  const { data, error, response } = await api.PUT("/onboarding/state", {
    params: { header: { "Idempotency-Key": crypto.randomUUID() } },
    body,
  });
  if (error) {
    throw new WizardStateWriteError(response.status, problemMessage(error));
  }
  return data;
}

function wizardStateBody(input: {
  expectedVersion: number;
  nextStep: number;
  mode: SourceMode | null;
  readID: string | null;
  norm: { ok: boolean; full: string };
  values: CompanyForm;
  factKeys: string[];
  skippedVoice: boolean;
  skippedConnect: boolean;
}): PutOnboardingState {
  const websiteMode = input.mode === "website";
  return {
    expected_version: input.expectedVersion,
    step: STEPS[input.nextStep]?.key ?? "complete",
    source_mode: input.mode,
    website_url: websiteMode && input.norm.ok ? input.norm.full : null,
    site_read_id: websiteMode ? input.readID : null,
    company_draft: onboardingDraftPayload(input.values),
    selected_fact_keys: input.factKeys,
    voice_skipped: input.skippedVoice,
    connect_skipped: input.skippedConnect,
  };
}

function restoredWizardStep(
  state: OnboardingState,
  routeID: string | undefined,
): number | null {
  if (routeID === "connect") {
    return null;
  }
  const index = STEPS.findIndex((candidate) => candidate.key === state.step);
  return index >= 0 ? index : null;
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

// The coordinator mirrors the six server states directly; keeping the finite
// branches together makes Back/skip/OAuth transitions reviewable as one machine.
export function OnboardingScreen() {
  const capabilities = useCompanyContextCapabilities();
  if (capabilities.data && !capabilities.data.onboarding_enabled) {
    return <ManualCompanySetup />;
  }
  return <OnboardingCoordinator />;
}

// biome-ignore lint/complexity/noExcessiveCognitiveComplexity: splitting the state machine would hide cross-step invariants
function OnboardingCoordinator() {
  const t = useT();
  const queryClient = useQueryClient();
  const route = useRoute();
  const [step, setStep] = useState(route.id === "connect" ? 4 : 0);
  const connectOutcome =
    route.id === "connect" && route.id2 ? route.id2 : undefined;
  const [voiceBuilt, setVoiceBuilt] = useState(false);
  // Company-step state lives HERE, not in the step component: stepping back
  // and forward must not destroy what the user typed.
  const [draft, setDraft] = useState<CompanyDraft>(EMPTY_DRAFT);
  const [saveAttempted, setSaveAttempted] = useState(false);
  const [companySaved, setCompanySaved] = useState(false);
  const [sourceMode, setSourceMode] = useState<SourceMode | null>(null);
  const [siteReadID, setSiteReadID] = useState<string | null>(null);
  const [selectedFactKeys, setSelectedFactKeys] = useState<string[]>([]);
  const [voiceSkipped, setVoiceSkipped] = useState(false);
  const [connectSkipped, setConnectSkipped] = useState(false);
  const [stateConflict, setStateConflict] = useState<string | null>(null);

  const norm = useMemo(
    () => normalizeUrl(draft.values.website),
    [draft.values.website],
  );

  const existing = useCompany(true);
  const wizardState = useQuery({
    queryKey: ["onboarding-state"],
    queryFn: async (): Promise<OnboardingState | null> => {
      const { data, error, response } = await api.GET("/onboarding/state");
      if (error) {
        if (response.status === 404) {
          return null;
        }
        throw new Error(problemMessage(error));
      }
      if (
        !data.company_draft ||
        !Array.isArray(data.selected_fact_keys) ||
        typeof data.version !== "number"
      ) {
        return null;
      }
      return data;
    },
  });

  const stateVersion = useRef(0);
  const statePath = useRef<"creator" | "member">("creator");
  const persistQueue = useRef<Promise<boolean>>(Promise.resolve(true));
  const seeded = useRef(false);

  const persistState = (
    nextStep: number,
    overrides: Partial<{
      sourceMode: SourceMode | null;
      siteReadID: string | null;
      selectedFactKeys: string[];
      voiceSkipped: boolean;
      connectSkipped: boolean;
    }> = {},
  ) => {
    const mode = overrides.sourceMode ?? sourceMode;
    const readID = overrides.siteReadID ?? siteReadID;
    const factKeys = overrides.selectedFactKeys ?? selectedFactKeys;
    const skippedVoice = overrides.voiceSkipped ?? voiceSkipped;
    const skippedConnect = overrides.connectSkipped ?? connectSkipped;
    const values = draft.values;
    persistQueue.current = persistQueue.current.then(async () => {
      try {
        const data = await writeWizardState(
          wizardStateBody({
            expectedVersion: stateVersion.current,
            nextStep,
            mode,
            readID,
            norm,
            values,
            factKeys,
            skippedVoice,
            skippedConnect,
          }),
        );
        stateVersion.current = data.version;
        statePath.current = data.path;
        queryClient.setQueryData(["onboarding-state"], data);
        setStateConflict(null);
        return true;
      } catch (error) {
        if (error instanceof WizardStateWriteError && error.status === 409) {
          setStateConflict(t("ob.stateConflict"));
          seeded.current = false;
          await queryClient.invalidateQueries({
            queryKey: ["onboarding-state"],
          });
          return false;
        }
        setStateConflict(
          error instanceof Error ? error.message : t("ob.stateSaveFailed"),
        );
        return false;
      }
    });
    return persistQueue.current;
  };

  useEffect(() => {
    if (seeded.current || existing.isPending || wizardState.isPending) {
      return;
    }
    seeded.current = true;
    const saved = wizardState.data;
    if (saved) {
      stateVersion.current = saved.version;
      statePath.current = saved.path;
      setSourceMode(saved.source_mode ?? null);
      setSiteReadID(saved.site_read_id ?? null);
      setSelectedFactKeys(saved.selected_fact_keys);
      setVoiceSkipped(saved.voice_skipped);
      setConnectSkipped(saved.connect_skipped);
      setDraft({
        values: formFromWizardState(saved),
        grounded: {},
        edited: new Set(),
      });
      const restored = restoredWizardStep(saved, route.id);
      if (restored !== null) {
        setStep(restored);
      }
    } else if (existing.data) {
      statePath.current = "member";
      setDraft({
        values: formFromProfile(existing.data),
        grounded: {},
        edited: new Set(),
      });
      if (route.id !== "connect") {
        setStep(2);
      }
    }
    setCompanySaved(Boolean(existing.data));
  }, [
    existing.data,
    existing.isPending,
    route.id,
    wizardState.data,
    wizardState.isPending,
  ]);

  const startRead = useMutation({
    mutationFn: async (): Promise<CompanySiteRead> => {
      const { data, error } = await api.POST("/company/site-reads", {
        params: { header: { "Idempotency-Key": crypto.randomUUID() } },
        body: { url: norm.full },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      setSiteReadID(data.id);
      persistState(0, { sourceMode: "website", siteReadID: data.id });
    },
  });

  const siteRead = useQuery({
    queryKey: ["company-site-read", siteReadID],
    enabled: siteReadID !== null,
    queryFn: async (): Promise<CompanySiteRead> => {
      const { data, error } = await api.GET("/company/site-reads/{readId}", {
        params: { path: { readId: siteReadID ?? "" } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      if (status === "queued" || status === "reading") {
        return 800;
      }
      return status === "deferred" ? 60_000 : false;
    },
  });

  const appliedReadVersion = useRef(0);
  useEffect(() => {
    const read = siteRead.data;
    if (!read || read.draft_version <= appliedReadVersion.current) {
      return;
    }
    appliedReadVersion.current = read.draft_version;
    setDraft((prev) => prefill(prev, read.profile_fields));
    // A value key can name more than one fact — the same company can be
    // both a partner and a named customer — and the API takes a SET of
    // keys, so the selection folds the repeats rather than sending a
    // duplicate it would reject.
    setSelectedFactKeys([...new Set(read.facts.map((fact) => fact.value_key))]);
  }, [siteRead.data]);

  const go = (next: number, persist = true) => {
    if (next < 0 || next >= STEPS.length) {
      return;
    }
    if (persist) {
      persistState(next);
    }
    setStep(next);
    globalThis.scrollTo({ top: 0, behavior: "smooth" });
  };

  // Choosing a legal entity fills the three fields from the block the legal
  // notice printed — the whole point of offering the choice. They are marked
  // as YOUR input, not as read-from-site, because that is what the server
  // records: the multi-entity abstention strips the legal trio from the
  // read's own fields, and confirmation trusts evidence only from those. A
  // label claiming provenance the database does not hold would be worse than
  // no label at all. A field the notice left blank is cleared rather than
  // left holding the previous entity's value.
  const setLegalEntity = (entity: CompanySiteReadLegalEntity) =>
    setDraft((prev) => {
      const grounded = { ...prev.grounded };
      const edited = new Set(prev.edited);
      const values = { ...prev.values };
      const applied: Array<[ColdField["field"], string]> = [
        ["legal_name", entity.name],
        ["registered_address", entity.registered_address ?? ""],
        ["register_vat", entity.register_number ?? ""],
      ];
      for (const [field, value] of applied) {
        values[field] = value;
        delete grounded[field];
        edited.add(field);
      }
      return { values, grounded, edited };
    });

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

  const save = useMutation({
    mutationFn: async (): Promise<CompanyProfile> => {
      const profile = {
        ...draft.values,
        display_name: draft.values.display_name.trim(),
        offer_summary: draft.values.offer_summary.trim(),
        icp: draft.values.icp.trim(),
        legal_name: draft.values.legal_name.trim(),
        registered_address: draft.values.registered_address.trim(),
        register_vat: draft.values.register_vat.trim(),
        industry: draft.values.industry.trim(),
      };
      const readyRead =
        sourceMode === "website" &&
        siteRead.data &&
        (siteRead.data.status === "ready" ||
          siteRead.data.status === "partial");
      const result = readyRead
        ? await api.POST("/company/site-reads/{readId}/confirm", {
            params: {
              path: { readId: siteRead.data.id },
              header: { "Idempotency-Key": crypto.randomUUID() },
            },
            body: {
              draft_version: siteRead.data.draft_version,
              proposal_hash: siteRead.data.proposal_hash,
              profile,
              selected_fact_keys: selectedFactKeys,
              resolutions: [],
            },
          })
        : await api.PUT("/company", { body: profile });
      const { data, error } = result;
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (profile) => {
      setCompanySaved(true);
      // The shell's onboarding gate reads the same ["company"] cache entry;
      // stamp the save into it or the gate still sees "undescribed" and
      // bounces the freshly saved workspace back here on the next navigation.
      queryClient.setQueryData(["company"], profile);
      // The server owns the stored shape (a full URL is reduced to its bare
      // domain) — show what was actually saved, not what was typed.
      setDraft((prev) => ({ ...prev, values: formFromProfile(profile) }));
      go(2);
    },
  });

  // The company step is mandatory: every required field must carry a value
  // before Continue will save, and there is no way past it that does not.
  const missingRequired = REQUIRED_FIELDS.filter(
    (field) => draft.values[field].trim() === "",
  );
  const saveCompany = () => {
    setSaveAttempted(true);
    if (missingRequired.length > 0) {
      return;
    }
    save.mutate();
  };

  const memberPath = statePath.current === "member";
  const visibleSteps = memberPath
    ? STEPS.filter(
        (candidate) => candidate.key === "voice" || candidate.key === "connect",
      )
    : STEPS;
  const finishOnboarding = async (skipped: boolean) => {
    setConnectSkipped(skipped);
    const persisted = await persistState(STEPS.length, {
      connectSkipped: skipped,
    });
    if (!persisted) {
      return;
    }
    navigate({ screen: "home" });
  };

  return (
    <div className="ob-page">
      <div className="ob-top">
        <Wordmark alt={t("auth.title")} className="ob-wordmark" />
        <nav className="stepper" aria-label={t("ob.title")}>
          {visibleSteps.map((s, i) => {
            const actualIndex = STEPS.findIndex(
              (candidate) => candidate.key === s.key,
            );
            const state = stepState(actualIndex, step);
            return (
              <span key={s.key} style={{ display: "contents" }}>
                <span
                  className={`sdot ${state}`}
                  aria-current={actualIndex === step ? "step" : undefined}
                >
                  <span className="n">
                    {actualIndex < step ? <Check aria-hidden /> : i + 1}
                  </span>
                  <span className="step">{t(s.label)}</span>
                </span>
                {i < visibleSteps.length - 1 && <span className="sline" />}
              </span>
            );
          })}
        </nav>
      </div>

      <div className="wiz">
        {(wizardState.isPending || existing.isPending) && (
          <div className="ob-state-loading" role="status">
            <span className="ob-spinner" /> {t("ob.restoring")}
          </div>
        )}
        {stateConflict && (
          <div className="readfail warn" role="alert">
            <Info aria-hidden /> <p>{stateConflict}</p>
          </div>
        )}
        {step === 0 && (
          <ReadCompanyStep
            mode={sourceMode}
            website={draft.values.website}
            norm={norm}
            read={siteRead.data ?? startRead.data ?? null}
            pending={startRead.isPending}
            refreshing={siteRead.isFetching}
            error={
              startRead.isError
                ? startRead.error.message
                : siteRead.isError
                  ? siteRead.error.message
                  : null
            }
            onWebsiteChange={(value) => setField("website", value)}
            onChooseWebsite={() => {
              setSourceMode("website");
              persistState(0, { sourceMode: "website" });
            }}
            onChooseManual={() => {
              setSourceMode("manual");
              persistState(1, { sourceMode: "manual", siteReadID: null });
              go(1, false);
            }}
            onStart={() => startRead.mutate()}
            onContinue={() => {
              persistState(1, { sourceMode: "website", selectedFactKeys });
              go(1, false);
            }}
          />
        )}
        {step === 1 && (
          <CompanyStep
            draft={draft}
            setField={setField}
            saved={companySaved}
            saveError={save.isError ? save.error.message : null}
            missingRequired={saveAttempted ? missingRequired : []}
            read={siteRead.data ?? null}
            onPickEntity={setLegalEntity}
            selectedFactKeys={selectedFactKeys}
            setSelectedFactKeys={(keys) => {
              setSelectedFactKeys(keys);
              persistState(1, { selectedFactKeys: keys });
            }}
            onFieldBlur={() => persistState(1)}
          />
        )}
        {step === 2 && <VoiceStep onBuilt={() => setVoiceBuilt(true)} />}
        {step === 3 && (
          <ResultsStep
            voiceBuilt={voiceBuilt}
            profileSaved={companySaved}
            profile={existing.data ?? undefined}
          />
        )}
        {step === 4 && (
          <ConnectStep outcome={connectOutcome} onComplete={finishOnboarding} />
        )}

        <Footer
          step={step}
          go={go}
          onSaveCompany={saveCompany}
          savePending={save.isPending}
          memberPath={memberPath}
          onSkipVoice={() => {
            setVoiceSkipped(true);
            const next = memberPath ? 4 : 3;
            persistState(next, { voiceSkipped: true });
            go(next, false);
          }}
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
  memberPath,
  onSkipVoice,
}: Readonly<{
  step: number;
  go: (n: number, persist?: boolean) => void;
  onSaveCompany: () => void;
  savePending: boolean;
  memberPath: boolean;
  onSkipVoice: () => void;
}>) {
  const t = useT();
  let backTarget: number | null = step - 1;
  if (memberPath && step === 2) {
    backTarget = null;
  } else if (memberPath && step === 4) {
    backTarget = 2;
  }
  return (
    <div className="wiz-foot">
      {backTarget !== null && backTarget >= 0 ? (
        <button
          type="button"
          className="wiz-back"
          onClick={() => go(backTarget)}
        >
          <ArrowLeft aria-hidden /> {t("ob.back")}
        </button>
      ) : (
        <span />
      )}
      <span className="grow" />
      {step === 1 && (
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
      {step === 2 && (
        <>
          <button type="button" className="wiz-later" onClick={onSkipVoice}>
            {t("ob.skipStep")}
          </button>
          <Button variant="primary" onClick={() => go(memberPath ? 4 : 3)}>
            {t("ob.next")} <ArrowRight aria-hidden />
          </Button>
        </>
      )}
      {step === 3 && (
        <Button variant="primary" onClick={() => go(4)}>
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
  read,
  saved,
  saveError,
  missingRequired,
  selectedFactKeys,
  setSelectedFactKeys,
  onPickEntity,
  onFieldBlur,
}: Readonly<{
  draft: CompanyDraft;
  setField: (field: CompanyFieldName, value: string) => void;
  onPickEntity: (entity: CompanySiteReadLegalEntity) => void;
  read: CompanySiteRead | null;
  saved: boolean;
  saveError: string | null;
  missingRequired: readonly CompanyFieldName[];
  selectedFactKeys: readonly string[];
  setSelectedFactKeys: (keys: string[]) => void;
  onFieldBlur: () => void;
}>) {
  const t = useT();

  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s1.kick")}</div>
      <h1 className="ttl">{t("ob.s1.title")}</h1>
      <p className="ob-sub">{t("ob.s1.sub")}</p>

      <div className="confirm-origin">
        <ShieldCheck aria-hidden />
        <span>
          {read
            ? t("ob.confirmWebsite", {
                count: read.pages_read ?? read.pages.length,
              })
            : t("ob.confirmManual")}
        </span>
      </div>

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

      {missingRequired.length > 0 && (
        <div className="urlnote err" style={{ marginTop: "var(--space-3)" }}>
          <Circle aria-hidden />{" "}
          {t("ob.s1.requiredMissing", {
            fields: missingRequired
              .map((field) => coldFieldLabel(field, t))
              .join(", "),
          })}
        </div>
      )}

      {/* One .form-stack carries the whole form at the house 8/12 rhythm; the
          two groups are separated by labeled dividers (the create-form
          pattern), not by per-field margins. */}
      <div className="form-stack ob-companyform">
        <p className="form-divider t-label">{t("ob.s1.identityLabel")}</p>
        <LegalEntityChoice read={read} draft={draft} onPick={onPickEntity} />
        {IDENTITY_FIELDS.map((field) => (
          <CompanyFormField
            key={field}
            field={field}
            value={draft.values[field]}
            grounded={groundingOf(draft, field)}
            edited={draft.edited.has(field)}
            required={isRequired(field)}
            error={
              missingRequired.includes(field) ? t("ob.s1.fieldRequired") : null
            }
            onChange={(v) => setField(field, v)}
            onBlur={onFieldBlur}
          />
        ))}

        <p className="form-divider t-label">{t("ob.s1.positioningLabel")}</p>
        {POSITIONING_FIELDS.map((field) => (
          <CompanyFormField
            key={field}
            field={field}
            value={draft.values[field]}
            grounded={groundingOf(draft, field)}
            edited={draft.edited.has(field)}
            required={isRequired(field)}
            error={
              missingRequired.includes(field) ? t("ob.s1.fieldRequired") : null
            }
            multiline
            onChange={(v) => setField(field, v)}
            onBlur={onFieldBlur}
          />
        ))}
      </div>

      {read && read.facts.length > 0 && (
        <details className="confirm-facts">
          {/* Collapsed by default: a hundred evidence cards between the form
              and the Continue button turns a review into a scroll. The
              summary states what is selected, which is the only thing a
              human needs unless they want to change it. */}
          <summary>
            <span className="seclabel">{t("ob.factsTitle")}</span>
            <span className="facts-count">
              {t("ob.factsSelected", {
                selected: selectedFactKeys.length,
                total: read.facts.length,
              })}
            </span>
          </summary>
          <p className="ob-sub">{t("ob.factsSub")}</p>
          <div className="fact-grid">
            {read.facts.map((fact) => {
              const selected = selectedFactKeys.includes(fact.value_key);
              return (
                <button
                  key={`${fact.field}:${fact.value_key}`}
                  type="button"
                  className={`fact-card ${selected ? "selected" : ""}`}
                  aria-pressed={selected}
                  onClick={() =>
                    setSelectedFactKeys(
                      selected
                        ? selectedFactKeys.filter(
                            (key) => key !== fact.value_key,
                          )
                        : [...selectedFactKeys, fact.value_key],
                    )
                  }
                >
                  <span className="fact-check">
                    {selected ? <Check aria-hidden /> : <Circle aria-hidden />}
                  </span>
                  <span>
                    <b>{coldFieldLabel(fact.field, t)}</b>
                    <span>{fact.value}</span>
                    <small>{fact.evidence_snippet}</small>
                  </span>
                </button>
              );
            })}
          </div>
        </details>
      )}
    </section>
  );
}

// The website field, with the optional read-back action on it: the company's
// website is a form value like any other — reading it is a shortcut into the
// form below, never a step of its own.
// The legal-entity choice. A group's imprint states one block per company
// — registered name, address, register number — and the read refuses to
// guess which of them the installation belongs to, because picking wrong
// writes another company's legal identity into this one's CRM. So it
// offers what it read and the human answers in one click, instead of
// retyping five lines the page already printed.
//
// One entity needs no question: the read already filled the fields.
function LegalEntityChoice({
  read,
  draft,
  onPick,
}: Readonly<{
  read: CompanySiteRead | null;
  draft: CompanyDraft;
  onPick: (entity: CompanySiteReadLegalEntity) => void;
}>) {
  const t = useT();
  const entities = read?.legal_entities ?? [];
  if (entities.length < 2) {
    return null;
  }
  const chosen = draft.values.legal_name.trim();
  return (
    <div className="legal-choice">
      <div className="l">{t("ob.legalTitle")}</div>
      <p className="ob-sub">{t("ob.legalSub")}</p>
      <div className="legal-grid">
        {entities.map((entity) => {
          const selected = chosen !== "" && chosen === entity.name;
          return (
            <button
              key={`${entity.name}-${entity.source_url}`}
              type="button"
              className={`legal-card ${selected ? "selected" : ""}`}
              aria-pressed={selected}
              onClick={() => onPick(entity)}
            >
              <span className="fact-check">
                {selected ? <Check aria-hidden /> : <Circle aria-hidden />}
              </span>
              <span>
                <b>{entity.name}</b>
                {entity.registered_address ? (
                  <span>{entity.registered_address}</span>
                ) : null}
                {entity.register_number ? (
                  <small>{entity.register_number}</small>
                ) : null}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

export function WebsiteReadBar({
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

function isRequired(field: CompanyFieldName): boolean {
  return (REQUIRED_FIELDS as readonly CompanyFieldName[]).includes(field);
}

function CompanyFormField({
  field,
  value,
  grounded,
  edited,
  required,
  error,
  multiline,
  onChange,
  onBlur,
}: Readonly<{
  field: CompanyFieldName;
  value: string;
  grounded: ColdField | null;
  edited: boolean;
  required: boolean;
  error: string | null;
  multiline?: boolean;
  onChange: (v: string) => void;
  onBlur: () => void;
}>) {
  const t = useT();
  const id = `co-${field}`;
  const level = grounded ? confidenceLevel(grounded.confidence) : null;
  // The design-system field shape (create.tsx RecordFormBody is the reference):
  // .field + .t-label + .input/.textarea. The trust adornments (confidence,
  // read-from-site, typed-by-you) ride the label; the evidence chip sits under
  // the control. Onboarding gets no bespoke input styling — the form must read
  // as the same product as every other screen.
  return (
    <div className="field">
      <label className="t-label" htmlFor={id}>
        {coldFieldLabel(field, t)}
        {required ? " *" : ""} {level && <ConfidenceMeter level={level} />}
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
          className="textarea"
          value={value}
          required={required}
          aria-invalid={error ? true : undefined}
          onChange={(e) => onChange(e.target.value)}
          onBlur={onBlur}
        />
      ) : (
        <TextInput
          id={id}
          value={value}
          required={required}
          aria-invalid={error ? true : undefined}
          onChange={(e) => onChange(e.target.value)}
          onBlur={onBlur}
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

export function ReadFailure({ message }: Readonly<{ message: string }>) {
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

type VoicePiece = {
  ref: string;
  label: string;
  words: number;
  content: string;
  register: components["schemas"]["IngestVoiceCorpusSourceRequest"]["register"];
  kind: components["schemas"]["IngestVoiceCorpusSourceRequest"]["kind"];
};

// The corpus meter is honest: it counts only the real words the owner uploaded
// or pasted here (the build ingests exactly these). Presets below are examples
// of what will feed the voice once connected — never fabricated word counts.
// 800 mirrors the server's build floor ("at least 800 eligible own-authored
// words"): gating the button here turns that 422 into a clear, up-front ask.
const VOICE_MIN_WORDS = 800;
const PASTE_REF = "onboarding:paste";

function VoiceStep({ onBuilt }: Readonly<{ onBuilt: () => void }>) {
  const t = useT();
  const [optedIn, setOptedIn] = useState(false);
  const [pieces, setPieces] = useState<VoicePiece[]>([]);
  const [paste, setPaste] = useState("");
  const [skipped, setSkipped] = useState<string[]>([]);
  const [built, setBuilt] = useState(false);
  const [building, setBuilding] = useState(false);
  const [deferred, setDeferred] = useState(false);
  const [buildError, setBuildError] = useState<string | null>(null);
  const [derived, setDerived] = useState<
    components["schemas"]["VoiceProfile"] | null
  >(null);
  const fileRef = useRef<HTMLInputElement>(null);

  // A build in flight must not write state after the step unmounts — the parent
  // would otherwise flip voiceBuilt for a user who navigated away and make
  // step 4 claim a voice. One ref gates every post-await setState.
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const pasteWords = paste.trim() ? paste.trim().split(/\s+/).length : 0;
  const corpus = useMemo(() => {
    let spoken = 0;
    let written = 0;
    for (const p of pieces) {
      if (p.register === "spoken") {
        spoken += p.words;
      } else {
        written += p.words;
      }
    }
    written += pasteWords;
    const total = spoken + written;
    return {
      total,
      spoken,
      written,
      sources: pieces.length + (pasteWords > 0 ? 1 : 0),
    };
  }, [pieces, pasteWords]);

  const onFiles = (e: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []);
    const rejected: string[] = [];
    for (const file of files) {
      // V1 corpus is text only (features/09 §B1.1): the meter counts the real
      // words of what was read — never an estimate — and the text is KEPT so
      // the real build can ingest it. Binary documents (.docx/.pdf) are
      // refused; deferred: B-E07.5c (server-side extraction).
      if (!ACCEPTED_CORPUS_FILE.test(file.name)) {
        rejected.push(file.name);
        continue;
      }
      file.text().then((text) => {
        if (!mounted.current) {
          return;
        }
        const words = text.split(/\s+/).filter(Boolean).length;
        if (words === 0) {
          return;
        }
        const spoken = /\.(vtt|srt)$/i.test(file.name);
        const ref = `onboarding:upload:${file.name}`;
        setPieces((prev) => [
          ...prev.filter((p) => p.ref !== ref),
          {
            ref,
            label: file.name,
            words,
            content: text,
            register: spoken ? "spoken" : "general",
            kind: spoken ? "transcript" : "document",
          },
        ]);
      });
    }
    setSkipped(rejected);
    e.target.value = "";
  };

  const quality = corpusQuality(corpus.total);
  const canBuild = corpus.total >= VOICE_MIN_WORDS && !building;

  async function ingest(profileId: string, piece: VoicePiece) {
    const { error } = await api.POST("/voice-profiles/{id}/sources", {
      params: { path: { id: profileId } },
      body: {
        kind: piece.kind,
        register: piece.register,
        weight: 1,
        source_label: piece.label,
        source_ref: piece.ref,
        format: "text",
        content: piece.content,
      },
    });
    if (error) {
      throw new Error(problemMessage(error));
    }
  }

  // The build runs on the background worker; poll its durable row until a
  // terminal state. `deferred` = the monthly AI budget snoozed it — an honest
  // "still coming", not a failure; the worker keeps the durable build.
  async function pollBuild(
    profileId: string,
    buildId: string,
  ): Promise<{ status: string; detail?: string | null }> {
    for (let attempt = 0; attempt < 40 && mounted.current; attempt++) {
      const { data, error } = await api.GET(
        "/voice-profiles/{id}/builds/{buildId}",
        { params: { path: { id: profileId, buildId } } },
      );
      if (error) {
        throw new Error(problemMessage(error));
      }
      if (
        data.status === "succeeded" ||
        data.status === "failed" ||
        data.status === "deferred"
      ) {
        return { status: data.status, detail: data.status_detail };
      }
      await new Promise((resolve) => {
        globalThis.setTimeout(resolve, 1200);
      });
    }
    return { status: "deferred" };
  }

  // Reuse the owner's single profile (listVoiceProfiles caps at one) or mint it.
  async function ensureProfileId(): Promise<string> {
    const list = await api.GET("/voice-profiles");
    if (list.error) {
      throw new Error(problemMessage(list.error));
    }
    const existing = list.data.data[0]?.id;
    if (existing) {
      return existing;
    }
    const created = await api.POST("/voice-profiles", {
      body: { personality_md: "" },
    });
    if (created.error) {
      throw new Error(problemMessage(created.error));
    }
    if (!created.data.id) {
      throw new Error(t("ob.s2.failedBody"));
    }
    return created.data.id;
  }

  async function ingestCorpus(profileId: string) {
    for (const piece of pieces) {
      await ingest(profileId, piece);
    }
    if (pasteWords > 0) {
      await ingest(profileId, {
        ref: PASTE_REF,
        label: t("ob.s2.pasteSource"),
        words: pasteWords,
        content: paste,
        register: "general",
        kind: "other",
      });
    }
  }

  async function startBuild(profileId: string): Promise<string> {
    const build = await api.POST("/voice-profiles/{id}/builds", {
      params: { path: { id: profileId } },
      body: { reason: "onboarding" },
    });
    if (build.error) {
      throw new Error(problemMessage(build.error));
    }
    return build.data.id;
  }

  // A succeeded build has an active derived artifact to show; a deferred build
  // is honestly "still coming"; a failed build surfaces its safe status detail.
  async function applyOutcome(
    profileId: string,
    outcome: { status: string; detail?: string | null },
  ) {
    if (outcome.status === "failed") {
      setBuildError(outcome.detail ?? t("ob.s2.failedBody"));
      return;
    }
    if (outcome.status === "deferred") {
      setDeferred(true);
    } else {
      const profile = await api.GET("/voice-profiles/{id}", {
        params: { path: { id: profileId } },
      });
      if (!mounted.current) {
        return;
      }
      setDerived(profile.data ?? null);
    }
    setBuilt(true);
    onBuilt();
  }

  async function runBuild() {
    setBuilding(true);
    setBuildError(null);
    try {
      const profileId = await ensureProfileId();
      await ingestCorpus(profileId);
      const outcome = await pollBuild(profileId, await startBuild(profileId));
      if (mounted.current) {
        await applyOutcome(profileId, outcome);
      }
    } catch (err) {
      if (mounted.current) {
        setBuildError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (mounted.current) {
        setBuilding(false);
      }
    }
  }

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
          {SOURCES.map((s) => (
            <div key={s.id} className="src locked">
              <span className="star">
                {s.star ? <Star aria-hidden /> : <Lock aria-hidden />}
              </span>
              <span className="si">{s.icon}</span>
              <span className="sb">
                <span className="st">
                  {t(s.label)}
                  <span className={`reg ${s.reg}`}>{t(`ob.reg.${s.reg}`)}</span>
                </span>
                <span className="sh">{t(s.hint)}</span>
                <span className="added-w muted">
                  {t("ob.s2.whenConnected")}
                </span>
              </span>
            </div>
          ))}
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
        {pieces.length > 0 && (
          <ul className="vp-list" style={{ marginTop: 10 }}>
            {pieces.map((p) => (
              <li key={p.ref}>
                <Check aria-hidden /> {p.label} · {p.words.toLocaleString()}
              </li>
            ))}
          </ul>
        )}

        <div className="field" style={{ marginTop: "var(--space-3)" }}>
          <label className="t-label" htmlFor="voice-paste">
            {t("ob.s2.pasteLabel")}
          </label>
          <textarea
            id="voice-paste"
            className="textarea"
            rows={5}
            placeholder={t("ob.s2.pastePlaceholder")}
            value={paste}
            onChange={(e) => setPaste(e.target.value)}
          />
        </div>
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

        {buildError && (
          <div className="voiceout">
            <div className="card" style={{ padding: "var(--space-4)" }}>
              <b>{t("ob.s2.failedTitle")}</b>
              <p style={{ marginTop: "var(--space-2)", lineHeight: 1.55 }}>
                {buildError}
              </p>
            </div>
          </div>
        )}

        {!built && (
          <Button
            variant="primary"
            style={{ marginTop: 18 }}
            disabled={!canBuild}
            onClick={runBuild}
          >
            {building ? (
              <>
                <span className="ob-spinner" />{" "}
                {t("ob.s2.building", { count: corpus.total.toLocaleString() })}
              </>
            ) : (
              <>
                <Sparkles aria-hidden /> {t("ob.s2.build")}
              </>
            )}
          </Button>
        )}

        {!built &&
          !building &&
          corpus.total > 0 &&
          corpus.total < VOICE_MIN_WORDS && (
            <p className="t-small" style={{ marginTop: "var(--space-2)" }}>
              {t("ob.s2.minWords", { min: VOICE_MIN_WORDS.toLocaleString() })}
            </p>
          )}

        {built && deferred && (
          <div className="voiceout">
            <div className="card" style={{ padding: "var(--space-4)" }}>
              <span className="provenance provenance-human">
                <Sparkles aria-hidden style={{ width: 13, height: 13 }} />{" "}
                {t("ob.s2.deferredTitle")}
              </span>
              <p style={{ marginTop: "var(--space-3)", lineHeight: 1.55 }}>
                {t("ob.s2.deferredBody")}
              </p>
            </div>
          </div>
        )}

        {built && !deferred && (
          <div className="voiceout">
            <div className="card" style={{ padding: "var(--space-4)" }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <span className="provenance provenance-human">
                  <User aria-hidden style={{ width: 13, height: 13 }} />{" "}
                  {t("ob.s2.builtTitle")}
                </span>
                <span style={{ marginLeft: "auto" }} className="t-small">
                  {t("ob.s2.vpMeta", {
                    count: corpus.total.toLocaleString(),
                    sources: corpus.sources,
                  })}
                </span>
              </div>
              {derived?.voice_profile_md ? (
                <p
                  style={{
                    marginTop: "var(--space-3)",
                    lineHeight: 1.55,
                    whiteSpace: "pre-wrap",
                  }}
                >
                  {derived.voice_profile_md}
                </p>
              ) : (
                <p style={{ marginTop: "var(--space-3)", lineHeight: 1.55 }}>
                  {t("ob.s2.builtEmpty")}
                </p>
              )}
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
  voiceBuilt,
  profileSaved,
  profile,
}: Readonly<{
  voiceBuilt: boolean;
  profileSaved: boolean;
  profile?: CompanyProfile;
}>) {
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
  const understood = [
    { label: t("ob.field.offer_summary"), value: profile?.offer_summary },
    { label: t("ob.field.icp"), value: profile?.icp },
    {
      label: t("ob.field.value_proposition"),
      value: profile?.value_proposition,
    },
    { label: t("ob.field.buying_center"), value: profile?.buying_center },
  ].filter((item): item is { label: string; value: string } =>
    Boolean(item.value),
  );
  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s3.kick")}</div>
      <h1 className="ttl">
        {t("ob.s3.title")} <span className="em">{t("ob.s3.titleEm")}</span>
      </h1>
      {/* The subtitle claims only what the funnel actually did: "knows your
          voice" is earned by building it, not by reaching this step. */}
      <p className="ob-sub">
        {t(voiceBuilt ? "ob.s3.sub" : "ob.s3.subNoVoice")}
      </p>
      {profile && understood.length > 0 && (
        <div className="understanding-reveal">
          <div className="understanding-brand">
            <span>
              <CheckCircle2 aria-hidden />
            </span>
            <div>
              <small>{t("ob.nowUnderstands")}</small>
              <h2>{profile.display_name}</h2>
            </div>
          </div>
          <div className="understanding-grid">
            {understood.map((item) => (
              <div key={item.label}>
                <small>{item.label}</small>
                <p>{item.value}</p>
              </div>
            ))}
          </div>
          <p className="understanding-note">
            <Sparkles aria-hidden /> {t("ob.contextReady")}
          </p>
        </div>
      )}
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

function ConnectStep({
  outcome,
  onComplete,
}: Readonly<{
  outcome?: string;
  onComplete: (skipped: boolean) => Promise<void>;
}>) {
  const t = useT();
  // Returning from the Google consent lands here with an outcome in the
  // route; the Google tab is then the one that explains what happened.
  const [provider, setProvider] = useState<"imap" | "google" | "microsoft">(
    "google",
  );

  const scopes: { lead: MessageKey; rest: MessageKey }[] = [
    { lead: "ob.s4.scope1Lead", rest: "ob.s4.scope1Rest" },
    { lead: "ob.s4.scope2Lead", rest: "ob.s4.scope2Rest" },
    { lead: "ob.s4.scope3Lead", rest: "ob.s4.scope3Rest" },
    { lead: "ob.s4.scope4Lead", rest: "ob.s4.scope4Rest" },
  ];

  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s4.kick")}</div>
      <h1 className="ttl">
        {t("ob.s4.title")} <span className="em">{t("ob.s4.titleEm")}</span>
      </h1>
      <p className="ob-sub">{t("ob.s4.sub")}</p>

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

        {provider === "google" && (
          <GoogleConnectPanel outcome={outcome} onComplete={onComplete} />
        )}
        {provider === "microsoft" && (
          <MicrosoftConnectPanel onComplete={onComplete} />
        )}
        {provider === "imap" && <ImapConnectPanel onComplete={onComplete} />}

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
    </section>
  );
}

// The honest-failure banner the connect panels share.
function ConnectWarn({ title, body }: { title: string; body: string }) {
  return (
    <div className="readfail warn" style={{ maxWidth: 460, margin: "0 auto" }}>
      <span className="rfi">
        <Circle aria-hidden />
      </span>
      <div>
        <div className="rft">{title}</div>
        <p className="rfp">{body}</p>
      </div>
    </div>
  );
}

// Google: the server mints the consent URL (and the signed state + CSRF
// cookie that guard the callback); the browser just goes. The return deep
// link lands back here with the outcome in the route.
function GoogleConnectPanel({
  outcome,
  onComplete,
}: Readonly<{
  outcome?: string;
  onComplete: (skipped: boolean) => Promise<void>;
}>) {
  const t = useT();
  const google = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/connectors/{provider}/connect", {
        params: { path: { provider: "gmail" } },
        body: {},
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      if (data.authorize_url) {
        globalThis.location.assign(data.authorize_url);
      }
    },
  });

  // After a successful return, show the live connection rather than a
  // static claim — the row IS the proof (never a fake-populated screen).
  const connections = useQuery({
    queryKey: ["connectors"],
    enabled: outcome === "ok",
    queryFn: async () => {
      const { data, error } = await api.GET("/connectors");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const gmailConnected =
    connections.data?.data.some(
      (c) => c.provider === "gmail" && c.status === "connected",
    ) ?? false;

  if (outcome === "ok") {
    return (
      <div className="connect-result">
        <div className="cr-h">
          <CheckCircle2 aria-hidden /> {t("ob.s4.googleOkTitle")}
        </div>
        <p className="ob-sub" style={{ margin: "8px auto 0", maxWidth: 460 }}>
          {t("ob.s4.googleOkBody")}
        </p>
        {connections.isPending && (
          <p className="t-small" style={{ marginTop: "var(--space-3)" }}>
            {t("ob.s4.googleVerifying")}
          </p>
        )}
        {gmailConnected && (
          <>
            <span className="trustpill" style={{ marginTop: "var(--space-3)" }}>
              <ShieldCheck aria-hidden /> {t("ob.s4.googleLive")}
            </span>
            <BackfillPanel provider="gmail" />
          </>
        )}
        {!connections.isPending && !gmailConnected && (
          <ConnectWarn
            title={t("ob.s4.googleFailed")}
            body={t("ob.s4.googleRetry")}
          />
        )}
        <Button
          variant="primary"
          style={{ marginTop: "var(--space-4)" }}
          onClick={() => void onComplete(false)}
        >
          {t("ob.s4.enterCrm")} <ArrowRight aria-hidden />
        </Button>
      </div>
    );
  }

  return (
    <>
      {outcome === "denied" && (
        <ConnectWarn
          title={t("ob.s4.googleDenied")}
          body={t("ob.s4.googleRetry")}
        />
      )}
      {outcome === "error" && (
        <ConnectWarn
          title={t("ob.s4.googleFailed")}
          body={t("ob.s4.googleRetry")}
        />
      )}
      {google.isError && (
        <ConnectWarn
          title={t("ob.s4.googleFailed")}
          body={google.error.message}
        />
      )}
      <p
        className="spoken-hint"
        style={{ maxWidth: 460, margin: "4px auto 0" }}
      >
        <ShieldCheck aria-hidden /> {t("ob.s4.googleHint")}
      </p>
      <div className="connect-acts">
        <Button
          variant="primary"
          disabled={google.isPending}
          onClick={() => google.mutate()}
        >
          {google.isPending ? (
            <>
              <span className="ob-spinner" /> {t("ob.s4.connecting")}
            </>
          ) : (
            <>
              <Mail aria-hidden /> {t("ob.s4.googleBtn")}
            </>
          )}
        </Button>
        <Button onClick={() => void onComplete(true)}>
          <SkipForward aria-hidden /> {t("ob.s4.skipLater")}
        </Button>
      </div>
    </>
  );
}

function MicrosoftConnectPanel({
  onComplete,
}: Readonly<{ onComplete: (skipped: boolean) => Promise<void> }>) {
  const t = useT();
  return (
    <>
      <p className="ob-sub" style={{ margin: "0 auto 6px", maxWidth: 460 }}>
        {t("ob.s4.oauthSoon")}
      </p>
      <div className="connect-acts">
        <Button onClick={() => void onComplete(true)}>
          <SkipForward aria-hidden /> {t("ob.s4.skipLater")}
        </Button>
      </div>
    </>
  );
}

// IMAP: the one-shot pull, exactly as before — the form is the consent.
function ImapConnectPanel({
  onComplete,
}: Readonly<{ onComplete: (skipped: boolean) => Promise<void> }>) {
  const t = useT();
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

  const parsedMax = max.trim() === "" ? 30 : Number(max);
  const ready =
    host.trim() !== "" &&
    email.trim() !== "" &&
    password !== "" &&
    Number.isInteger(parsedMax) &&
    parsedMax >= 1 &&
    parsedMax <= 200;

  if (connect.data?.connected) {
    return (
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
          style={{ marginTop: "var(--space-4)" }}
          onClick={() => void onComplete(false)}
        >
          {t("ob.s4.enterCrm")} <ArrowRight aria-hidden />
        </Button>
      </div>
    );
  }

  return (
    <>
      <div className="imap-form">
        <label className="field full">
          {t("ob.s4.imapHost")}
          <input
            className="input"
            value={host}
            placeholder={t("ob.s4.imapHostPlaceholder")}
            onChange={(e) => setHostVal(e.target.value)}
          />
        </label>
        <label className="field full">
          {t("ob.s4.imapEmail")}
          <input
            className="input"
            type="email"
            autoComplete="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </label>
        <label className="field full">
          {t("ob.s4.imapPassword")}
          <input
            className="input"
            type="password"
            autoComplete="off"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        <label className="field">
          {t("ob.s4.imapMailbox")}
          <input
            className="input"
            value={mailbox}
            onChange={(e) => setMailbox(e.target.value)}
          />
        </label>
        <label className="field">
          {t("ob.s4.imapMax")}
          <input
            className="input"
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
        <ConnectWarn
          title={t("ob.s4.connectFailed")}
          body={connect.error.message}
        />
      )}
      {connect.data && !connect.data.connected && (
        <ConnectWarn
          title={t("ob.s4.connectFailed")}
          body={t("ob.s4.googleRetry")}
        />
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
        <Button onClick={() => void onComplete(true)}>
          <SkipForward aria-hidden /> {t("ob.s4.skipLater")}
        </Button>
      </div>
    </>
  );
}
