/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import { installFetchStub, jsonResponse } from "../story-utils";
import { initialConversationState } from "./conversation-machine";
import { ResultsAct } from "./results-act";

// The lead sentence's only real fork: a live, never-restored run earns
// "minutes ago", a run this act is seeing again after a reload or a return
// visit never does — see onboarding-payoff.tsx#payoffLeadKey. What actually
// tells the two apart is a "recap:*" narration id somewhere in the thread,
// and `withEntries` (conversation-machine.ts) stamps every entry with a
// `<seq>:` prefix before it ever reaches state, so the id these tests plant
// is exactly the shape the reducer produces, not a hand-picked one.

afterEach(() => {
  cleanup();
});

type OnboardingState = components["schemas"]["OnboardingState"];

const WIZARD_STATE: OnboardingState = {
  path: "creator",
  step: "results",
  source_mode: "manual",
  company_draft: {},
  selected_fact_keys: [],
  voice_skipped: true,
  connect_skipped: true,
  version: 1,
  // Now, so the elapsed check has every chance to say "minutes ago" — the
  // one thing a genuinely resumed session must still refuse to say.
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

function renderResults(threadIds: readonly string[]) {
  // Counted rather than trusted blind: `startedAt` reads `null` for the one
  // render before this resolves, and payoffLeadKey's own null branch prints
  // the SAME "leadResumed" copy a genuinely resumed session earns — so a
  // test that inspected the lead before this settled would pass whether or
  // not the resumedSession wiring actually worked.
  let stateCalls = 0;
  installFetchStub({
    "GET /onboarding/state": () => {
      stateCalls += 1;
      return jsonResponse(WIZARD_STATE);
    },
  });
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <ResultsAct
          state={{
            ...initialConversationState,
            act: "results",
            phase: "re.recap",
            thread: threadIds.map((id) => ({
              kind: "narration",
              id,
              i18nKey: "ob.conv.recap",
            })),
          }}
          dispatch={vi.fn()}
          profile={null}
          voiceBuilt={false}
          corpusWords={null}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return async () => {
    await waitFor(() => expect(stateCalls).toBeGreaterThan(0));
  };
}

describe("the payoff's resumed-session lead", () => {
  it("reads a stamped restore recap entry as a resumed session, never the elapsed-time lead", async () => {
    // The exact shape restore.ts's entries take once withEntries has stamped
    // them: the seq prefix sits BEFORE "recap:", never after it.
    const settled = renderResults(["3:recap:company", "4:recap:voice-built"]);
    await settled();

    await waitFor(() =>
      expect(
        screen.getByText("This started as an empty install."),
      ).toBeInTheDocument(),
    );
    expect(
      screen.queryByText("Minutes ago this was an empty install."),
    ).not.toBeInTheDocument();
  });

  it("still reads the live, never-restored transition as fresh", async () => {
    // The bare "recap" id the live re.recap transition stamps — "12:recap",
    // never "12:recap:anything" — must not trip the same check.
    const settled = renderResults(["12:recap"]);
    await settled();

    await waitFor(() =>
      expect(
        screen.getByText("Minutes ago this was an empty install."),
      ).toBeInTheDocument(),
    );
    expect(
      screen.queryByText("This started as an empty install."),
    ).not.toBeInTheDocument();
  });
});
