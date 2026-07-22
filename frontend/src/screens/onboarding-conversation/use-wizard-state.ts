import { useCallback, useRef } from "react";
import { api } from "../../api/client";
import { problemMessage } from "../common";
import type { CompanyForm } from "../onboarding";
import { wizardStateBody, writeWizardState } from "../onboarding";

// Minimal wizard-state persistence for the conversational shell: the server
// resolves GET /onboarding/company/proposal through the persisted
// site_read_id, so the shell must record the running read (and the current
// draft, for confirm-time consistency) even though its own restore story is
// Phase 5. Writes are queued so a fast retry never races an earlier PUT.

async function currentStateVersion(): Promise<number> {
  const { data, error, response } = await api.GET("/onboarding/state");
  if (error) {
    if (response.status !== 404) {
      throw new Error(problemMessage(error));
    }
    // 404 = nothing persisted yet: the first write starts at version 0.
    return 0;
  }
  return typeof data.version === "number" ? data.version : 0;
}

export function useWizardStatePersist() {
  const version = useRef<number | null>(null);
  const queue = useRef<Promise<void>>(Promise.resolve());

  const persistReadStart = useCallback(
    (input: { url: string; readId: string; values: CompanyForm }) => {
      queue.current = queue.current.then(async () => {
        try {
          version.current ??= await currentStateVersion();
          const data = await writeWizardState(
            wizardStateBody({
              expectedVersion: version.current,
              nextStep: 0,
              mode: "website",
              readID: input.readId,
              norm: { ok: true, full: input.url },
              values: input.values,
              factKeys: [],
              skippedVoice: false,
              skippedConnect: false,
            }),
          );
          version.current = data.version;
        } catch {
          // Best-effort by design: the act must never stall on state
          // persistence. When this write fails, the proposal join fails too
          // and the driver narrates that honestly, reviewing from the
          // site-read snapshot instead.
          version.current = null;
        }
      });
      return queue.current;
    },
    [],
  );

  return { persistReadStart };
}
