import { useCallback, useRef } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { problemMessage } from "../common";
import type { CompanyForm } from "../onboarding";
import { wizardStateBody, writeWizardState } from "../onboarding";
import { isCompanyField } from "./company-proposal";

// Minimal wizard-state persistence for the conversational shell: the server
// resolves GET /onboarding/company/proposal through the persisted
// site_read_id, and act checkpoints (company confirmed, finish) keep the
// classic coordinator able to take over mid-journey. Full restore is Phase
// 5. Writes are queued so a fast retry never races an earlier PUT, and a
// prior session's checkpoint is merged, never clobbered: blanks fill from
// the saved draft, and unspecified flags keep their saved values.

type OnboardingState = components["schemas"]["OnboardingState"];

type SavedState = {
  version: number;
  sourceMode: "website" | "manual" | null;
  siteReadId: string | null;
  websiteUrl: string | null;
  factKeys: string[];
  voiceSkipped: boolean;
  connectSkipped: boolean;
  draft: OnboardingState["company_draft"] | null;
};

export type WizardPersistInput = Readonly<{
  /** Classic STEPS index (0 read, 1 voice, 2 results, 3 connect, 4 complete). */
  nextStep: number;
  values: CompanyForm;
  /** Omitted fields inherit the previously saved state. */
  mode?: "website" | "manual" | null;
  readId?: string | null;
  factKeys?: string[];
  voiceSkipped?: boolean;
  connectSkipped?: boolean;
}>;

const FRESH_STATE: SavedState = {
  version: 0,
  sourceMode: null,
  siteReadId: null,
  websiteUrl: null,
  factKeys: [],
  voiceSkipped: false,
  connectSkipped: false,
  draft: null,
};

function fromServerState(data: OnboardingState): SavedState {
  return {
    version: typeof data.version === "number" ? data.version : 0,
    sourceMode: data.source_mode ?? null,
    siteReadId: data.site_read_id ?? null,
    websiteUrl: data.website_url ?? null,
    factKeys: Array.isArray(data.selected_fact_keys)
      ? data.selected_fact_keys
      : [],
    voiceSkipped: Boolean(data.voice_skipped),
    connectSkipped: Boolean(data.connect_skipped),
    draft: data.company_draft ?? null,
  };
}

async function loadSavedState(): Promise<SavedState> {
  const { data, error, response } = await api.GET("/onboarding/state");
  if (error) {
    if (response.status !== 404) {
      throw new Error(problemMessage(error));
    }
    // 404 = nothing persisted yet: the first write starts at version 0.
    return FRESH_STATE;
  }
  return fromServerState(data);
}

// A blank conversational field never clobbers a value an earlier session
// already saved; a non-blank current value always wins.
function mergedValues(
  values: CompanyForm,
  savedDraft: SavedState["draft"],
): CompanyForm {
  if (!savedDraft) {
    return values;
  }
  const merged = { ...values };
  for (const [key, savedValue] of Object.entries(savedDraft)) {
    if (
      typeof savedValue === "string" &&
      savedValue !== "" &&
      isCompanyField(key, merged) &&
      merged[key].trim() === ""
    ) {
      merged[key] = savedValue;
    }
  }
  return merged;
}

// One write on top of the saved base: current values (blanks filled from
// the saved draft), explicit input fields winning over saved ones.
async function writeMerged(
  base: SavedState,
  input: WizardPersistInput,
): Promise<SavedState> {
  const values = mergedValues(input.values, base.draft);
  const website =
    values.website.trim() !== "" ? values.website : (base.websiteUrl ?? "");
  const data = await writeWizardState(
    wizardStateBody({
      expectedVersion: base.version,
      nextStep: input.nextStep,
      mode: input.mode === undefined ? base.sourceMode : input.mode,
      readID: input.readId === undefined ? base.siteReadId : input.readId,
      norm: { ok: website.trim() !== "", full: website },
      values,
      factKeys: input.factKeys ?? base.factKeys,
      skippedVoice: input.voiceSkipped ?? base.voiceSkipped,
      skippedConnect: input.connectSkipped ?? base.connectSkipped,
    }),
  );
  return fromServerState(data);
}

export function useWizardStatePersist() {
  const saved = useRef<SavedState | null>(null);
  const queue = useRef<Promise<boolean>>(Promise.resolve(true));

  const persist = useCallback((input: WizardPersistInput): Promise<boolean> => {
    queue.current = queue.current.then(async () => {
      try {
        saved.current ??= await loadSavedState();
        saved.current = await writeMerged(saved.current, input);
        return true;
      } catch {
        // Best-effort by design: the act must never stall on state
        // persistence. The caller learns via the false result (the proposal
        // join is gated on it and falls back to the site-read snapshot);
        // resetting forces a fresh version sync on the next attempt.
        saved.current = null;
        return false;
      }
    });
    return queue.current;
  }, []);

  return { persist };
}
