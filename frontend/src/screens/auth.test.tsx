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
import { AuthScreen, deriveWorkspaceSlug } from "./auth";

// Signup+login first-run screen: the dev slug the client derives MUST equal the
// server's identity.slugify output (else the workspace won't resolve), and a
// successful signup persists that slug and moves the user into onboarding.

// jsdom serves an opaque origin, where window.localStorage is null; the app
// client tolerates that (optional chaining), but the persistence assertion
// needs a real store, so back it with an in-memory one for the test.
function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    getItem: (key) => (map.has(key) ? (map.get(key) as string) : null),
    setItem: (key, value) => {
      map.set(key, String(value));
    },
    removeItem: (key) => {
      map.delete(key);
    },
    clear: () => map.clear(),
    key: (index) => Array.from(map.keys())[index] ?? null,
    get length() {
      return map.size;
    },
  };
}

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

describe("deriveWorkspaceSlug mirrors the server slugify", () => {
  // Each pair is the exact output the Go identity.slugify produces — keep them
  // in lock-step: lowercase, keep [a-z0-9], map space/-/_ to '-', trim '-'.
  const cases: Array<[string, string]> = [
    ["Acme Corp", "acme-corp"],
    ["  Gradion  ", "gradion"],
    ["Müller GmbH", "mller-gmbh"],
    ["Acme_Corp 2", "acme-corp-2"],
    ["-Hello-", "hello"],
    ["A & B", "a--b"],
    ["ALL CAPS", "all-caps"],
  ];
  for (const [input, expected] of cases) {
    it(`"${input}" -> "${expected}"`, () => {
      expect(deriveWorkspaceSlug(input)).toBe(expected);
    });
  }
});

describe("AuthScreen signup", () => {
  it("posts the workspace, persists the derived slug, enters onboarding", async () => {
    const fetchMock = vi.fn(
      async (_input: Request | string | URL) =>
        new Response(JSON.stringify({ user: {}, roles: [], teams: [] }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const onAuthed = vi.fn();

    render(<AuthScreen onAuthed={onAuthed} />);

    await userEvent.type(screen.getByLabelText("Workspace name"), "Acme Corp");
    await userEvent.type(screen.getByLabelText("Your name"), "Dana Admin");
    await userEvent.type(screen.getByLabelText("Email"), "dana@acme.test");
    await userEvent.type(
      screen.getByLabelText("Password"),
      "correct-horse-battery",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Create workspace" }),
    );

    await waitFor(() => expect(onAuthed).toHaveBeenCalled());

    const request = fetchMock.mock.calls[0]?.[0] as Request | undefined;
    expect(String(request?.url)).toContain("/v1/workspaces");
    expect(globalThis.localStorage.getItem("margince.workspaceSlug")).toBe(
      "acme-corp",
    );
    expect(window.location.hash).toBe("#/onboarding");
  });
});
