/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { en } from "../i18n/en";
import { OnboardingBackread } from "./onboarding-backread";
import { installFetchStub, jsonResponse } from "./story-utils";

// The mailbox backread step. Two invariants carry most of the weight here and
// both are about honesty rather than layout: a count the wire did not send is
// omitted (never printed as 0), and progress is a fraction only when the server
// supplied the denominator. The rest pins the request bodies, the failure
// sentences, and that polling stops the moment the run reaches a terminal state.

type BackfillStatus = components["schemas"]["BackfillStatus"];

const STATUS_ROUTE = "GET /connectors/gmail/backfill";
const PREVIEW_ROUTE = "POST /connectors/gmail/backfill/preview";
const START_ROUTE = "POST /connectors/gmail/backfill";
const CANCEL_ROUTE = "DELETE /connectors/gmail/backfill";

function render(initial: BackfillStatus, onFinish = vi.fn()) {
  const view = rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <OnboardingBackread
          provider="gmail"
          initial={initial}
          onFinish={onFinish}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return { ...view, onFinish };
}

const previewOf = (body: Record<string, unknown>) =>
  jsonResponse({
    window: "6m",
    estimated_messages: 1234,
    computed_at: "2026-07-31T10:00:00Z",
    ...body,
  });

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("the scope preview", () => {
  it("previews the default window, then the window the user picks", async () => {
    const windows: unknown[] = [];
    installFetchStub({
      [PREVIEW_ROUTE]: (body) => {
        windows.push(body);
        return previewOf({});
      },
    });
    render({ state: "none" });

    await waitFor(() => expect(windows).toEqual([{ window: "6m" }]));
    expect(
      await screen.findByText("About 1,234 messages in that window."),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: /12 months/ }));
    await waitFor(() =>
      expect(windows).toEqual([{ window: "6m" }, { window: "12m" }]),
    );
  });

  // Changing the window must not leave the old window's estimate on screen,
  // and Start must not fire for a scope the reader never actually saw a
  // preview for.
  it("clears the previous window's estimate and holds Start until the new one settles", async () => {
    // A box, not a bare `let`: TS's control-flow narrowing otherwise loses
    // the function type across the callback boundary that assigns it.
    const deferred: { resolve: ((r: Response) => void) | null } = {
      resolve: null,
    };
    installFetchStub({
      [PREVIEW_ROUTE]: (body) => {
        if ((body as { window: string }).window === "12m") {
          return new Promise((resolve) => {
            deferred.resolve = resolve;
          });
        }
        return previewOf({ estimated_messages: 100 });
      },
    });
    render({ state: "none" });

    await screen.findByText("About 100 messages in that window.");
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Connect and read" }),
      ).not.toBeDisabled(),
    );

    await userEvent.click(screen.getByRole("radio", { name: /12 months/ }));

    // The steady pending state for the new window: no estimate for ANY
    // window is on screen — not the old one, and not a "new" one, since none
    // has arrived yet — while the request for it is still in flight.
    expect(
      screen.queryByText(/messages in that window\./),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Connect and read" }),
    ).toBeDisabled();

    deferred.resolve?.(previewOf({ estimated_messages: 900 }));
    expect(
      await screen.findByText("About 900 messages in that window."),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Connect and read" }),
      ).not.toBeDisabled(),
    );
  });

  it("qualifies an estimate the server only guessed at", async () => {
    installFetchStub({
      [PREVIEW_ROUTE]: () => previewOf({ estimate_quality: "heuristic" }),
    });
    render({ state: "none" });

    expect(
      await screen.findByText("Estimated from the mailbox, not counted yet."),
    ).toBeInTheDocument();
  });

  it("leaves a counted estimate unqualified", async () => {
    installFetchStub({
      [PREVIEW_ROUTE]: () => previewOf({ estimate_quality: "observed" }),
    });
    render({ state: "none" });

    await screen.findByText("About 1,234 messages in that window.");
    expect(screen.queryByText(/not counted yet/)).not.toBeInTheDocument();
  });

  it("reads the cost as USD minor units, never as euros", async () => {
    installFetchStub({
      [PREVIEW_ROUTE]: () =>
        previewOf({ estimated_cost_minor: 250, currency: "USD" }),
    });
    render({ state: "none" });

    // 250 minor units is 2.50 of the major unit; the symbol is the locale's
    // business, so only the amount is pinned here.
    expect(
      await screen.findByText(/Roughly\s+\S*2\.50 in model calls\./),
    ).toBeInTheDocument();
    expect(screen.queryByText(/EUR|€/)).not.toBeInTheDocument();
  });

  it("omits the cost line when the server priced nothing", async () => {
    installFetchStub({ [PREVIEW_ROUTE]: () => previewOf({}) });
    render({ state: "none" });

    await screen.findByText("About 1,234 messages in that window.");
    expect(screen.queryByText(/in model calls/)).not.toBeInTheDocument();
  });

  it("states a failed estimate and still lets the read start", async () => {
    const starts: unknown[] = [];
    installFetchStub({
      [PREVIEW_ROUTE]: () =>
        jsonResponse(
          { code: "internal", detail: "The mailbox went quiet." },
          502,
        ),
      [START_ROUTE]: (body) => {
        starts.push(body);
        return jsonResponse({ state: "queued" }, 202);
      },
      [STATUS_ROUTE]: () => jsonResponse({ state: "queued" }),
    });
    render({ state: "none" });

    expect(
      await screen.findByText(/I could not estimate that window/),
    ).toBeInTheDocument();
    expect(screen.getByText(/The mailbox went quiet\./)).toBeInTheDocument();

    const start = screen.getByRole("button", { name: "Connect and read" });
    expect(start).toBeEnabled();
    await userEvent.click(start);
    await waitFor(() => expect(starts).toEqual([{ window: "6m" }]));
  });

  // A transport failure never reaches `throwProblem` (there is no server
  // body to wrap), so its raw `Error` message is not reader-safe — only a
  // `ProblemError`'s message is server-composed and safe to show verbatim.
  it("hides a raw transport failure behind a safe sentence", async () => {
    installFetchStub({
      [PREVIEW_ROUTE]: () => {
        throw new Error("ECONNRESET: socket hang up");
      },
    });
    render({ state: "none" });

    expect(
      await screen.findByText(
        "I could not estimate that window: Something unexpected went wrong. You can still start, or pick another.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/ECONNRESET/)).not.toBeInTheDocument();
  });
});

describe("starting the read", () => {
  it("posts the window the user chose", async () => {
    const starts: unknown[] = [];
    installFetchStub({
      [PREVIEW_ROUTE]: () => previewOf({}),
      [START_ROUTE]: (body) => {
        starts.push(body);
        return jsonResponse({ state: "queued" }, 202);
      },
      [STATUS_ROUTE]: () =>
        jsonResponse({ state: "running", counts: { messages_scanned: 3 } }),
    });
    render({ state: "none" });

    await screen.findByText("About 1,234 messages in that window.");
    await userEvent.click(screen.getByRole("radio", { name: /3 months/ }));
    await userEvent.click(
      screen.getByRole("button", { name: "Connect and read" }),
    );

    await waitFor(() => expect(starts).toEqual([{ window: "3m" }]));
    expect(
      await screen.findByRole("heading", { name: "Reading your mailbox" }),
    ).toBeInTheDocument();
  });

  it("says a start failed and never claims a running read", async () => {
    installFetchStub({
      [PREVIEW_ROUTE]: () => previewOf({}),
      [START_ROUTE]: () =>
        jsonResponse(
          { code: "backfill_running", detail: "A read is already running." },
          409,
        ),
    });
    render({ state: "none" });

    await screen.findByText("About 1,234 messages in that window.");
    await userEvent.click(
      screen.getByRole("button", { name: "Connect and read" }),
    );

    expect(
      await screen.findByText(/I could not start the backread/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Reading your mailbox" }),
    ).not.toBeInTheDocument();
    // Still the setup view: the window pick is where the user was left.
    expect(screen.getByRole("radio", { name: /6 months/ })).toBeInTheDocument();
  });

  it("shows a read that is already running without starting a second one", async () => {
    const starts: unknown[] = [];
    installFetchStub({
      [START_ROUTE]: (body) => {
        starts.push(body);
        return jsonResponse({ state: "queued" }, 202);
      },
    });
    // The row the GET /connectors roster already embedded.
    render({
      state: "running",
      window: "6m",
      estimated_messages: 400,
      counts: { messages_scanned: 120 },
    });

    expect(
      await screen.findByRole("heading", { name: "Reading your mailbox" }),
    ).toBeInTheDocument();
    expect(starts).toEqual([]);
    expect(
      screen.queryByRole("button", { name: "Connect and read" }),
    ).not.toBeInTheDocument();
  });
});

describe("progress", () => {
  it("states the fraction when the server gave a denominator", async () => {
    installFetchStub({});
    render({
      state: "running",
      estimated_messages: 400,
      counts: { messages_scanned: 120 },
    });

    expect(
      await screen.findByText("120 of about 400 messages"),
    ).toBeInTheDocument();
  });

  it("counts openly, with no percentage, when there is no denominator", async () => {
    installFetchStub({});
    const { container } = render({
      state: "running",
      counts: { messages_scanned: 120 },
    });

    expect(await screen.findByText("120 messages so far")).toBeInTheDocument();
    expect(screen.queryByText(/of about/)).not.toBeInTheDocument();
    expect(screen.queryByText(/%/)).not.toBeInTheDocument();
    // No bar either: a bar with no end is a bar that lies about one.
    expect(container.querySelector(".ob-backread-bar")).toBeNull();
  });
});

describe("the tallies", () => {
  it("omits a count the wire never sent instead of printing a zero", async () => {
    installFetchStub({});
    render({
      state: "running",
      counts: { messages_scanned: 10, captured: 4 },
    });

    expect(await screen.findByText("messages read")).toBeInTheDocument();
    expect(screen.getByText("kept")).toBeInTheDocument();
    expect(screen.queryByText("ignored")).not.toBeInTheDocument();
    expect(screen.queryByText("people found")).not.toBeInTheDocument();
    expect(screen.queryByText("companies found")).not.toBeInTheDocument();
    // The absent counts are absent, not zero.
    expect(screen.queryByText("0")).not.toBeInTheDocument();
  });

  it("never reports a deals tally, which the wire does not carry", async () => {
    installFetchStub({});
    render({
      state: "done",
      counts: {
        messages_scanned: 600,
        captured: 512,
        skipped: 88,
        people_created: 90,
        organizations_created: 20,
        dedupe_candidates: 7,
      },
    });

    await screen.findByRole("heading", { name: "Here is what is in there." });
    expect(screen.queryByText(/deal/i)).not.toBeInTheDocument();
  });
});

describe("polling", () => {
  // A poll that outlives its run is invisible until it shows up as someone's
  // flat battery, so each terminal state is pinned separately.
  const terminals: BackfillStatus["state"][] = ["done", "error", "cancelled"];
  for (const state of terminals) {
    it(`stops polling once the run reaches "${state}"`, async () => {
      vi.useFakeTimers();
      let reads = 0;
      installFetchStub({
        [STATUS_ROUTE]: () => {
          reads += 1;
          return jsonResponse({ state, counts: { messages_scanned: 42 } });
        },
      });
      render({ state: "running", counts: { messages_scanned: 1 } });

      // One poll carries the run to its terminal state...
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      expect(reads).toBe(1);

      // ...and no further poll is scheduled after it.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(reads).toBe(1);
    });
  }
});

describe("outcomes", () => {
  it("names the provider-side failure class in words, not as an identifier", async () => {
    installFetchStub({});
    render({ state: "error", last_error_class: "auth" });

    expect(
      await screen.findByText(
        /The backread stopped: The provider rejected our credentials\./,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/\bauth\b/)).not.toBeInTheDocument();
  });

  it("degrades an unclassified failure to an honest generic", async () => {
    installFetchStub({});
    render({ state: "error" });

    expect(
      await screen.findByText(/we can't classify yet/),
    ).toBeInTheDocument();
  });

  it("states that a cancelled read wrote nothing", async () => {
    installFetchStub({});
    render({ state: "cancelled", counts: { messages_scanned: 40 } });

    expect(
      await screen.findByText("I stopped reading. Nothing was written."),
    ).toBeInTheDocument();
  });

  it("surfaces a refused stop instead of dropping it", async () => {
    installFetchStub({
      [CANCEL_ROUTE]: () =>
        jsonResponse(
          { code: "not_running", detail: "There is no read to stop." },
          409,
        ),
      [STATUS_ROUTE]: () =>
        jsonResponse({ state: "running", counts: { messages_scanned: 1 } }),
    });
    render({ state: "running", counts: { messages_scanned: 1 } });

    await userEvent.click(screen.getByRole("button", { name: "Stop reading" }));
    expect(
      await screen.findByText(
        "I could not stop the read: There is no read to stop. Try again — it keeps running meanwhile.",
      ),
    ).toBeInTheDocument();
  });

  it("distinguishes a partial cancel from an untouched one", async () => {
    installFetchStub({});
    render({
      state: "cancelled",
      counts: { messages_scanned: 40, captured: 12 },
    });

    expect(
      await screen.findByText(
        "I stopped reading. What was already captured stays — it is waiting for you in the inbox.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("I stopped reading. Nothing was written."),
    ).not.toBeInTheDocument();
  });

  it("says the status could not be read, and still offers the exit", async () => {
    installFetchStub({
      [STATUS_ROUTE]: () => jsonResponse({ code: "internal" }, 500),
    });
    const onFinish = vi.fn();
    rtlRender(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <LocaleProvider initial="en">
          <OnboardingBackread provider="gmail" onFinish={onFinish} />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    expect(
      await screen.findByText(/import status can't be read right now/),
    ).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: /enter your crm/i }),
    );
    expect(onFinish).toHaveBeenCalledTimes(1);
  });
});

describe("leaving", () => {
  it("walks into the app with the read still running", async () => {
    const cancels: unknown[] = [];
    installFetchStub({
      [CANCEL_ROUTE]: (body) => {
        cancels.push(body);
        return jsonResponse({ state: "cancelled" });
      },
      [STATUS_ROUTE]: () =>
        jsonResponse({ state: "running", counts: { messages_scanned: 5 } }),
    });
    const { onFinish } = render({
      state: "running",
      counts: { messages_scanned: 5 },
    });

    await userEvent.click(
      screen.getByRole("button", { name: "Explore Margince meanwhile" }),
    );
    // The mailbox is connected on this path, so the CONNECT step is not
    // skipped — only the history read would have been.
    expect(onFinish).toHaveBeenCalledTimes(1);
    expect(onFinish).toHaveBeenCalledWith(false);
    expect(cancels).toEqual([]);
  });

  it("declines the history read without starting one", async () => {
    const starts: unknown[] = [];
    installFetchStub({
      [PREVIEW_ROUTE]: () => previewOf({}),
      [START_ROUTE]: (body) => {
        starts.push(body);
        return jsonResponse({ state: "queued" }, 202);
      },
    });
    const { onFinish } = render({ state: "none" });

    await screen.findByText("About 1,234 messages in that window.");
    await userEvent.click(
      screen.getByRole("button", { name: "Do not read history now" }),
    );

    expect(onFinish).toHaveBeenCalledTimes(1);
    expect(onFinish).toHaveBeenCalledWith(false);
    expect(starts).toEqual([]);
  });

  it("shows the read-only promise before the button that acts on it", async () => {
    installFetchStub({ [PREVIEW_ROUTE]: () => previewOf({}) });
    const { container } = render({ state: "none" });

    await screen.findByText("About 1,234 messages in that window.");
    const note = screen.getByText(en["ob.backread.note"]);
    const start = screen.getByRole("button", { name: "Connect and read" });
    expect(
      note.compareDocumentPosition(start) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(container).toContainElement(note);
  });
});
