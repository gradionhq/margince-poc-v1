/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { en } from "../i18n/en";
import { CaptureExclusionsCard } from "./capture-exclusions";

// The two scopes this card draws are two different permissions, and that is the
// whole subject: a rule that binds only the reader is theirs to write, while one
// that binds the organization is admin/ops work. So every case below fixes the
// grant and asks what the card offers.
const CAPTURE_EDITOR: GrantSpec = { capture_settings: ["read", "update"] };
const READER: GrantSpec = { capture_settings: ["read"] };

const RULES = [
  { id: "cx-1", scope: "user", kind: "address", value: "ex@partner.test" },
  { id: "cx-2", scope: "workspace", kind: "domain", value: "recruiter.test" },
];

type Call = { method: string; url: string; body: unknown };

function backend(allow: GrantSpec, rules: unknown[] = RULES) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      // Built as a Request rather than read off `init`: openapi-fetch may pass a
      // Request with no init at all, and a mock that read the method from init
      // would answer every write as if it were a read.
      const request =
        input instanceof Request ? input : new Request(String(input), init);
      const url = request.url;
      const method = request.method;
      calls.push({
        method,
        url,
        body: method === "GET" ? undefined : await request.clone().json(),
      });
      if (url.endsWith("/v1/me")) {
        return new Response(JSON.stringify(meFixture({ allow })), {
          headers: { "Content-Type": "application/json" },
        });
      }
      if (method === "POST") {
        return new Response(JSON.stringify({ id: "cx-3" }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify({ data: rules }), {
        headers: { "Content-Type": "application/json" },
      });
    },
  );
  return { fetchMock, calls };
}

function Providers({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The header verb that opens the form, named by its catalog key: the dialog's
// own submit says "Exclude", so matching on that would find the wrong control.
const openForm = () =>
  screen.getByRole("button", { name: en["captureExclusions.addOpen"] });

const removeVerb = (value: string) =>
  screen.getByRole("button", {
    name: en["captureExclusions.remove"].replace("{value}", value),
  }) as HTMLButtonElement;

describe("CaptureExclusionsCard", () => {
  it("lists each rule with the scope and kind it binds by", async () => {
    const { fetchMock } = backend(CAPTURE_EDITOR);
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <CaptureExclusionsCard />
      </Providers>,
    );

    await waitFor(() =>
      expect(screen.getByText("ex@partner.test")).toBeTruthy(),
    );
    // The answer beside each rule is what it binds and what kind it is — the
    // two facts that decide whether this reader may take it back.
    expect(screen.getByText("Only me · Address")).toBeTruthy();
    expect(screen.getByText("Whole organization · Domain")).toBeTruthy();
  });

  // The permission split, on the row: a reader may take back their own rule and
  // may not take back the organization's, and the refusal names one sentence
  // rather than printing it per row.
  it("refuses only the organization-wide rule to a seat without the update grant", async () => {
    const { fetchMock } = backend(READER);
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <CaptureExclusionsCard />
      </Providers>,
    );

    await waitFor(() =>
      expect(screen.getByText("recruiter.test")).toBeTruthy(),
    );
    expect(removeVerb("ex@partner.test").disabled).toBe(false);
    const refused = removeVerb("recruiter.test");
    expect(refused.disabled).toBe(true);
    // Pointed at, not repeated: the button names the id of the one sentence on
    // the card that says why.
    const reason = refused.getAttribute("aria-describedby");
    expect(reason).toBeTruthy();
    expect(document.getElementById(reason ?? "")?.textContent).toBe(
      en["captureSettings.adminOnly"],
    );
  });

  // The sentence is a claim about this reader, so it is only made when a row on
  // the card actually bears it out.
  it("says nothing about permissions when no rule on the card is refused", async () => {
    const { fetchMock } = backend(READER, [RULES[0]]);
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <CaptureExclusionsCard />
      </Providers>,
    );

    await waitFor(() =>
      expect(screen.getByText("ex@partner.test")).toBeTruthy(),
    );
    expect(screen.queryByText(en["captureSettings.adminOnly"])).toBeNull();
  });

  it("reads as empty rather than as a failed read when nothing is excluded", async () => {
    const { fetchMock } = backend(CAPTURE_EDITOR, []);
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <CaptureExclusionsCard />
      </Providers>,
    );

    await waitFor(() =>
      expect(screen.getByTestId("capture-exclusions-empty").textContent).toBe(
        en["captureExclusions.empty"],
      ),
    );
    // Anybody may keep their OWN correspondent out, so the verb that opens the
    // form is offered even here — the dialog refuses the scope, not the card.
    expect(openForm()).toBeTruthy();
  });

  it("sends the scope, kind and value the form was filled with", async () => {
    const user = userEvent.setup();
    const { fetchMock, calls } = backend(CAPTURE_EDITOR);
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <CaptureExclusionsCard />
      </Providers>,
    );

    await waitFor(() =>
      expect(screen.getByText("ex@partner.test")).toBeTruthy(),
    );
    await user.click(openForm());
    await user.click(
      screen.getByRole("button", { name: en["captureExclusions.kind.domain"] }),
    );
    await user.type(
      screen.getByRole("textbox", { name: en["captureExclusions.addLabel"] }),
      "  jobs.test  ",
    );
    await user.click(
      screen.getByRole("button", { name: en["captureExclusions.add"] }),
    );

    await waitFor(() =>
      expect(calls.some((call) => call.method === "POST")).toBe(true),
    );
    const posted = calls.find((call) => call.method === "POST");
    // Trimmed: a value with a stray space is a rule that matches nothing, and
    // the reader cannot see the difference.
    expect(posted?.body).toEqual({
      scope: "user",
      kind: "domain",
      value: "jobs.test",
    });
  });

  // The scope a seat may not write is refused where that choice is made, rather
  // than by a card that offered the form and then rejected the submission.
  it("refuses the organization scope inside the dialog", async () => {
    const user = userEvent.setup();
    const { fetchMock, calls } = backend(READER, [RULES[0]]);
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Providers>
        <CaptureExclusionsCard />
      </Providers>,
    );

    await waitFor(() =>
      expect(screen.getByText("ex@partner.test")).toBeTruthy(),
    );
    await user.click(openForm());
    const value = screen.getByRole("textbox", {
      name: en["captureExclusions.addLabel"],
    }) as HTMLInputElement;
    await user.type(value, "jobs.test");
    await user.click(
      screen.getByRole("button", {
        name: en["captureExclusions.scope.workspace"],
      }),
    );

    expect(value.disabled).toBe(true);
    const submit = screen.getByRole("button", {
      name: en["captureExclusions.add"],
    }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    await user.click(submit);
    expect(calls.some((call) => call.method === "POST")).toBe(false);
  });
});
