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
import { THEME_KEY } from "../app/theme";
import { resetTheme } from "../app/theme-reset";
import { LOCALES, LocaleProvider, localeNameKey, translate } from "../i18n";
import { AuthScreen, AvailabilityScreen, ProviderButtons } from "./auth";

// The unauthenticated surface (A107/ADR-0061 §12): login is the default —
// no signup mode, no workspace field, no tenant selector on the wire — and
// the forgot-password flow renders exactly when the capabilities probe
// reports it operational.

const t = (key: Parameters<typeof translate>[1]) => translate("en", key);

// The theme lives in one module-level store, so the case that presses the
// toggle below would otherwise hand every later case a flipped document,
// `localStorage` and store.
beforeEach(resetTheme);

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

  // Derived from LOCALES rather than listed: a hardcoded pair passes while the
  // footer quietly drops the third language, which is the one failure this
  // case exists to catch — the reader who cannot read the screen it is on.
  it("the sign-in footer offers every shipped locale", async () => {
    stubApi({ password: true, password_reset: false }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);

    for (const locale of LOCALES) {
      const name = t(localeNameKey(locale));
      expect(screen.getByRole("button", { name }), name).toBeTruthy();
    }
  });

  // The document declares ONE language (LocaleProvider, WCAG 3.1.1) and this
  // row shows three. Unmarked, a screen reader reads every name with the
  // phonemes of whichever locale is currently on — so the reader who came here
  // to find their own language is read it in a language they may not have.
  it("voices each language name in its own language", async () => {
    stubApi({ password: true, password_reset: false }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);

    for (const locale of LOCALES) {
      const name = t(localeNameKey(locale));
      const button = screen.getByRole("button", { name });
      expect(
        button.querySelector(`[lang="${locale}"]`)?.textContent,
        name,
      ).toBe(name);
    }
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

  // The theme is readable before anyone signs in, so it has to be changeable
  // there too — the toggle used to exist only in the authenticated top bar.
  it("changes the document theme from the legal row", async () => {
    stubApi({ password: true, password_reset: true }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);

    const toggle = await screen.findByRole("button", {
      name: t("theme.toDark"),
    });
    await userEvent.click(toggle);

    expect(document.documentElement.dataset.theme).toBe("dark");
    // The label names the theme the press would move TO, so it has to flip with
    // the press — a stale label sends a reader the wrong way.
    expect(toggle.getAttribute("aria-label")).toBe(t("theme.toLight"));
    expect(window.localStorage.getItem(THEME_KEY)).toBe("dark");
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
    expect(screen.queryByText("or")).toBeNull();
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
    expect(screen.getByText("or")).toBeTruthy();
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
    // Inert, and that is the point of the switch: it draws the design, it does
    // not invent a redirect. Clicking must neither navigate nor hit the wire.
    const calls = stubApi({ password: true, password_reset: true }, () =>
      ok(200),
    );
    await userEvent.click(google);
    expect(calls).toEqual([]);
    expect(google).toBeTruthy();
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

// Every password field on this surface, DERIVED from the rendered form rather
// than named one by one: the autocomplete token is what makes a field a password
// field to a browser and to a password manager, and it survives the reveal (the
// `type` does not). A field this surface grows later is therefore covered by the
// obligation without anyone extending a list — which is the whole point, because
// a missing reveal looks exactly like a field nobody got round to.
async function expectEveryPasswordFieldRevealable() {
  const fields = [
    ...document.querySelectorAll<HTMLInputElement>(
      'input[autocomplete$="-password"]',
    ),
  ];
  expect(fields.length).toBeGreaterThan(0);
  for (const input of fields) {
    const field = input.closest(".auth-field");
    expect(field, input.name).toBeTruthy();
    const reveal = field?.querySelector<HTMLButtonElement>(".auth-reveal");
    expect(reveal, input.name).toBeTruthy();
    expect(input.type, input.name).toBe("password");
    expect(reveal?.getAttribute("aria-label")).toBe("Show password");
    expect(reveal?.getAttribute("aria-pressed")).toBe("false");

    if (reveal) {
      await userEvent.click(reveal);
    }
    expect(input.type, input.name).toBe("text");
    expect(reveal?.getAttribute("aria-label")).toBe("Hide password");
    expect(reveal?.getAttribute("aria-pressed")).toBe("true");

    if (reveal) {
      await userEvent.click(reveal);
    }
    expect(input.type, input.name).toBe("password");
  }
}

describe("password reveal", () => {
  it("covers the sign-in password", async () => {
    stubApi({ password: true, password_reset: false }, () => ok(200));
    render(<AuthScreen onAuthed={vi.fn()} />);
    await screen.findByLabelText("Password");

    await expectEveryPasswordFieldRevealable();
  });

  // The one that was missing, and the worse of the two to get wrong: a mistyped
  // sign-in password is refused by the server, while a mistyped NEW password just
  // becomes the password — there is no confirm field to disagree with it.
  it("covers the new password behind the emailed link", async () => {
    stubApi({ password: true, password_reset: true }, () => ok(204));
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/",
      hash: "#/reset-password?token=reveal-probe-token",
      origin: "http://localhost",
    });
    render(<AuthScreen onAuthed={vi.fn()} />);
    await screen.findByLabelText("New password");

    await expectEveryPasswordFieldRevealable();
  });
});

describe("AuthScreen reset deep link", () => {
  it("redeems the emailed token and lands back at sign-in", async () => {
    const calls = stubApi({ password: true, password_reset: true }, () =>
      ok(204),
    );
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/",
      hash: "#/reset-password?token=raw-reset-token",
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
      pathname: "/",
      hash: "#/reset-password?token=spent-token",
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

  // These three cases exist because one string used to serve every failure, and
  // the remedy it offered — "Request a new link" — SUPERSEDES the token the user
  // is still holding. So for anything that is not a token verdict, the old copy
  // was both wrong and destructive. What each failure must not do is as important
  // as what it says, hence the absence assertions.
  it("names the action the user was actually taking when refused for rate", async () => {
    // The 429 used to render auth.errRateLimited, which says "sign-in attempts"
    // — on a form whose only job is setting a password. Copy that names the wrong
    // action reads as the wrong error, so this branch has its own key and this
    // test is what keeps the two from being collapsed back together.
    stubApi({ password: true, password_reset: true }, () =>
      ok(429, { title: "rate_limited", detail: "budget exceeded" }),
    );
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/",
      hash: "#/reset-password?token=good-token",
      origin: "http://localhost",
    });
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      await screen.findByLabelText("New password"),
      "an entirely new password{enter}",
    );

    expect(
      await screen.findByText(
        "Too many attempts. Wait a moment, then set your password again.",
      ),
    ).toBeTruthy();
    // The link is untouched by a rate limit, so replacing it is still wrong.
    expect(screen.queryByText("Request a new link")).toBeNull();
  });

  it("blames the password, not the link, when the server refuses the password", async () => {
    stubApi({ password: true, password_reset: true }, () =>
      ok(422, { title: "validation_error", detail: "password too weak" }),
    );
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/",
      hash: "#/reset-password?token=good-token",
      origin: "http://localhost",
    });
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      await screen.findByLabelText("New password"),
      "an entirely new password{enter}",
    );

    expect(
      await screen.findByText(
        "That password was refused. Choose a different one and try again.",
      ),
    ).toBeTruthy();
    // The link is still good, so replacing it must not be offered.
    expect(screen.queryByText("Request a new link")).toBeNull();
  });

  it("keeps a good link alive when the request never reaches the server", async () => {
    stubApi({ password: true, password_reset: true }, () => {
      throw new TypeError("Failed to fetch");
    });
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/",
      hash: "#/reset-password?token=good-token",
      origin: "http://localhost",
    });
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      await screen.findByLabelText("New password"),
      "an entirely new password{enter}",
    );

    expect(
      await screen.findByText(
        "We couldn't set your password just now. Your link is still valid, so try again in a moment.",
      ),
    ).toBeTruthy();
    expect(screen.queryByText("Request a new link")).toBeNull();
  });

  it("announces a reset failure to assistive tech", async () => {
    // The reset error was the one error region on this surface with no role, so a
    // screen-reader user submitted an expired token and was told nothing.
    stubApi({ password: true, password_reset: true }, () =>
      ok(401, { title: "unauthorized", detail: "invalid, used, or expired" }),
    );
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/",
      hash: "#/reset-password?token=spent-token",
      origin: "http://localhost",
    });
    render(<AuthScreen onAuthed={vi.fn()} />);

    await userEvent.type(
      await screen.findByLabelText("New password"),
      "an entirely new password{enter}",
    );

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(
      "That reset link is invalid, used, or expired.",
    );
  });

  it("ignores a token in the server-visible query string", async () => {
    // The security property, asserted directly rather than implied by the happy
    // path: a query string is sent to servers, lands in access logs, is attached
    // as a Referer on same-origin api calls, and becomes a Cache Storage key. So
    // the reset view must open ONLY for a fragment token. If someone reverts the
    // parser to location.search to "be more forgiving", this fails.
    stubApi({ password: true, password_reset: true }, () => ok(204));
    vi.stubGlobal("location", {
      ...window.location,
      pathname: "/reset-password",
      search: "?token=raw-reset-token",
      hash: "",
      origin: "http://localhost",
    });
    render(<AuthScreen onAuthed={vi.fn()} />);

    // The sign-in form, not the reset form.
    expect(await screen.findByLabelText("Email")).toBeTruthy();
    expect(screen.queryByLabelText("New password")).toBeNull();
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
