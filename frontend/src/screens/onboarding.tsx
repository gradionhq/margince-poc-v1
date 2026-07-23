import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { problemMessage } from "./common";
import {
  ManualCompanySetup,
  useCompanyContextCapabilities,
} from "./company-context";
import { OnboardingConversationScreen } from "./onboarding-conversation/index";
import "./onboarding.css";

// The onboarding entry point plus the shared vocabulary of the journey:
// the company draft (values + grounding + human-edited marks), the URL and
// payload helpers, the wizard-state write, and the voice corpus constants.
// The conversational shell (onboarding-conversation/) renders the journey;
// its acts and the form/interview/panel modules all speak the types and
// helpers defined here.

// The wizard-state step names the server contract pins; an index past the
// last one means the journey is complete.
const STEP_KEYS = ["read", "voice", "results", "connect"] as const;

// The facts endpoint accepts at most this many selected keys; preselecting
// more than the API takes would make the default state unsubmittable.
export const MAX_SELECTED_FACTS = 100;

type CompanyProfile = components["schemas"]["CompanyProfile"];
type ColdField = components["schemas"]["ColdStartField"];

type PutOnboardingState = components["schemas"]["PutOnboardingStateRequest"];
type SourceMode = "website" | "manual";

// Legal identity comes first. The remaining groups then explain the offer,
// customer and sales motion without mixing registered facts with positioning.
export const LEGAL_IDENTITY_FIELDS = [
  "display_name",
  "legal_name",
  "registered_address",
  "register_vat",
  "industry",
  "history",
] as const;
export const OFFER_FIELDS = [
  "offer_summary",
  "value_proposition",
  "usp",
] as const;
export const CUSTOMER_FIELDS = [
  "icp",
  "buying_center",
  "customer_pains",
  "desired_outcomes",
] as const;
export const SALES_FIELDS = [
  "buying_intents",
  "common_objections",
  "sales_motion",
] as const;

export type CompanyFieldName =
  | "website"
  | (typeof LEGAL_IDENTITY_FIELDS)[number]
  | (typeof OFFER_FIELDS)[number]
  | (typeof CUSTOMER_FIELDS)[number]
  | (typeof SALES_FIELDS)[number];
export type CompanyForm = Record<CompanyFieldName, string>;

// The universal semantic minimum is enough to tell later product calls who the
// company is, what it sells, and to whom. Legal and registry details stay
// optional until a workflow with a real invoicing or jurisdictional need asks.
export const REQUIRED_FIELDS = [
  "display_name",
  "offer_summary",
  "icp",
] as const satisfies readonly CompanyFieldName[];

export function isRequired(field: CompanyFieldName): boolean {
  return (REQUIRED_FIELDS as readonly CompanyFieldName[]).includes(field);
}

// The read-back can only ground the contract's ColdStartField names —
// website is always the human's to give.
type Grounded = Partial<Record<ColdField["field"], ColdField>>;

// One state object, because the three parts move together: typing a value
// drops its site grounding (the value is the human's now) and marks it typed.
export type CompanyDraft = {
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

export const EMPTY_DRAFT: CompanyDraft = {
  values: EMPTY_FORM,
  grounded: {},
  edited: new Set(),
};

function orEmpty(value: string | null | undefined): string {
  return value ?? "";
}

export function formFromProfile(p: CompanyProfile): CompanyForm {
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
export function prefill(
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

export function changeDraftField(
  draft: CompanyDraft,
  field: CompanyFieldName,
  value: string,
): CompanyDraft {
  const grounded = { ...draft.grounded };
  if (field in grounded) {
    delete grounded[field as ColdField["field"]];
  }
  return {
    values: { ...draft.values, [field]: value },
    grounded,
    edited: new Set(draft.edited).add(field),
  };
}

// A field the read-back grounded and the human has not touched still carries
// the site's evidence; anything else is the human's own.
export function groundingOf(
  draft: CompanyDraft,
  field: CompanyFieldName,
): ColdField | null {
  return draft.grounded[field as ColdField["field"]] ?? null;
}

// URL normalization/validation (S-E01.1: scheme/host/dedupe, honest invalid).
export function normalizeUrl(raw: string): {
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

function optionalDraftValue(value: string): string | null {
  const trimmed = value.trim();
  return trimmed === "" ? null : value;
}

export function onboardingDraftPayload(values: CompanyForm) {
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

class WizardStateWriteError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

export async function writeWizardState(body: PutOnboardingState) {
  const { data, error, response } = await api.PUT("/onboarding/state", {
    params: { header: { "Idempotency-Key": crypto.randomUUID() } },
    body,
  });
  if (error) {
    throw new WizardStateWriteError(response.status, problemMessage(error));
  }
  return data;
}

export function wizardStateBody(input: {
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
    step: STEP_KEYS[input.nextStep] ?? "complete",
    source_mode: input.mode,
    website_url: websiteMode && input.norm.ok ? input.norm.full : null,
    site_read_id: websiteMode ? input.readID : null,
    company_draft: onboardingDraftPayload(input.values),
    selected_fact_keys: input.factKeys,
    voice_skipped: input.skippedVoice,
    connect_skipped: input.skippedConnect,
  };
}

// The accepted corpus formats, mirroring the contract's format enum
// (crm.yaml IngestVoiceCorpusSourceRequest.format: txt/md/vtt/srt/json).
export const ACCEPTED_CORPUS_FILE = /\.(txt|md|vtt|srt|json)$/i;
export const ACCEPTED_CORPUS_ATTR = ".txt,.md,.vtt,.srt,.json";

export const TRANSCRIPT_EXT = /\.(vtt|srt|json)$/i;

// 800 mirrors the server's build floor ("at least 800 eligible own-authored
// words"): gating the build action here turns that 422 into a clear,
// up-front ask.
export const VOICE_MIN_WORDS = 800;

// pickBuiltVersion names the version the build just produced: the highest
// numbered active-or-candidate row — active when it auto-activated,
// candidate when it awaits review.
export function pickBuiltVersion(
  items: components["schemas"]["VoiceProfileVersion"][],
): components["schemas"]["VoiceProfileVersion"] | null {
  let built: components["schemas"]["VoiceProfileVersion"] | null = null;
  for (const version of items) {
    if (version.status !== "active" && version.status !== "candidate") {
      continue;
    }
    if (!built || version.profile_version > built.profile_version) {
      built = version;
    }
  }
  return built;
}

// The onboarding gate: below the `onboarding` rollout stage the rollback-safe
// manual setup floor renders; otherwise the conversational shell IS the
// onboarding.
export function OnboardingScreen() {
  const capabilities = useCompanyContextCapabilities();
  if (capabilities.data && !capabilities.data.onboarding_enabled) {
    return <ManualCompanySetup />;
  }
  return <OnboardingConversationScreen />;
}
