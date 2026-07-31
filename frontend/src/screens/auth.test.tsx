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
import { AuthScreen, AvailabilityScreen, ProviderButtons } from "./auth";

// The unauthenticated surface (A107/ADR-0061 §12): login is the default —
// no signup mode, no workspace field, no tenant selector on the wire — and
// the forgot-password flow renders exactly when the capabilities probe
// reports it operational.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  // The UI-preview switch is read from import.meta.env at the call, so a case
  // that turns it on must not leak into the next one — the default-off surface
  // is what every other case in this file asserts.
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
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
//
// `oidc_providers` defaults to [] — the running installation's own answer while
// the OIDC flow has not shipped (§19), and what keeps every case below asserting
// a surface with no federated block. A test that wants one passes it.
function stubApi(
  capabilities: {
    password: boolean;
    password_reset: boolean;
    oidc_providers?: ReadonlyArray<{ key: string; label: string }>;
  },
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
          JSON.stringify({ oidc_providers: [], ...capabilities }),
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
    // The statement is TYPED now (ADR-0076 Decision 5), so the visible layer
    // holds a partial string for the first second and there are three nodes
    // carrying this sentence. Assert on the `.sr-only` one: it is what a screen
    // reader is handed, it is complete on the first render, and reading the
    // visible layer instead would be asserting on a race.
    expect(
      screen.getByText(
        "I can only use your context after Margince verifies that it's you.",
        { selector: ".sr-only" },
      ),
    ).toBeTruthy();
    expect(
      screen.getByText(
        "That context is your mail, your calendar, and what I can read on the open web. Nothing else, and nothing without your permission.",
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

    expect(await screen.findByLabelText("Email")).toBeTruthy();
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
    expect(
      screen.queryByText(/create (your )?workspace|create one|sign up/i),
    ).toBeNull();

    await userEvent.type(screen.getByLabelText("Email"), "ada@example.com");
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

  it("does not show success until the authenticated session probe succeeds", async () => {
    stubApi({ password: true, password_reset: false }, () =>
      ok(200, { user: {}, roles: [], teams: [] }),
    );
    const probe = vi.fn().mockRejectedValue(new Error("session rejected"));
    const { container } = render(<AuthScreen onAuthed={probe} />);

    await userEvent.type(screen.getByLabelText("Email"), "ada@example.com");
    await userEvent.type(
      screen.getByLabelText("Password"),
      "correct-horse-battery{enter}",
    );

    expect((await screen.findByRole("alert")).textContent).toContain(
      "Margince couldn't be reached",
    );
    expect(probe).toHaveBeenCalledOnce();
    expect(
      container.querySelector<HTMLElement>(".auth-surface")?.dataset.authPhase,
    ).toBe("error");
  });

  it("answers bad credentials with the one non-enumerating message, keeps the email, clears the password", async () => {
    stubApi({ password: true, password_reset: false }, () =>
      ok(401, {
        title: "unauthorized",
        detail: "invalid email or password",
      }),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(screen.getByLabelText("Email"), "ada@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "wrong{enter}");

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(
      "We couldn't sign you in. Check your email and password and try again.",
    );
    expect(screen.getByLabelText("Email")).toHaveProperty(
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

    await userEvent.type(screen.getByLabelText("Email"), "ada@example.com");
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

    await userEvent.type(screen.getByLabelText("Email"), "ada@example.com");
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

    await userEvent.type(screen.getByLabelText("Email"), "ada@example.com");
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
    await screen.findByLabelText("Email");
    expect(screen.queryByText("Forgot password?")).toBeNull();
    cleanup();

    stubApi({ password: true, password_reset: true }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    expect(await screen.findByText("Forgot password?")).toBeTruthy();
  });

  // The reset UI-preview switch (app/ui-preview.ts), on the screen. The capability
  // is `false` in both halves — the running installation's own answer, since it
  // has no mailer — so the switch is the only difference, which is the property
  // this pair exists to pin.
  it("draws the forgot-password link on a false capability only under the UI-preview switch", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    stubApi({ password: true, password_reset: false }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    await screen.findByLabelText("Email");
    expect(screen.queryByText("Forgot password?")).toBeNull();
    cleanup();

    vi.stubEnv("VITE_UI_PREVIEW_RESET", "1");
    stubApi({ password: true, password_reset: false }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    expect(await screen.findByText("Forgot password?")).toBeTruthy();
  });

  // §12: the two fields keep their VISIBLE labels, which is where this build
  // deliberately parts company with the reference artifact — it names its fields
  // with a placeholder and an aria-label. A placeholder is not a label: it
  // disappears the moment the field has content (WCAG 3.3.2). The bordered shell
  // must not quietly move the accessible name onto itself either.
  it("names both fields with a real label, not a placeholder", async () => {
    stubApi({ password: true, password_reset: true }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    for (const name of ["Email", "Password"]) {
      const field = await screen.findByLabelText(name);
      expect(field.tagName).toBe("INPUT");
      // The accessible name comes from the <label>, so it survives typing.
      expect(field.getAttribute("aria-label")).toBeNull();
    }
  });

  // §6.7: the legal line states that ACCESS is restricted — never that data is
  // safe, encrypted, sovereign or compliant, because those are outcome claims the
  // installation's own configuration can contradict (VOICE-RULE-7).
  it("states that access is restricted, and nothing about the data", async () => {
    stubApi({ password: true, password_reset: true }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    expect(
      await screen.findByText("Access to this organization is restricted."),
    ).toBeTruthy();
    expect(
      screen.queryByText(/encrypted|compliant|sovereign|your data is safe/i),
    ).toBeNull();
    // Server paths, not app routes: both documents have to be readable BEFORE
    // anyone authenticates, so they cannot sit behind the SPA router.
    expect(screen.getByRole("link", { name: "Terms" })).toHaveProperty(
      "pathname",
      "/legal/terms",
    );
    expect(screen.getByRole("link", { name: "Privacy" })).toHaveProperty(
      "pathname",
      "/legal/privacy",
    );
  });
});

// §19/§11, and now the markup exists — so the gate has to be the CAPABILITY
// rather than the absence of a component. Both directions, because only ever
// testing the empty case is what let the block go unbuilt for so long.
/**
 * The text that NAMES a provider button.
 *
 * A button carrying the phone layout's short brand word has two label spans: an
 * `.sr-only` copy of the served label, which is what assistive tech reads, and an
 * `aria-hidden` visible one. A button whose served label has no recognised brand
 * word has a single span and no `.sr-only` copy. Reading whichever exists is how
 * these tests assert the name without depending on which layout the button was
 * rendered for.
 */
function nameSource(button: HTMLElement): string | undefined {
  const name =
    button.querySelector(".sr-only") ??
    button.querySelector(".auth-social-label");
  return name?.textContent ?? undefined;
}

describe("federated sign-in", () => {
  it("offers a provider only when the installation serves one", async () => {
    stubApi({ password: true, password_reset: true }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    await screen.findByLabelText("Email");
    expect(
      screen.queryByRole("button", { name: "Continue with Google" }),
    ).toBeNull();
    expect(screen.queryByText("or with email")).toBeNull();
    cleanup();

    stubApi(
      {
        password: true,
        password_reset: true,
        oidc_providers: [
          { key: "google", label: "Continue with Google" },
          { key: "microsoft", label: "Continue with Microsoft" },
        ],
      },
      () => ok(200),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);
    expect(
      await screen.findByRole("button", { name: "Continue with Google" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "Continue with Microsoft" }),
    ).toBeTruthy();
    // The divider labels the PASSWORD path below it, not the buttons above.
    expect(screen.getByText("or with email")).toBeTruthy();
  });

  // The UI-preview switch (app/ui-preview.ts), on the screen rather than on the
  // pure function. Both positions, and the OFF one is the assertion that matters:
  // every other case in this file runs with the var unset, so the default is
  // pinned by the whole suite — this pair pins that the switch is what changes it
  // and that nothing else does.
  it("draws the federated block on the real empty capability only under the UI-preview switch", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    stubApi({ password: true, password_reset: true }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    await screen.findByLabelText("Email");
    expect(
      screen.queryByRole("button", { name: "Continue with Google" }),
    ).toBeNull();
    cleanup();

    vi.stubEnv("VITE_UI_PREVIEW_OIDC", "1");
    // Same stub, same empty `oidc_providers` the running server serves — the
    // override is presentation, so the wire is identical in both halves.
    stubApi({ password: true, password_reset: true }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    const google = await screen.findByRole<HTMLButtonElement>("button", {
      name: "Continue with Google",
    });
    expect(google.disabled).toBe(false);
    // The same switch marks the SECOND provider not-yet-available, so the preview
    // shows both halves of the design rather than two identical buttons.
    const microsoft = screen.getByRole<HTMLButtonElement>("button", {
      name: "Continue with Microsoft",
    });
    expect(microsoft.disabled).toBe(true);
    expect(microsoft.classList.contains("is-unavailable")).toBe(true);
    // The preview draws the REAL buttons: clicking one leaves for the real
    // start endpoint. Against an installation that configured no provider that
    // endpoint answers 404 — an honest outcome the switch does not dress up.
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    const calls = stubApi({ password: true, password_reset: true }, () =>
      ok(200),
    );
    await userEvent.click(google);
    // A navigation, never an XHR: the flow's whole point is leaving this origin.
    expect(calls).toEqual([]);
    expect(assign).toHaveBeenCalledWith("/v1/auth/oidc/google/start");
  });

  it("hands the browser to the server-owned start endpoint, encoding the served key", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    const calls = stubApi(
      {
        password: true,
        password_reset: true,
        // A key with a character that must not land raw in a path. A real
        // installation's key is `google`; the encoding is what stops the
        // server's answer from being read as path structure.
        oidc_providers: [{ key: "corp/sso", label: "Anmeldung über Werk-IT" }],
      },
      () => ok(200),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.click(
      await screen.findByRole("button", { name: "Anmeldung über Werk-IT" }),
    );
    expect(assign).toHaveBeenCalledWith("/v1/auth/oidc/corp%2Fsso/start");
    expect(calls).toEqual([]);
  });

  // The return half: the callback always redirects (crm.yaml: completeOidcLogin),
  // so every failure arrives as one bounded code on the login screen.
  it("renders a returned sso_error, scrubs it from the URL, and clears it on the next attempt", async () => {
    const replaceState = vi.fn();
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/",
      search: "?sso_error=not_linked",
      hash: "",
      origin: "http://localhost",
    });
    vi.stubGlobal("history", { ...window.history, replaceState });
    stubApi({ password: true, password_reset: true }, () =>
      ok(401, { title: "unauthorized", detail: "invalid email or password" }),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("That account can't sign in here");
    // Scrubbed, so a reload is a fresh sign-in screen rather than the same
    // refusal again.
    expect(replaceState).toHaveBeenCalledWith(null, "", "/");

    // The next attempt replaces the message rather than stacking beside it.
    await userEvent.type(screen.getByLabelText("Email"), "ada@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "hunter2{enter}");
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "We couldn't sign you in",
      ),
    );
  });

  it("ignores an sso_error code outside the contract's vocabulary", async () => {
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/",
      // The query string is attacker-supplied. An unknown code must draw
      // nothing — echoing it would let a link put chosen text on this screen.
      search: "?sso_error=<script>Call+this+number",
      hash: "",
      origin: "http://localhost",
    });
    stubApi({ password: true, password_reset: true }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);

    await screen.findByLabelText("Email");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  // The product path, asserted as a property rather than assumed. A real server
  // can never mark a provider — `oidc_providers[]` items are `{ key, label }` with
  // no availability field — so on the shipped surface every button an
  // installation serves is live and unannotated. This is the case that fails if
  // the preview marker ever leaks into the default render.
  it("leaves every served provider enabled and unannotated, with no unavailable set", async () => {
    stubApi(
      {
        password: true,
        password_reset: true,
        oidc_providers: [
          { key: "google", label: "Continue with Google" },
          { key: "microsoft", label: "Continue with Microsoft" },
        ],
      },
      () => ok(200),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    for (const label of ["Continue with Google", "Continue with Microsoft"]) {
      const button = await screen.findByRole<HTMLButtonElement>("button", {
        name: label,
      });
      expect(button.disabled).toBe(false);
      // The accessible name is the server's label and nothing else. The role
      // query above already proves it — `name` matches the COMPUTED name, which
      // skips the `aria-hidden` copy. What is left to pin is the other half of
      // the same promise: no words of ours reach that name, and the short brand
      // word the phone layout shows is always the installation's own substring.
      expect(nameSource(button)).toBe(label);
      const brand = button.querySelector(".auth-social-brand")?.textContent;
      if (brand) {
        expect(label).toContain(brand);
      }
    }
    expect(document.querySelector(".is-unavailable")).toBeNull();
  });

  // The preview marker (app/ui-preview.ts), on the component that renders it.
  // Passing the set explicitly rather than through the env switch is deliberate:
  // this case is about what the MARKUP does with a marked key, and the switch is
  // pinned where it lives.
  it("renders a marked provider as disabled without touching its label", async () => {
    render(
      <ProviderButtons
        providers={[
          { key: "google", label: "Continue with Google" },
          { key: "microsoft", label: "Continue with Microsoft" },
        ]}
        unavailable={new Set(["microsoft"])}
        onSelect={vi.fn()}
      />,
    );

    // The state is `disabled` plus a class the stylesheet draws, and the
    // accessible name is left as the installation's own string. That is the
    // assertion worth pinning: the marker must not append copy to somebody
    // else's label, so an unrecognised provider on a real installation could
    // never have words we wrote spliced onto the words they wrote.
    const microsoft = await screen.findByRole<HTMLButtonElement>("button", {
      name: "Continue with Microsoft",
    });
    expect(microsoft.disabled).toBe(true);
    expect(microsoft.classList.contains("is-unavailable")).toBe(true);
    // What names the button, not its raw text: the phone layout's short brand
    // word is `aria-hidden` beside an `.sr-only` copy of the served label. What
    // must never happen is a word of OURS reaching the name.
    expect(nameSource(microsoft)).toBe("Continue with Microsoft");

    // Only the marked one. The other provider is offered exactly as it would be
    // on an installation that serves it.
    const google = screen.getByRole<HTMLButtonElement>("button", {
      name: "Continue with Google",
    });
    expect(google.disabled).toBe(false);
  });

  it("renders nothing at all for an empty capability", () => {
    const { container } = render(
      <ProviderButtons providers={[]} onSelect={vi.fn()} />,
    );
    expect(container.textContent).toBe("");
  });

  // The label is the installation's string. A frontend that composed it from the
  // key would render "Continue with corp-sso" for a provider it does not know,
  // and the button still has to work for that provider — which is why the mark
  // falls back to a neutral icon rather than the block disappearing.
  it("renders an unrecognised provider with its own label and reports its key", async () => {
    const chosen: string[] = [];
    render(
      <ProviderButtons
        providers={[{ key: "corp-sso", label: "Anmeldung über Werk-IT" }]}
        onSelect={(key) => chosen.push(key)}
      />,
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Anmeldung über Werk-IT" }),
    );
    expect(chosen).toEqual(["corp-sso"]);
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
      screen.getByLabelText("Email"),
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
    expect(screen.queryByLabelText("Email")).toBeNull();
  });
});

describe("password-disabled installation", () => {
  // §3.3: the screen renders exactly the methods that work. A server that
  // refuses /auth/login must not be offered a password — the form would be an
  // invitation it will not honour.
  it("offers only the provider when the capability says password is off", async () => {
    stubApi(
      {
        password: false,
        password_reset: false,
        oidc_providers: [{ key: "google", label: "Continue with Google" }],
      },
      () => ok(200),
    );
    render(<AuthScreen onAuthed={vi.fn()} />);

    expect(
      await screen.findByRole("button", { name: "Continue with Google" }),
    ).toBeTruthy();
    expect(screen.queryByLabelText("Email")).toBeNull();
    expect(screen.queryByLabelText("Password")).toBeNull();
    expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
    // The divider labels the password path below it, so with no path below
    // there is nothing for it to separate.
    expect(screen.queryByText("or with email")).toBeNull();
  });

  // The honest default for a probe that has not answered yet, or failed: the
  // password form is the baseline method, and hiding it on a transient read
  // would lock everyone out of a working installation.
  it("keeps the password form when the capability is absent", async () => {
    stubApi({ password: true, password_reset: false }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    expect(await screen.findByLabelText("Email")).toBeTruthy();
    expect(screen.getByLabelText("Password")).toBeTruthy();
  });
});
