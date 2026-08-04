/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import { OnboardingScreen } from "../onboarding";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type MessageReply = components["schemas"]["OnboardingCompanyMessageReply"];
type ColdField = components["schemas"]["ColdStartField"];

const READ_ID = "018f3a1b-0000-7000-8000-0000000000c3";

const savedProfile = {
  organization_id: "018f3a1b-0000-7000-8000-0000000000a1",
  display_name: "Gradion",
  website: "gradion.com",
  legal_name: "Gradion GmbH",
  registered_address: "Hauptstrasse 1, 10115 Berlin",
  register_vat: "DE123456789",
  industry: "Robotics",
  offer_summary: "Revenue software for manufacturers",
  icp: "Mid-market manufacturers",
};

function grounded(
  field: ColdField["field"],
  value: string,
  snippet: string,
): ColdField {
  return {
    field,
    value,
    evidence_snippet: snippet,
    source_kind: "url",
    source_url: "https://gradion.com",
    confidence: 0.9,
  };
}

const readingRead = {
  id: READ_ID,
  target_kind: "onboarding",
  organization_id: null,
  root_url: "https://gradion.com",
  status: "reading",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: "crawling",
  pages_read: 1,
  pages: [{ url: "https://gradion.com", status: "fetched", kind: "home" }],
  profile_fields: [
    grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
  ],
  facts: [],
  comparisons: [],
  people: [],
  legal_entities: [],
  warnings: [],
  draft_version: 1,
  proposal_hash: "proposal-1",
  created_at: "2026-07-22T08:00:00Z",
  updated_at: "2026-07-22T08:00:01Z",
} as const satisfies CompanySiteRead;

const readyRead = {
  ...readingRead,
  status: "ready",
  phase: null,
  pages_read: 3,
  draft_version: 2,
  proposal_hash: "proposal-2",
  profile_fields: [
    grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
    grounded("display_name", "Gradion", "Gradion"),
    grounded(
      "offer_summary",
      "Revenue software for manufacturers",
      "We build revenue software",
    ),
    grounded("icp", "Mid-market manufacturers", "We serve manufacturers"),
  ],
  facts: [
    {
      category: "company",
      field: "founded_year",
      value: "2021",
      value_key: "founded_year:2021",
      evidence_snippet: "Founded in 2021",
      evidence_url: "https://gradion.com/about",
      confidence: 0.88,
    },
  ],
} as const satisfies CompanySiteRead;

function proposalFor(read: CompanySiteRead): Proposal {
  return {
    ready: true,
    fields: read.profile_fields.map((field) => ({
      field: field.field,
      value: field.value,
      confidence: field.confidence,
      evidence_snippet: field.evidence_snippet,
      source_url: field.source_url ?? "https://gradion.com",
    })),
    facts: [...read.facts],
    open_questions: [],
    remaining_required_fields: [],
    draft_version: read.draft_version,
    proposal_hash: read.proposal_hash,
  };
}

const zeroRuntime: MessageReply["ai_runtime"] = {
  currency: "USD",
  call_attempts: 1,
  tokens_in: 100,
  tokens_out: 20,
  latency_ms: 500,
  estimated_cost_microusd: 0,
  unpriced_calls: 0,
  models: [],
};

const defaultReply: MessageReply = {
  kind: "answer",
  act: "company",
  message: "Noted.",
  proposed_changes: [],
  citations: [],
  remaining_required_fields: [],
  available_action: "confirm_company",
  ai_runtime: zeroRuntime,
};

type StubOptions = {
  startRead?: CompanySiteRead;
  read?: CompanySiteRead;
  proposal?: Proposal;
  /** Error status for GET /onboarding/company/proposal (resilience tests). */
  proposalStatus?: number;
  /** Error status for the site-read poll GET (resilience tests). */
  pollStatus?: number;
  messageReply?: MessageReply;
  /** A 409 the confirm POST returns instead of succeeding, RFC-7807-shaped
   * so `problemCodeOf` reads the same `code` the real backend sentinels
   * carry (version_skew / conflict). */
  confirmProblem?: { code: string; detail: string };
  /** GET /company's answer while a confirmProblem is staged: whether the
   * confirmation this 409 blocked had, in fact, already landed. */
  companyAlreadyExists?: boolean;
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(options: StubOptions = {}) {
  const calls: Request[] = [];
  let version = 0;
  // GET /company must still answer "no company yet" for the shell's OWN
  // startup restore probe — only the confirm attempt itself may have
  // raced another one home, and only after this session's own POST to
  // /confirm actually went out.
  let confirmAttempted = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push(request);
      const path = new URL(request.url).pathname;
      if (path.endsWith("/ai/profile")) {
        return jsonResponse({
          name: "Margince",
          kind: "ai",
          state: "configured",
          inference_mode: "cloud",
          providers: ["gemini"],
          configured_models: [
            {
              tier: "cheap_cloud",
              provider: "gemini",
              model: "gemini-3.5-flash",
            },
          ],
        });
      }
      if (path.endsWith("/company/context/capabilities")) {
        return jsonResponse({
          onboarding_enabled: true,
          read_enabled: true,
          rollout: "ga",
        });
      }
      if (path.endsWith("/onboarding/state") && request.method === "GET") {
        return jsonResponse({ detail: "not started" }, 404);
      }
      if (path.endsWith("/onboarding/state") && request.method === "PUT") {
        const body = (await request.clone().json()) as Record<string, unknown>;
        version += 1;
        return jsonResponse({
          ...body,
          path: "creator",
          version,
          completed_at: null,
          created_at: "2026-07-22T08:00:00Z",
          updated_at: "2026-07-22T08:01:00Z",
        });
      }
      if (path.endsWith("/onboarding/company/proposal")) {
        if (options.proposalStatus !== undefined) {
          return jsonResponse(
            { detail: "no proposal" },
            options.proposalStatus,
          );
        }
        return jsonResponse(options.proposal ?? proposalFor(readyRead));
      }
      if (
        path.endsWith("/onboarding/company/messages") &&
        request.method === "POST"
      ) {
        return jsonResponse(options.messageReply ?? defaultReply);
      }
      if (path.endsWith("/company/site-reads") && request.method === "POST") {
        return jsonResponse(options.startRead ?? readingRead, 202);
      }
      if (path.includes("/company/site-reads/") && path.endsWith("/confirm")) {
        confirmAttempted = true;
        if (options.confirmProblem) {
          return jsonResponse(options.confirmProblem, 409);
        }
        return jsonResponse(savedProfile);
      }
      if (path.includes("/company/site-reads/") && request.method === "GET") {
        if (options.pollStatus !== undefined) {
          return jsonResponse(
            { detail: "read fetch failed" },
            options.pollStatus,
          );
        }
        return jsonResponse(options.read ?? readyRead);
      }
      if (path.endsWith("/company") && request.method === "GET") {
        if (confirmAttempted && options.companyAlreadyExists) {
          return jsonResponse(savedProfile);
        }
        return jsonResponse({ detail: "no company yet" }, 404);
      }
      if (path.endsWith("/company") && request.method === "PUT") {
        return jsonResponse(savedProfile);
      }
      throw new Error(`unstubbed request: ${request.method} ${request.url}`);
    }),
  );
  return calls;
}

function render(ui: ReactNode) {
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

async function submitWebsite() {
  const composer = await screen.findByRole("textbox", {
    name: /Your website address/,
  });
  await userEvent.type(composer, "gradion.com{Enter}");
}

function confirmCardElement(): HTMLElement {
  const card = document.querySelector(".ob-conv-confirm");
  expect(card).not.toBeNull();
  return card as HTMLElement;
}

function requestsTo(calls: Request[], path: string, method: string) {
  return calls.filter(
    (request) => request.url.includes(path) && request.method === method,
  );
}

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("the conversational company act", () => {
  it("folds poll-delta narration into an activity group, and a clean read reaches the confirm card with no terminal bubble", async () => {
    stubApi();
    render(<OnboardingScreen />);

    await submitWebsite();

    expect(await screen.findByText(/Reading gradion\.com now/)).toBeTruthy();
    // Progress is what the machine did, not a message: it folds to one line
    // (the latest step and a count) and the full trail opens on demand.
    const fold = await screen.findByRole("button", { name: /steps/ });
    await userEvent.click(fold);
    expect(
      screen.getByText(/Learned Registered legal name: Gradion GmbH/),
    ).toBeTruthy();
    // The latest step doubles as the fold's summary line, so the text can
    // legitimately appear twice: once folded, once in the opened trail.
    expect(
      screen.getAllByText(
        /Learned Ideal customer profile|Learned Ideal customer/,
      ).length,
    ).toBeGreaterThan(0);
    // The read concluded silently: no outcome bubble sits between the fold
    // and the confirm card that follows it.
    expect(
      await screen.findByRole("button", { name: /Continue/ }),
    ).toBeTruthy();
    expect(screen.queryByText(/I could not read/)).toBeNull();
  });

  it("asks the proposal's open question and answering posts the authorizing selected_option", async () => {
    const entityClarify = {
      id: "clarify:legal_name:2",
      question: "Which legal entity is this installation for?",
      field: "legal_name",
      options: [
        {
          value: "Gradion GmbH",
          label: "Gradion GmbH",
          evidence_url: "https://gradion.com/impressum",
          evidence_snippet: "Gradion GmbH, Berlin",
          detail: null,
        },
        {
          value: "Gradion Holding GmbH",
          label: "Gradion Holding GmbH",
          evidence_url: "https://gradion.com/impressum",
          evidence_snippet: "Gradion Holding GmbH, Berlin",
          detail: null,
        },
      ],
      allow_free_text: false,
    };
    const calls = stubApi({
      proposal: { ...proposalFor(readyRead), open_questions: [entityClarify] },
      messageReply: {
        ...defaultReply,
        kind: "clarification",
        proposed_changes: [
          {
            field: "legal_name",
            value: "Gradion Holding GmbH",
            reason: "You chose this entity.",
          },
          {
            field: "industry",
            value: "Nonsense the selection never authorized",
            reason: "model overreach",
          },
        ],
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();
    expect(
      await screen.findByRole("heading", {
        name: /Which legal entity is this installation for\?/,
      }),
    ).toBeTruthy();

    // The decision scene: choose, then confirm — a candidate is picked as a
    // radio and only Continue puts it on the record.
    await userEvent.click(
      screen.getByRole("radio", { name: /Gradion Holding GmbH/ }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => {
      expect(
        requestsTo(calls, "/onboarding/company/messages", "POST").length,
      ).toBeGreaterThan(0);
    });
    const body = (await requestsTo(
      calls,
      "/onboarding/company/messages",
      "POST",
    )[0]
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.act).toBe("company");
    expect(body.selected_option).toEqual({
      clarify_id: "clarify:legal_name:2",
      field: "legal_name",
      value: "Gradion Holding GmbH",
    });

    // Only the selection-authorized change lands in the review; the model's
    // extra proposal never auto-applies.
    await screen.findByText(/Here is everything I found/);
    const card = confirmCardElement();
    // "Gradion Holding GmbH" now also names the identity card's jump link to
    // this same field.
    await waitFor(() => {
      expect(
        within(card).getAllByText("Gradion Holding GmbH").length,
      ).toBeGreaterThan(0);
    });
    expect(
      screen.queryByText(/Nonsense the selection never authorized/),
    ).toBeNull();
  });

  it("disables Continue while required fields are missing and says which", async () => {
    const thinRead = {
      ...readyRead,
      profile_fields: [
        grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
      ],
    } satisfies CompanySiteRead;
    stubApi({
      read: thinRead,
      proposal: {
        ...proposalFor(thinRead),
        remaining_required_fields: ["display_name", "offer_summary", "icp"],
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();

    const accept = (await screen.findByRole("button", {
      name: /Continue/,
    })) as HTMLButtonElement;
    expect(accept.disabled).toBe(true);
    expect(await screen.findByText(/I still need:/)).toBeTruthy();
  });

  it("lists every outstanding row the board itself counts in the rail, and a click jumps to and focuses the exact row", async () => {
    const thinRead = {
      ...readyRead,
      profile_fields: [
        grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
      ],
    } satisfies CompanySiteRead;
    stubApi({
      read: thinRead,
      proposal: {
        ...proposalFor(thinRead),
        remaining_required_fields: ["display_name", "offer_summary", "icp"],
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();
    await screen.findByRole("button", { name: /Continue/ });

    // The rail — never the surface — carries this list; it is read from the
    // SAME rowFor/isWork pair the board's own section nav counts with, so it
    // names every empty row (not only the ones the confirm gate blocks on),
    // not just the required trio.
    const rail = document.querySelector(".mw-thread");
    expect(rail).not.toBeNull();
    within(rail as HTMLElement).getByRole("heading", {
      name: "These need your input",
    });
    const attention = (rail as HTMLElement).querySelector(
      ".ob-conv-attention",
    ) as HTMLElement;
    expect(attention).not.toBeNull();
    const items = within(attention).getAllByRole("button");
    // Every field but legal_name is empty: 16 fields minus the one grounded.
    expect(items).toHaveLength(15);

    await userEvent.click(items[0]);
    const row = document.getElementById("ob-triage-row-display_name");
    expect(row).not.toBeNull();
    // The jump lands in the field's own control, not merely on the row that
    // contains it: a click on a to-do means "let me fill this in", and
    // display_name's row is already expanded (it is a required, empty
    // field, open by default), so its input exists right away.
    expect(document.activeElement?.tagName).toBe("INPUT");
    expect((row as HTMLElement).contains(document.activeElement)).toBe(true);
  });

  it("says '1 field blocks confirm', not '1 fields block confirm', when only one required field is empty", async () => {
    // Every field but icp grounded, so icp is the ONLY row that blocks
    // confirm — the rest being filled means there is no advisory work to
    // fold in either.
    const almostReadyRead = {
      ...readyRead,
      profile_fields: [
        grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
        grounded("display_name", "Gradion", "Gradion"),
        grounded(
          "offer_summary",
          "Revenue software for manufacturers",
          "We build revenue software",
        ),
        grounded("registered_address", "Berlin, Germany", "Berlin, Germany"),
        grounded("register_vat", "DE123456789", "DE123456789"),
        grounded("industry", "Robotics", "We build robots"),
        grounded("history", "Founded 2019", "Founded 2019"),
        grounded("value_proposition", "Faster onboarding", "Faster onboarding"),
        grounded("usp", "AI-native from day one", "AI-native from day one"),
        grounded(
          "buying_center",
          "Ops and RevOps leads",
          "Ops and RevOps leads",
        ),
        grounded(
          "customer_pains",
          "Manual onboarding takes weeks",
          "Manual onboarding takes weeks",
        ),
        grounded("desired_outcomes", "Faster ramp", "Faster ramp"),
        grounded(
          "buying_intents",
          "Evaluating CRM replacements",
          "Evaluating CRM replacements",
        ),
        grounded("common_objections", "Migration risk", "Migration risk"),
        grounded("sales_motion", "Sales-assisted", "Sales-assisted"),
      ],
    } satisfies CompanySiteRead;
    stubApi({
      read: almostReadyRead,
      proposal: {
        ...proposalFor(almostReadyRead),
        remaining_required_fields: ["icp"],
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();
    await screen.findByRole("button", { name: /Continue/ });

    expect(
      screen.getByText(
        "Your review is ready on the right. 1 field blocks confirm.",
      ),
    ).toBeTruthy();
    expect(screen.queryByText(/1 fields block confirm/)).toBeNull();
  });

  it("lets a human fill a missing required field right in the thread, enabling Continue", async () => {
    const thinRead = {
      ...readyRead,
      profile_fields: [
        grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
      ],
    } satisfies CompanySiteRead;
    stubApi({
      read: thinRead,
      proposal: {
        ...proposalFor(thinRead),
        remaining_required_fields: ["display_name", "offer_summary", "icp"],
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();

    const accept = (await screen.findByRole("button", {
      name: /Continue/,
    })) as HTMLButtonElement;
    expect(accept.disabled).toBe(true);

    // By role, not by label text alone: the triage map's jump squares carry
    // the same field name in their accessible names, and the fill affordance
    // under test is the text control.
    const card = confirmCardElement();
    await userEvent.type(
      within(card).getByRole("textbox", { name: /Company name/ }),
      "Gradion",
    );
    await userEvent.type(
      within(card).getByRole("textbox", { name: /What do you sell\?/ }),
      "Revenue software for manufacturers",
    );
    await userEvent.type(
      within(card).getByRole("textbox", { name: /Ideal customer/ }),
      "Mid-market manufacturers",
    );

    await waitFor(() => {
      expect(accept.disabled).toBe(false);
    });
  });

  it("confirms with the proposal's draft_version and proposal_hash", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);

    await submitWebsite();

    const accept = (await screen.findByRole("button", {
      name: /Continue/,
    })) as HTMLButtonElement;
    await waitFor(() => {
      expect(accept.disabled).toBe(false);
    });
    await userEvent.click(accept);

    await waitFor(() => {
      expect(requestsTo(calls, "/confirm", "POST").length).toBe(1);
    });
    const body = (await requestsTo(calls, "/confirm", "POST")[0]
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.draft_version).toBe(2);
    expect(body.proposal_hash).toBe("proposal-2");
    // The composer-typed bare domain reaches the profile as the canonical
    // URL, exactly like the classic form's website field.
    expect((body.profile as Record<string, unknown>).website).toBe(
      "https://gradion.com",
    );
    expect(await screen.findByText(/Company profile confirmed/)).toBeTruthy();
    // The machine advanced straight into the voice act's collect scene.
    expect(await screen.findByText(/Teach me how you write\./)).toBeTruthy();
  });

  // The invariant the double-confirm dead end violated: a 409 always leaves
  // the reader with something they can do, never a live button whose next
  // press can only fail the same way again.
  it("a stale-draft 409 leaves the reader able to succeed on retry", async () => {
    const calls = stubApi({
      confirmProblem: { code: "version_skew", detail: "draft changed" },
    });
    render(<OnboardingScreen />);

    await submitWebsite();
    const accept = (await screen.findByRole("button", {
      name: /Continue/,
    })) as HTMLButtonElement;
    await waitFor(() => {
      expect(accept.disabled).toBe(false);
    });
    await userEvent.click(accept);

    // The dedicated notice, not the raw server detail glued into the
    // generic "I could not save" sentence.
    expect(await screen.findByText(/Your review just updated/)).toBeTruthy();
    expect(screen.queryByText(/draft changed/)).toBeNull();
    // The read is re-fetched so the NEXT retry sends the current draft
    // rather than the same stale one forever — the honest fix for skew,
    // since the poll that would otherwise catch this already stopped once
    // the read went terminal.
    await waitFor(() => {
      expect(requestsTo(calls, "/company/site-reads/", "GET").length).toBe(2);
    });
    // Nothing about the button itself is disabled: the reader can press it
    // again right away.
    expect(accept.disabled).toBe(false);
  });

  // The other half: a state that is not retryable at all must not be
  // presented as if it were.
  it("an already-confirmed 409 moves the reader forward instead of leaving them pressing a dead button", async () => {
    stubApi({
      confirmProblem: { code: "conflict", detail: "already confirmed" },
      companyAlreadyExists: true,
    });
    render(<OnboardingScreen />);

    await submitWebsite();
    const accept = (await screen.findByRole("button", {
      name: /Continue/,
    })) as HTMLButtonElement;
    await waitFor(() => {
      expect(accept.disabled).toBe(false);
    });
    await userEvent.click(accept);

    // The confirmation this click was blocked from making had, in fact,
    // already landed (a duplicate submit, or another tab) — the reader is
    // moved on exactly as a fresh success would, not invited to press
    // Continue again for a state that can never succeed that way.
    expect(await screen.findByText(/Company profile confirmed/)).toBeTruthy();
    expect(await screen.findByText(/Teach me how you write\./)).toBeTruthy();
    expect(screen.queryByText(/already confirmed/)).toBeNull();
  });

  // The third server state (read not yet confirmable) shares the same
  // generic 409 code as "already confirmed"; GET /company is how the two
  // are told apart, and this is the branch where that check comes back
  // empty — genuinely nothing to move forward to.
  it("a not-yet-confirmable 409 says so plainly instead of implying a retry would work", async () => {
    stubApi({
      confirmProblem: { code: "conflict", detail: "read not ready" },
      companyAlreadyExists: false,
    });
    render(<OnboardingScreen />);

    await submitWebsite();
    const accept = (await screen.findByRole("button", {
      name: /Continue/,
    })) as HTMLButtonElement;
    await waitFor(() => {
      expect(accept.disabled).toBe(false);
    });
    await userEvent.click(accept);

    expect(await screen.findByText(/not ready to confirm yet/)).toBeTruthy();
    expect(screen.queryByText(/read not ready/)).toBeNull();
    expect(screen.queryByText(/Company profile confirmed/)).toBeNull();
  });

  it("lets the workbench frame introduce itself once, not once per act", async () => {
    stubApi();
    render(<OnboardingScreen />);

    await submitWebsite();
    const accept = (await screen.findByRole("button", {
      name: /Continue/,
    })) as HTMLButtonElement;
    await waitFor(() => {
      expect(accept.disabled).toBe(false);
    });
    // The frame's entrance is the class the animation hangs off, and it is worn
    // by the first shell of the setup.
    expect(document.querySelector(".ob-workbench-panel")?.className).toContain(
      "ob-panel",
    );

    await userEvent.click(accept);
    await screen.findByText(/Teach me how you write\./);

    // The next act mounts its own shell. The rail, the brand line, the orb and
    // the runtime chip are already on screen by now — they are the frame, so
    // re-entering would make the reader's step forward look like a page load.
    expect(
      document.querySelector(".ob-workbench-panel")?.className,
    ).not.toContain("ob-panel");
  });

  it("persists wizard state on read start so the proposal endpoint can join", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);

    await submitWebsite();

    await waitFor(() => {
      expect(requestsTo(calls, "/onboarding/state", "PUT").length).toBe(1);
    });
    const body = (await requestsTo(calls, "/onboarding/state", "PUT")[0]
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.site_read_id).toBe(READ_ID);
    expect(body.source_mode).toBe("website");
    expect(body.step).toBe("read");
    expect(body.website_url).toBe("https://gradion.com");
  });

  it("still concludes and reviews from the snapshot when the proposal fails", async () => {
    stubApi({ proposalStatus: 404 });
    render(<OnboardingScreen />);

    await submitWebsite();

    // The act never stalls: one honest turn about the fallback, then a
    // review card built from the site-read snapshot itself — the read's own
    // terminal stays silent, exactly as it does when the proposal succeeds.
    expect(
      await screen.findByText(/I could not load the prepared mapping/),
    ).toBeTruthy();
    expect(
      await screen.findByRole("button", { name: /Continue/ }),
    ).toBeTruthy();
    // "Gradion GmbH" names both the identity summary and its own row.
    expect(
      within(confirmCardElement()).getAllByText("Gradion GmbH").length,
    ).toBeGreaterThan(0);
  });

  it("concludes as failed with the manual path when the poll keeps erroring", async () => {
    stubApi({ pollStatus: 500 });
    render(<OnboardingScreen />);

    await submitWebsite();

    // The act never sits silent. The gate carries the cause forward — a lost
    // poll, not a site that refused — and the terminal turn's own advice ("try
    // another URL, or tell me directly") is now the two controls in front of
    // the reader rather than a second sentence repeating them.
    expect(
      await screen.findByText(/I lost the connection while reading/),
    ).toBeTruthy();
    expect(await screen.findByLabelText(/Your website address/)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Enter the details yourself/ }),
    ).toBeTruthy();
  });

  it("never renders a proposal field without evidence in the confirm card", async () => {
    stubApi({
      proposal: {
        ...proposalFor(readyRead),
        fields: [
          ...(proposalFor(readyRead).fields ?? []),
          {
            field: "parent_company",
            value: "Umbrella Holding AG",
            confidence: 0.7,
            evidence_snippet: "",
            source_url: "https://gradion.com",
          },
        ],
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();

    await screen.findByText(/Here is everything I found/);
    const card = confirmCardElement();
    // "Gradion GmbH" names both the identity summary at the top of the card
    // and its own settled row further down.
    expect(within(card).getAllByText("Gradion GmbH").length).toBeGreaterThan(0);
    expect(within(card).queryByText("Umbrella Holding AG")).toBeNull();
  });

  // The rail's one hard rule: every reply is a chip or a jump, never typed
  // prose. Checked in both post-gate phases the rail can be in — a live
  // decision (the surface owns the candidate list) and the review (the
  // surface owns the field edits) — so removing the composer from one and
  // missing a sibling free-text control in the other cannot slip through.
  it("offers no free-text control anywhere in the rail, in either post-gate phase", async () => {
    const entityClarify = {
      id: "clarify:legal_name:2",
      question: "Which legal entity is this installation for?",
      field: "legal_name",
      options: [
        {
          value: "Gradion GmbH",
          label: "Gradion GmbH",
          evidence_url: "https://gradion.com/impressum",
          evidence_snippet: "Gradion GmbH, Berlin",
          detail: null,
        },
      ],
      allow_free_text: false,
    };
    stubApi({
      proposal: { ...proposalFor(readyRead), open_questions: [entityClarify] },
    });
    render(<OnboardingScreen />);
    await submitWebsite();
    await screen.findByRole("heading", {
      name: /Which legal entity is this installation for\?/,
    });

    const railDuringDecision = document.querySelector(".mw-thread");
    expect(railDuringDecision).not.toBeNull();
    expect(
      within(railDuringDecision as HTMLElement).queryAllByRole("textbox"),
    ).toHaveLength(0);
    expect(document.querySelector(".mw-composer")).toBeNull();

    await userEvent.click(screen.getByRole("radio", { name: /Gradion GmbH/ }));
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText(/Here is everything I found/);

    const railDuringReview = document.querySelector(".mw-thread");
    expect(railDuringReview).not.toBeNull();
    expect(
      within(railDuringReview as HTMLElement).queryAllByRole("textbox"),
    ).toHaveLength(0);
    expect(document.querySelector(".mw-composer")).toBeNull();
  });

  // The dead-end this guards against: the server can hand back several open
  // questions, but the machine only ever auto-asks the FIRST one while the
  // run is still active. Without sequential promotion, the second would
  // have nowhere to be answered — editing a field does not clear
  // `open_questions`, only a recorded answer or dismissal does. A field
  // outside the legal block, so answering the first cannot auto-dismiss the
  // second as its sibling and mask the gap.
  it("promotes the second open question to the decision surface once the first is answered", async () => {
    const nameClarify = {
      id: "clarify:legal_name:2",
      question: "Which legal entity is this installation for?",
      field: "legal_name",
      options: [
        {
          value: "Gradion GmbH",
          label: "Gradion GmbH",
          evidence_url: "https://gradion.com/impressum",
          evidence_snippet: "Gradion GmbH, Berlin",
          detail: null,
        },
      ],
      allow_free_text: false,
    };
    const industryClarify = {
      id: "clarify:industry:2",
      question: "Which industry best describes you?",
      field: "industry",
      options: [
        {
          value: "Robotics",
          label: "Robotics",
          evidence_url: "https://gradion.com",
          evidence_snippet: "We build robots",
          detail: null,
        },
      ],
      allow_free_text: false,
    };
    stubApi({
      proposal: {
        ...proposalFor(readyRead),
        open_questions: [nameClarify, industryClarify],
      },
      // Industry starts empty (unlike legal_name, which the fixture already
      // grounds), so its answer needs a matching authorized change or the
      // round trip rolls it back as unconfirmed.
      messageReply: {
        ...defaultReply,
        proposed_changes: [
          { field: "industry", value: "Robotics", reason: "You chose this." },
        ],
      },
    });
    render(<OnboardingScreen />);
    await submitWebsite();
    await screen.findByRole("heading", {
      name: /Which legal entity is this installation for\?/,
    });
    await userEvent.click(screen.getByRole("radio", { name: /Gradion GmbH/ }));
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    // The second question takes over the SAME decision surface — never the
    // review, and never left stranded with no answering control at all.
    expect(
      await screen.findByRole("heading", {
        name: /Which industry best describes you\?/,
      }),
    ).toBeTruthy();
    expect(screen.queryByText(/Here is everything I found/)).toBeNull();
    expect(
      screen.getByText(
        "I need one decision from you: Which industry best describes you? It is on the right, with the evidence for each option.",
      ),
    ).toBeTruthy();

    // Answering it, too, finally reaches the review — with both decisions
    // settled. The read only ever grounded 5 of the 16 fields (the fixture's
    // usual four plus the industry this clarify just filled), and all three
    // REQUIRED_FIELDS are among those five, so nothing left blocks confirm —
    // the other 11 rows are advisory only, and the rail says so plainly
    // rather than calling them an obstacle.
    await userEvent.click(screen.getByRole("radio", { name: /Robotics/ }));
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText(/Here is everything I found/);
    expect(
      screen.getByText(
        "Your review is ready on the right. Nothing blocks you; 11 things are worth a look.",
      ),
    ).toBeTruthy();
    const rail = document.querySelector(".mw-thread");
    expect(rail).not.toBeNull();
    expect(
      within(rail as HTMLElement).getByRole("heading", {
        name: "These need your input",
      }),
    ).toBeTruthy();
  });
});
