/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { OnboardingScreen } from "./onboarding";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];

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

const readyRead = {
  id: "018f3a1b-0000-7000-8000-0000000000b2",
  target_kind: "onboarding",
  organization_id: null,
  root_url: "https://gradion.com",
  status: "ready",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: null,
  pages_read: 2,
  pages: [
    { url: "https://gradion.com", status: "fetched", kind: "home" },
    { url: "https://gradion.com/about", status: "fetched", kind: "about" },
  ],
  profile_fields: [
    {
      field: "legal_name",
      value: "Gradion GmbH",
      evidence_snippet: "© 2026 Gradion GmbH",
      source_kind: "url",
      source_url: "https://gradion.com",
      confidence: 0.9,
    },
    {
      field: "icp",
      value: "Mid-market manufacturers",
      evidence_snippet: "We serve mid-market manufacturers",
      source_kind: "url",
      source_url: "https://gradion.com/about",
      confidence: 0.8,
    },
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
  people: [],
  warnings: [],
  draft_version: 1,
  proposal_hash: "proposal-1",
  created_at: "2026-07-19T08:00:00Z",
  updated_at: "2026-07-19T08:00:01Z",
} as const satisfies CompanySiteRead;

type StubOptions = {
  company?: typeof savedProfile | null;
  state?: Record<string, unknown> | null;
  read?: CompanySiteRead;
  readStartError?: { detail: string; status: number };
  saveError?: { detail: string; status: number };
  conflictOnce?: boolean;
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function requestPath(request: Request) {
  return new URL(request.url).pathname;
}

function stubApi(options: StubOptions = {}) {
  const calls: Request[] = [];
  let version = 0;
  let conflictPending = options.conflictOnce ?? false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push(request);
      const path = requestPath(request);
      if (path.endsWith("/onboarding/state") && request.method === "GET") {
        return options.state
          ? jsonResponse(options.state)
          : jsonResponse({ detail: "not started" }, 404);
      }
      if (path.endsWith("/onboarding/state") && request.method === "PUT") {
        if (conflictPending) {
          conflictPending = false;
          return jsonResponse({ detail: "state changed elsewhere" }, 409);
        }
        const body = (await request.clone().json()) as Record<string, unknown>;
        version += 1;
        return jsonResponse({
          ...body,
          path: "creator",
          version,
          completed_at:
            body.step === "complete" ? "2026-07-19T08:03:00Z" : null,
          created_at: "2026-07-19T08:00:00Z",
          updated_at: "2026-07-19T08:01:00Z",
        });
      }
      if (path.endsWith("/company/site-reads") && request.method === "POST") {
        if (options.readStartError) {
          return jsonResponse(
            { detail: options.readStartError.detail },
            options.readStartError.status,
          );
        }
        return jsonResponse(options.read ?? readyRead, 202);
      }
      if (path.includes("/company/site-reads/") && path.endsWith("/confirm")) {
        return jsonResponse(savedProfile);
      }
      if (path.includes("/company/site-reads/") && request.method === "GET") {
        return jsonResponse(options.read ?? readyRead);
      }
      if (path.endsWith("/company") && request.method === "GET") {
        return options.company
          ? jsonResponse(options.company)
          : jsonResponse({ detail: "no company yet" }, 404);
      }
      if (path.endsWith("/company") && request.method === "PUT") {
        if (options.saveError) {
          return jsonResponse(
            { detail: options.saveError.detail },
            options.saveError.status,
          );
        }
        return jsonResponse(savedProfile);
      }
      throw new Error(`unstubbed request: ${request.method} ${request.url}`);
    }),
  );
  return calls;
}

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

async function chooseManual() {
  await userEvent.click(
    await screen.findByRole("button", { name: /Enter it myself/ }),
  );
  await screen.findByLabelText(/Company name/);
}

async function fillRequired() {
  await userEvent.type(screen.getByLabelText(/Company name/), "Gradion");
  await userEvent.type(
    screen.getByLabelText(/What do you sell\?/),
    "Revenue software for manufacturers",
  );
  await userEvent.type(
    screen.getByLabelText(/Ideal customer/),
    "Mid-market manufacturers",
  );
}

async function readWebsite() {
  await userEvent.click(
    await screen.findByRole("button", { name: /Read my website/ }),
  );
  await userEvent.type(
    screen.getByRole("textbox", { name: "Website" }),
    "gradion.com",
  );
  await userEvent.click(
    screen
      .getAllByRole("button", { name: /Read my website/ })
      .at(-1) as HTMLElement,
  );
  await screen.findByText("Gradion GmbH");
  await userEvent.click(
    screen.getByRole("button", { name: /Review what we found/ }),
  );
  await screen.findByLabelText(/Company name/);
}

function requestTo(calls: Request[], path: string, method: string) {
  return calls.find(
    (request) => request.url.includes(path) && request.method === method,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});

describe("the optional website path", () => {
  it("offers an honest choice between website reading and manual entry", async () => {
    stubApi();
    render(<OnboardingScreen />);

    expect(
      await screen.findByRole("button", { name: /Read my website/ }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Enter it myself/ }),
    ).toBeTruthy();
    expect(screen.queryByLabelText(/Company name/)).toBeNull();
  });

  it("shows grounded findings, evidence and coverage before confirmation", async () => {
    stubApi();
    render(<OnboardingScreen />);

    await readWebsite();

    expect(
      (screen.getByLabelText(/Registered legal name/) as HTMLInputElement)
        .value,
    ).toBe("Gradion GmbH");
    expect(
      (screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement).value,
    ).toBe("Mid-market manufacturers");
    expect(screen.getByText(/© 2026 Gradion GmbH/)).toBeTruthy();
    expect(screen.getByText(/founded year/i)).toBeTruthy();
  });

  it("keeps manual entry available when the read cannot start", async () => {
    stubApi({
      readStartError: { detail: "site blocked automated access", status: 422 },
    });
    render(<OnboardingScreen />);

    await userEvent.click(
      await screen.findByRole("button", { name: /Read my website/ }),
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: "Website" }),
      "gradion.com",
    );
    await userEvent.click(
      screen
        .getAllByRole("button", { name: /Read my website/ })
        .at(-1) as HTMLElement,
    );

    expect(
      await screen.findByText("site blocked automated access"),
    ).toBeTruthy();
    await userEvent.click(
      screen.getByRole("button", { name: /Continue manually/ }),
    );
    expect(await screen.findByLabelText(/Company name/)).toBeTruthy();
  });

  it("shows a deferred read as scheduled work and keeps the manual path available", async () => {
    stubApi({
      read: {
        ...readyRead,
        status: "deferred",
        phase: null,
        status_code: "budget_deferred",
        status_detail:
          "AI budget reached its current limit. This website read will resume automatically.",
        next_attempt_at: "2026-08-01T00:00:00Z",
        profile_fields: [],
        facts: [],
      },
    });
    render(<OnboardingScreen />);

    await userEvent.click(
      await screen.findByRole("button", { name: /Read my website/ }),
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: "Website" }),
      "gradion.com",
    );
    await userEvent.click(
      screen
        .getAllByRole("button", { name: /Read my website/ })
        .at(-1) as HTMLElement,
    );

    expect(await screen.findByText("Waiting for AI budget")).toBeTruthy();
    expect(screen.getByText(/Resumes automatically/)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Enter it myself/ }),
    ).toBeTruthy();
  });
});

describe("the mandatory company minimum", () => {
  it("saves a manually entered company without requiring a website", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);
    await chooseManual();
    await fillRequired();

    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));

    await waitFor(() =>
      expect(requestTo(calls, "/company", "PUT")).toBeTruthy(),
    );
    const body = (await (requestTo(calls, "/company", "PUT") as Request)
      .clone()
      .json()) as Record<string, string>;
    expect(body.display_name).toBe("Gradion");
    expect(body.offer_summary).toBe("Revenue software for manufacturers");
    expect(body.icp).toBe("Mid-market manufacturers");
    expect(calls.some((request) => request.url.includes("site-reads"))).toBe(
      false,
    );
  });

  it("blocks an empty form and names exactly the three required fields", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);
    await chooseManual();

    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));

    expect(
      screen.getByText(
        "Fill these in before you continue: Company name, What do you sell?, Ideal customer",
      ),
    ).toBeTruthy();
    expect(requestTo(calls, "/company", "PUT")).toBeUndefined();
    expect(
      (screen.getByLabelText(/Registered legal name/) as HTMLInputElement)
        .required,
    ).toBe(false);
  });

  it("treats whitespace as missing and keeps a failed save editable", async () => {
    stubApi({ saveError: { detail: "database unavailable", status: 503 } });
    render(<OnboardingScreen />);
    await chooseManual();
    await fillRequired();
    await userEvent.clear(screen.getByLabelText(/What do you sell\?/));
    await userEvent.type(screen.getByLabelText(/What do you sell\?/), "   ");
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    expect(
      screen.getByText("Fill these in before you continue: What do you sell?"),
    ).toBeTruthy();

    await userEvent.clear(screen.getByLabelText(/What do you sell\?/));
    await userEvent.type(
      screen.getByLabelText(/What do you sell\?/),
      "Revenue software",
    );
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    expect(await screen.findByText("Couldn't save your company")).toBeTruthy();
    expect(screen.getByText("database unavailable")).toBeTruthy();
  });
});

describe("server-owned wizard state", () => {
  it("restores an unfinished company draft at the exact saved step", async () => {
    stubApi({
      state: {
        path: "creator",
        step: "confirm",
        source_mode: "manual",
        website_url: null,
        site_read_id: null,
        company_draft: {
          display_name: "Restored GmbH",
          offer_summary: "Industrial automation",
          icp: "Plant operators",
        },
        selected_fact_keys: [],
        voice_skipped: false,
        connect_skipped: false,
        version: 4,
        completed_at: null,
        created_at: "2026-07-19T08:00:00Z",
        updated_at: "2026-07-19T08:02:00Z",
      },
    });
    render(<OnboardingScreen />);

    expect(
      ((await screen.findByLabelText(/Company name/)) as HTMLInputElement)
        .value,
    ).toBe("Restored GmbH");
    expect(
      (screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement).value,
    ).toBe("Plant operators");
  });

  it("surfaces a stale-version conflict and reloads authoritative state", async () => {
    stubApi({ conflictOnce: true });
    render(<OnboardingScreen />);
    await chooseManual();

    expect((await screen.findByRole("alert")).textContent).toMatch(
      /changed in another tab/i,
    );
  });
});

describe("later optional steps remain honest", () => {
  it("shows confirmed understanding without fabricating a draft after voice is skipped", async () => {
    stubApi();
    render(<OnboardingScreen />);
    await chooseManual();
    await fillRequired();
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await userEvent.click(
      await screen.findByRole("button", { name: "Skip this step" }),
    );

    expect(screen.getByText(/You skipped the voice step/)).toBeTruthy();
    expect(screen.getByText("Margince now understands")).toBeTruthy();
    expect(screen.queryByText(/Nordwind Robotics/)).toBeNull();
  });

  it("persists completion before a skipped inbox connection exits", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);
    await chooseManual();
    await fillRequired();
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await userEvent.click(
      await screen.findByRole("button", { name: "Skip this step" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: /Connect my inbox/ }),
    );
    await userEvent.click(screen.getByRole("button", { name: /Skip for now/ }));

    await waitFor(() => expect(window.location.hash).toBe("#/home"));
    const stateWrites = calls.filter(
      (request) =>
        request.url.includes("/onboarding/state") && request.method === "PUT",
    );
    const finalBody = (await stateWrites.at(-1)?.clone().json()) as Record<
      string,
      unknown
    >;
    expect(finalBody.step).toBe("complete");
    expect(finalBody.connect_skipped).toBe(true);
  });
});
