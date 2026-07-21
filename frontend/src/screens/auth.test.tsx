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
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { AuthScreen, AvailabilityScreen } from "./auth";

// The unauthenticated surface (A107/ADR-0061 §12): login is the default —
// no signup mode, no workspace field, no tenant selector on the wire — and
// the forgot-password flow renders exactly when the capabilities probe
// reports it operational.

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

// stubApi answers GET /auth/capabilities from `capabilities` and records
// every other call for the test to assert on.
function stubApi(
  capabilities: { password: boolean; password_reset: boolean },
  respond: (request: Request) => Response | Promise<Response>,
  profile: Response = ok(200, {
    name: "Margince",
    kind: "ai",
    state: "unconfigured",
    inference_mode: "none",
    providers: [],
  }),
) {
  const calls: Request[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: Request | string | URL) => {
      const request = input instanceof Request ? input : new Request(input);
      if (new URL(request.url).pathname.endsWith("/auth/capabilities")) {
        return new Response(
          JSON.stringify({ ...capabilities, oidc_providers: [] }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (new URL(request.url).pathname.endsWith("/assistant/profile")) {
        return profile;
      }
      calls.push(request);
      return respond(request);
    }),
  );
  return calls;
}

const ok = (status: number, body?: unknown) =>
  new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

describe("AuthScreen login", () => {
  it("introduces Margince as AI and renders the configured routing posture without claiming health", async () => {
    stubApi(
      { password: true, password_reset: false },
      () => ok(200),
      ok(200, {
        name: "Margince",
        kind: "ai",
        state: "configured",
        inference_mode: "hybrid",
        providers: ["anthropic", "ollama"],
      }),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    expect(screen.getByText("Margince · AI system")).toBeTruthy();
    expect(
      screen.getByText(
        "I can only use your context after Margince verifies that it's you.",
      ),
    ).toBeTruthy();
    expect(await screen.findByText("Configured")).toBeTruthy();
    expect(
      screen.getByText("Anthropic + Ollama · hybrid routing"),
    ).toBeTruthy();
    expect(screen.queryByText(/online|running|healthy/i)).toBeNull();
  });

  it("keeps login available when the optional assistant profile fails", async () => {
    stubApi(
      { password: true, password_reset: false },
      () => ok(200),
      ok(500, { title: "unavailable" }),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    expect(await screen.findByLabelText("Email address")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
    expect(screen.queryByText("Configured")).toBeNull();
  });

  it("is a login form — no signup mode, no workspace field, Enter submits, no tenant header", async () => {
    const calls = stubApi({ password: true, password_reset: false }, () =>
      ok(200, { user: {}, roles: [], teams: [] }),
    );
    const onAuthed = vi.fn();
    render(<AuthScreen onAuthed={onAuthed} />);

    expect(screen.queryByLabelText(/workspace/i)).toBeNull();
    expect(screen.queryByText(/create/i)).toBeNull();

    await userEvent.type(
      screen.getByLabelText("Email address"),
      "ada@example.com",
    );
    // Enter inside the real <form> submits — no button click needed.
    await userEvent.type(
      screen.getByLabelText("Password"),
      "correct-horse-battery{enter}",
    );

    await waitFor(() => expect(onAuthed).toHaveBeenCalled());
    const request = calls[0];
    expect(String(request?.url)).toContain("/v1/auth/login");
    expect(request?.headers.has("X-Workspace-Slug")).toBe(false);
  });

  it("answers bad credentials with the one non-enumerating message, keeps the email, clears the password", async () => {
    stubApi({ password: true, password_reset: false }, () =>
      ok(401, {
        title: "unauthorized",
        detail: "invalid email or password",
      }),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      screen.getByLabelText("Email address"),
      "ada@example.com",
    );
    await userEvent.type(screen.getByLabelText("Password"), "wrong{enter}");

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(
      "We couldn't sign you in. Check your email and password and try again.",
    );
    expect(screen.getByLabelText("Email address")).toHaveProperty(
      "value",
      "ada@example.com",
    );
    // §9.2: a rejected credential clears the password for the retry.
    expect(screen.getByLabelText("Password")).toHaveProperty("value", "");
  });

  it("presents rate limiting as its own actionable state, never a credential error", async () => {
    stubApi({ password: true, password_reset: false }, () =>
      ok(429, { title: "budget exceeded" }),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      screen.getByLabelText("Email address"),
      "ada@example.com",
    );
    await userEvent.type(screen.getByLabelText("Password"), "whatever{enter}");

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(
      "Too many sign-in attempts. Wait a moment and try again.",
    );
  });

  it("presents a server outage as connectivity, not wrong credentials", async () => {
    stubApi({ password: true, password_reset: false }, () =>
      ok(500, { title: "boom" }),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      screen.getByLabelText("Email address"),
      "ada@example.com",
    );
    await userEvent.type(screen.getByLabelText("Password"), "whatever{enter}");

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Margince couldn't be reached");
  });

  it("restores a deep link after login instead of forcing home", async () => {
    stubApi({ password: true, password_reset: false }, () =>
      ok(200, { user: {}, roles: [], teams: [] }),
    );
    window.location.hash = "#/deals/d-42";
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      screen.getByLabelText("Email address"),
      "ada@example.com",
    );
    await userEvent.type(
      screen.getByLabelText("Password"),
      "correct-horse-battery{enter}",
    );

    await waitFor(() => expect(window.location.hash).toBe("#/deals/d-42"));
  });

  it("renders the session notices the boundary hands it", async () => {
    stubApi({ password: true, password_reset: false }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} notice="session-expired" />);
    expect(
      await screen.findByText(
        "Your session expired. Sign in again to continue.",
      ),
    ).toBeTruthy();
    cleanup();

    stubApi({ password: true, password_reset: false }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} notice="signed-out" />);
    expect(await screen.findByText("You have been signed out.")).toBeTruthy();
  });

  it("hides the forgot-password link when the capability is off, shows it when on", async () => {
    stubApi({ password: true, password_reset: false }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    await screen.findByLabelText("Email address");
    expect(screen.queryByText("Forgot password?")).toBeNull();
    cleanup();

    stubApi({ password: true, password_reset: true }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    expect(await screen.findByText("Forgot password?")).toBeTruthy();
  });
});

describe("AuthScreen forgot password", () => {
  it("requests the reset and confirms neutrally", async () => {
    const calls = stubApi({ password: true, password_reset: true }, () =>
      ok(202),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.click(await screen.findByText("Forgot password?"));
    await userEvent.type(
      screen.getByLabelText("Email address"),
      "ada@example.com{enter}",
    );

    expect(await screen.findByText("Check your inbox")).toBeTruthy();
    expect(String(calls[0]?.url)).toContain("/v1/auth/forgot-password");
  });
});

describe("AuthScreen reset deep link", () => {
  it("redeems the emailed token and lands back at sign-in", async () => {
    const calls = stubApi({ password: true, password_reset: true }, () =>
      ok(204),
    );
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/reset-password",
      search: "?token=raw-reset-token",
      origin: "http://localhost",
    });
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      await screen.findByLabelText("New password"),
      "an entirely new password{enter}",
    );

    expect(await screen.findByText("Password updated")).toBeTruthy();
    const request = calls[0];
    expect(String(request?.url)).toContain("/v1/auth/reset-password");
    expect(await request?.text()).toContain("raw-reset-token");
  });

  it("offers a fresh link on a spent token — one neutral refusal", async () => {
    stubApi({ password: true, password_reset: true }, () =>
      ok(401, { title: "unauthorized", detail: "invalid, used, or expired" }),
    );
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/reset-password",
      search: "?token=spent-token",
      origin: "http://localhost",
    });
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      await screen.findByLabelText("New password"),
      "an entirely new password{enter}",
    );

    expect(
      await screen.findByText("That reset link is invalid, used, or expired."),
    ).toBeTruthy();
    expect(screen.getByText("Request a new link")).toBeTruthy();
  });
});

describe("AvailabilityScreen", () => {
  it("presents connectivity and installation problems as availability with a retry", async () => {
    const onRetry = vi.fn();
    render(<AvailabilityScreen kind="connection" onRetry={onRetry} />);
    expect(screen.getByText("Margince couldn't be reached")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalled();
    cleanup();

    render(<AvailabilityScreen kind="installation" onRetry={vi.fn()} />);
    expect(screen.getByText("Installation not ready")).toBeTruthy();
    // No credential fields: this is not a login problem.
    expect(screen.queryByLabelText("Email address")).toBeNull();
  });
});
