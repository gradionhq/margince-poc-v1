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
import { LocaleProvider } from "../i18n";
import { OnboardingScreen } from "./onboarding";

// Onboarding honesty pins: the company step is a form the admin can fill by
// hand end to end; the website read-back only PRE-FILLS it (never clobbers,
// never guesses, never stages); Save is the confirmation and needs every field
// the contract requires; the company step cannot be skipped; and the step-3
// results tell the truth about a skipped voice step.

// The universal semantic minimum works without a website, model, legal entity
// or invoicing setup.
const REQUIRED_LABELS = [
  /Company name/,
  /What do you sell\?/,
  /Ideal customer/,
] as const;

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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
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

const readback = {
  fields: [
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
      source_url: "https://gradion.com",
      confidence: 0.8,
    },
  ],
};

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

type Route = { url: string; body: unknown; status?: number };

// The first-run backend: no company saved yet (404), a read-back on demand,
// and a PUT that echoes what it stored.
function stubApi(routes: Route[] = []) {
  const calls: Request[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (req: Request) => {
      calls.push(req);
      const route = routes.find((r) => req.url.includes(r.url));
      if (route) {
        return jsonResponse(route.body, route.status);
      }
      if (req.url.includes("/company")) {
        return req.method === "PUT"
          ? jsonResponse(savedProfile)
          : jsonResponse({ title: "no company yet" }, 404);
      }
      if (req.url.includes("/coldstart/preview")) {
        return jsonResponse(readback);
      }
      throw new Error(`unstubbed request: ${req.method} ${req.url}`);
    }),
  );
  return calls;
}

function requestTo(calls: Request[], path: string, method: string) {
  return calls.find((r) => r.url.includes(path) && r.method === method);
}

// The read-back leaves `icp` alone in every case here, so it is the honest
// anchor for "the read landed" — legal_name may be the human's own.
async function readWebsite() {
  await userEvent.type(
    screen.getByRole("textbox", { name: "Website" }),
    "gradion.com",
  );
  await userEvent.click(
    screen.getByRole("button", { name: /Read my website/ }),
  );
  await waitFor(() =>
    expect(
      (screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement).value,
    ).toBe("Mid-market manufacturers"),
  );
}

describe("the company step is a form, not a read-back gate", () => {
  it("an admin who never touches the website types the whole company and saves it", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);

    await fillRequired();
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));

    await waitFor(() =>
      expect(requestTo(calls, "/company", "PUT")).toBeTruthy(),
    );
    const put = requestTo(calls, "/company", "PUT") as Request;
    const body = (await put.clone().json()) as Record<string, string>;
    expect(body.display_name).toBe("Gradion");
    expect(body.offer_summary).toBe("Revenue software for manufacturers");
    expect(body.icp).toBe("Mid-market manufacturers");
    // Nothing is staged and nothing is approved — the form IS the 🟡 gate.
    expect(calls.some((r) => r.url.includes("/approvals"))).toBe(false);
    expect(calls.some((r) => r.url.includes("/coldstart/preview"))).toBe(false);
  });

  it("an empty form blocks the save and names every field that is missing", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);

    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));

    expect(
      screen.getByText(
        "Fill these in before you continue: Company name, What do you sell?, Ideal customer",
      ),
    ).toBeTruthy();
    expect(requestTo(calls, "/company", "PUT")).toBeUndefined();
    // still on the company step
    expect(screen.getByLabelText(/Ideal customer/)).toBeTruthy();
  });

  it("a partly filled form names only what is still missing, and saves once it is complete", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);

    await userEvent.type(screen.getByLabelText(/Company name/), "Gradion");
    await userEvent.type(
      screen.getByLabelText(/What do you sell\?/),
      "Revenue software for manufacturers",
    );
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));

    expect(
      screen.getByText("Fill these in before you continue: Ideal customer"),
    ).toBeTruthy();
    expect(requestTo(calls, "/company", "PUT")).toBeUndefined();

    await userEvent.type(
      screen.getByLabelText(/Ideal customer/),
      "Mid-market manufacturers",
    );
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));

    await waitFor(() =>
      expect(requestTo(calls, "/company", "PUT")).toBeTruthy(),
    );
  });

  it("whitespace alone does not satisfy a required field — the server would 422 it", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);

    await fillRequired();
    await userEvent.clear(screen.getByLabelText(/What do you sell\?/));
    await userEvent.type(screen.getByLabelText(/What do you sell\?/), "   ");
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));

    expect(
      screen.getByText("Fill these in before you continue: What do you sell?"),
    ).toBeTruthy();
    expect(requestTo(calls, "/company", "PUT")).toBeUndefined();
  });

  it("marks every required field, and only those, with the form-wide required marker", async () => {
    stubApi();
    render(<OnboardingScreen />);

    for (const label of REQUIRED_LABELS) {
      expect((screen.getByLabelText(label) as HTMLInputElement).required).toBe(
        true,
      );
    }
    // Legal details and the website can be discovered later — they are not gated.
    expect(
      (screen.getByLabelText(/Registered legal name/) as HTMLInputElement)
        .required,
    ).toBe(false);
    expect(
      (screen.getByRole("textbox", { name: "Website" }) as HTMLInputElement)
        .required,
    ).toBe(false);
  });

  it("renders the human label for every field, never the raw contract key", async () => {
    stubApi();
    render(<OnboardingScreen />);
    await readWebsite();

    expect(screen.getByLabelText(/Registered legal name/)).toBeTruthy();
    expect(screen.getByLabelText(/Ideal customer/)).toBeTruthy();
    expect(screen.queryByText("legal_name")).toBeNull();
    expect(screen.queryByText("icp")).toBeNull();
  });
});

describe("the company step is mandatory", () => {
  it("offers no way past it — no skip on the step, and no setup-level skip", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);

    // The voice/connect steps keep their skip; the company step has none, and
    // there is no setup-wide escape hatch beside the stepper either.
    expect(screen.queryByRole("button", { name: /Skip/i })).toBeNull();
    expect(screen.queryByRole("link", { name: /Skip/i })).toBeNull();

    // The only forward control is Continue, and it saves rather than bypasses.
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    expect(requestTo(calls, "/company", "PUT")).toBeUndefined();
    expect(window.location.hash).toBe("");
    expect(screen.getByLabelText(/Ideal customer/)).toBeTruthy();
  });
});

describe("the website read-back pre-fills the form", () => {
  it("fills the grounded fields with their evidence and confidence, and leaves the rest empty", async () => {
    stubApi();
    render(<OnboardingScreen />);
    await readWebsite();

    expect(
      (screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement).value,
    ).toBe("Mid-market manufacturers");
    expect(screen.getByText(/© 2026 Gradion GmbH/)).toBeTruthy();
    expect(screen.getAllByText("read from site").length).toBe(2);
    // Ungrounded fields stay blank — the no-guess gate, not an invented value.
    expect(
      (screen.getByLabelText(/Register \/ VAT ID/) as HTMLInputElement).value,
    ).toBe("");
  });

  it("never clobbers a field the human already typed into", async () => {
    stubApi();
    render(<OnboardingScreen />);

    await userEvent.type(
      screen.getByLabelText(/Registered legal name/),
      "Gradion Holding SE",
    );
    await readWebsite();

    const legal = screen.getByLabelText(
      /Registered legal name/,
    ) as HTMLInputElement;
    expect(legal.value).toBe("Gradion Holding SE");
    expect(screen.queryByText(/© 2026 Gradion GmbH/)).toBeNull();
    // The untouched field still takes the read-back's value.
    expect(
      (screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement).value,
    ).toBe("Mid-market manufacturers");
  });

  it("a pre-filled field the human edits becomes theirs — their value, no site evidence", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);
    await readWebsite();

    const legal = screen.getByLabelText(/Registered legal name/);
    await userEvent.clear(legal);
    await userEvent.type(legal, "Gradion Holding SE");

    expect((legal as HTMLInputElement).value).toBe("Gradion Holding SE");
    expect(screen.queryByText(/© 2026 Gradion GmbH/)).toBeNull();
    expect(screen.getAllByText("typed by you").length).toBeGreaterThan(0);

    await userEvent.type(screen.getByLabelText(/Company name/), "Gradion");
    await userEvent.type(
      screen.getByLabelText(/What do you sell\?/),
      "Revenue software for manufacturers",
    );
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await waitFor(() =>
      expect(requestTo(calls, "/company", "PUT")).toBeTruthy(),
    );
    const body = (await (requestTo(calls, "/company", "PUT") as Request)
      .clone()
      .json()) as Record<string, string>;
    expect(body.legal_name).toBe("Gradion Holding SE");
  });

  it("reading a second site replaces the first site's values and drops what it doesn't ground", async () => {
    // Site A grounds legal_name + icp; site B grounds only icp, differently.
    // A field the human typed stays theirs through both reads.
    const siteB = {
      fields: [
        {
          field: "icp",
          value: "Enterprise logistics teams",
          evidence_snippet: "Built for enterprise logistics",
          source_kind: "url",
          source_url: "https://other.example",
          confidence: 0.85,
        },
      ],
    };
    let reads = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (req: Request) => {
        if (req.url.includes("/coldstart/preview")) {
          reads += 1;
          return jsonResponse(reads === 1 ? readback : siteB);
        }
        if (req.url.includes("/company")) {
          return jsonResponse({ title: "no company yet" }, 404);
        }
        throw new Error(`unstubbed request: ${req.method} ${req.url}`);
      }),
    );
    render(<OnboardingScreen />);

    await userEvent.type(screen.getByLabelText(/Industry/), "Robotics");
    await readWebsite();
    expect(
      (screen.getByLabelText(/Registered legal name/) as HTMLInputElement)
        .value,
    ).toBe("Gradion GmbH");

    await userEvent.click(
      screen.getByRole("button", { name: /Read again|Read my website/ }),
    );
    await waitFor(() =>
      expect(
        (screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement).value,
      ).toBe("Enterprise logistics teams"),
    );

    // Site A's legal_name is gone — value AND evidence — not left standing
    // as if the new site had claimed it.
    expect(
      (screen.getByLabelText(/Registered legal name/) as HTMLInputElement)
        .value,
    ).toBe("");
    expect(screen.queryByText(/© 2026 Gradion GmbH/)).toBeNull();
    // The human's own field survived both reads.
    expect((screen.getByLabelText(/Industry/) as HTMLInputElement).value).toBe(
      "Robotics",
    );
  });

  it("a read that grounds nothing names the cause and leaves the form fillable", async () => {
    stubApi([
      {
        url: "/coldstart/preview",
        body: { detail: "couldn't ground any field" },
        status: 422,
      },
    ]);
    render(<OnboardingScreen />);

    await userEvent.type(
      screen.getByRole("textbox", { name: "Website" }),
      "gradion.com",
    );
    await userEvent.click(
      screen.getByRole("button", { name: /Read my website/ }),
    );

    expect(
      await screen.findByText("Couldn't read enough from this page"),
    ).toBeTruthy();
    expect(screen.getByText("couldn't ground any field")).toBeTruthy();
    expect(
      (screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement).value,
    ).toBe("");
  });
});

describe("a returning admin edits the saved company", () => {
  it("pre-fills the form from GET /company instead of making them retype it", async () => {
    stubApi([{ url: "/company", body: savedProfile }]);
    render(<OnboardingScreen />);

    await waitFor(() =>
      expect(
        (screen.getByLabelText(/Company name/) as HTMLInputElement).value,
      ).toBe("Gradion"),
    );
    expect(
      (screen.getByRole("textbox", { name: "Website" }) as HTMLInputElement)
        .value,
    ).toBe("gradion.com");
    expect(
      (screen.getByLabelText(/Registered legal name/) as HTMLInputElement)
        .value,
    ).toBe("Gradion GmbH");
  });
});

describe("saving the company", () => {
  it("a failed save stays on the company step and names the cause", async () => {
    stubApi([
      {
        url: "/company",
        body: { detail: "database unavailable" },
        status: 503,
      },
    ]);
    render(<OnboardingScreen />);

    await fillRequired();
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));

    expect(await screen.findByText("Couldn't save your company")).toBeTruthy();
    expect(screen.getByText("database unavailable")).toBeTruthy();
    expect(screen.getByLabelText(/Ideal customer/)).toBeTruthy();
  });
});

describe("step-3 honesty about the voice step", () => {
  it("a skipped voice step gets the neutral-starter copy and the example tag — not 'sounds like you'", async () => {
    stubApi();
    render(<OnboardingScreen />);

    await fillRequired();
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    // now on the voice step — skip it
    await userEvent.click(
      await screen.findByRole("button", { name: "Skip this step" }),
    );

    expect(screen.getByText(/You skipped the voice step/)).toBeTruthy();
    expect(
      screen.queryByText(/Drafts will sound like you from day one/),
    ).toBeNull();
    expect(screen.getByText("A sample draft")).toBeTruthy();
    expect(
      screen.getByText(/Illustrative example — not written from your data/),
    ).toBeTruthy();
  });
});

describe("connect step is skippable", () => {
  it("the mailbox-connect step offers a skip beside the connect CTA that exits to home", async () => {
    stubApi();
    render(<OnboardingScreen />);

    await fillRequired();
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await userEvent.click(
      await screen.findByRole("button", { name: "Skip this step" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: /Connect my inbox/ }),
    );

    await userEvent.click(
      screen.getByRole("button", { name: /Skip for now — I'll connect later/ }),
    );
    expect(window.location.hash).toBe("#/home");
  });
});
