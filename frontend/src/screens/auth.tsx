import { useMutation, useQuery } from "@tanstack/react-query";
import { Eye, EyeOff, Lock, Mail } from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  previewedOidcProviders,
  previewedPasswordReset,
  previewedUnavailableProviders,
} from "../app/ui-preview";
import wordmarkDark from "../assets/wordmark-dark.png";
import wordmarkWhite from "../assets/wordmark-white.png";
import { Button } from "../design-system/atoms";
import {
  ProviderMark,
  providerBrandName,
} from "../design-system/provider-mark";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { AuthExperience, type AuthPhase, PhoneDisclosure } from "./auth-core";
import { problemMessage } from "./common";
import "./auth.css";

// The default unauthenticated screen is LOGIN, not setup or signup
// (A107/ADR-0061): one installation serves one organization, provisioned at
// API boot from the deployment file — the browser never creates a tenant and
// never selects one. The Margince Core introduces the AI beside a real <form>
// (Enter submits), and only the authentication methods the capabilities probe
// reports as operational render: the forgot-password flow appears exactly when
// the server can complete it.

// AuthNotice is the boundary's transient context for the login screen: a
// deliberate sign-out or an expired session — informational, never danger
// styling (§9.5: the user has nothing to correct).
export type AuthNotice = "signed-out" | "session-expired" | null;

// The installation's operational federated providers, exactly as
// /auth/capabilities serves them. `label` is the SERVER's string — the contract
// documents it as the button text, so the installation owns the wording and t()
// is not involved. Only the MARK is ours to choose, from `key`.
export type OidcProviders =
  components["schemas"]["AuthCapabilities"]["oidc_providers"];

// The product's answer to "which providers are unavailable": none. A module-level
// constant rather than an inline `new Set()` default, so every render of the
// federated block compares equal instead of allocating a fresh set.
const NO_UNAVAILABLE_PROVIDERS: ReadonlySet<string> = new Set();

type View =
  | { kind: "login" }
  | { kind: "forgot" }
  | { kind: "forgot-sent"; email: string }
  | { kind: "reset"; token: string }
  | { kind: "reset-done" };

/**
 * The hash route the emailed reset link lands on. Minted by identity/reset.go.
 * Exported so App.tsx can route this entry through the unauthenticated auth
 * flow even when a session cookie is already live — an existing session must
 * not hide the reset form behind the authenticated shell.
 */
export const RESET_ROUTE = "reset-password";

// resetTokenFromLocation reads the emailed deep link
// (/#/reset-password?token=…): the unauthenticated gate renders this screen
// wherever the link lands, so no route table entry is needed. The token is a
// live single-use credential, so it is scrubbed from the address bar (and
// browser history) the moment it is read — it lives on only in component state.
function resetTokenFromLocation(): string | null {
  if (typeof globalThis.location === "undefined") {
    return null;
  }
  // The FRAGMENT, not the query string, and the whole point is that a fragment
  // never leaves the browser: it is not sent to a server, so it cannot land in
  // an access log or in a Referer header, and it is not part of a Cache Storage
  // key, so the service worker cannot persist it to disk. The scrub below is
  // still worth doing — it keeps the token out of the session's history entry —
  // but it used to be the ONLY defence, and it ran after the first same-origin
  // API call had already carried the token off the page.
  //
  // Parsed as the app's own hash route (`#/reset-password?token=…`) so the
  // emailed link is a normal client route: `parseHash` strips a hash-local query
  // before deriving the screen name, and any static host serves index.html for
  // `/` without an SPA fallback.
  const hash = globalThis.location.hash.replace(/^#\/?/, "");
  const [route, query] = [
    hash.split("?")[0],
    hash.slice(hash.indexOf("?") + 1),
  ];
  if (route !== RESET_ROUTE || !hash.includes("?")) {
    return null;
  }
  const token = new URLSearchParams(query).get("token");
  if (token) {
    // replaceState, not a hash assignment: assigning to location.hash fires
    // hashchange and pushes an entry, which would put the token back in history
    // — the exact thing this line exists to prevent.
    // The QUERY is preserved. The token lives in the fragment, so nothing here
    // needs the query gone — and rewriting it away threw out whatever else the
    // URL was carrying. A reset link has no query of its own, so in the product
    // this was invisible; in Storybook it silently discarded `?id=<story>` and a
    // reload of the canvas landed on no story at all. A scrub should remove the
    // one thing it is for.
    globalThis.history?.replaceState?.(
      null,
      "",
      `${globalThis.location.pathname}${globalThis.location.search}#/${RESET_ROUTE}`,
    );
  }
  return token;
}

// clearResetHash drops a lingering `#/reset-password` from the address bar
// once the reset entry is done with — the token itself is already scrubbed
// by resetTokenFromLocation, but the bare route survives until this runs.
// Left in place, it would make LoginForm's "restore the originally requested
// route" check see a non-empty hash and skip the post-login redirect to
// home, stranding a completed reset on a screen this app never routes to.
// Guarded on the route so it is safe to call from every "back to login" exit,
// including the ones that never touched the reset flow.
function clearResetHash(): void {
  if (typeof globalThis.location === "undefined") {
    return;
  }
  const hash = globalThis.location.hash.replace(/^#\/?/, "").split("?")[0];
  if (hash === RESET_ROUTE) {
    globalThis.history?.replaceState?.(
      null,
      "",
      `${globalThis.location.pathname}${globalThis.location.search}`,
    );
  }
}

export function AuthScreen({
  onAuthed,
  notice = null,
}: Readonly<{ onAuthed: () => void | Promise<void>; notice?: AuthNotice }>) {
  const t = useT();
  const [view, setView] = useState<View>(() => {
    const token = resetTokenFromLocation();
    return token ? { kind: "reset", token } : { kind: "login" };
  });
  const [authPhase, setAuthPhase] = useState<AuthPhase>("idle");
  usePageTitle(t("auth.pageTitle"));

  // The anonymous capability probe drives what the screen offers — a dead
  // "Forgot password?" link is a misleading affordance, so it renders only
  // when the reset flow can complete end to end.
  const capabilities = useQuery({
    queryKey: ["auth-capabilities"],
    queryFn: async () => {
      const { data, error } = await api.GET("/auth/capabilities");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    staleTime: 60_000,
    retry: 1,
  });
  // The capability's real value, passed through the ONE ui-preview override site
  // for the reset link. Off by default, in which case this is the identity
  // function on the server's own answer.
  const resetAvailable = previewedPasswordReset(
    capabilities.data?.password_reset === true,
  );

  // This query is presentation-only and deliberately independent of auth:
  // profile latency or failure hides the live runtime line but can never
  // disable or delay the credential form.
  const assistantProfile = useQuery({
    queryKey: ["assistant-profile"],
    queryFn: async () => {
      const { data, error } = await api.GET("/assistant/profile");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });

  const setLoginView = () => {
    setAuthPhase("idle");
    setView({ kind: "login" });
    clearResetHash();
  };

  return (
    <AuthExperience
      profile={assistantProfile.data}
      phase={view.kind === "login" ? authPhase : "quiet"}
    >
      <Wordmark alt={t("auth.title")} />
      <PhoneDisclosure />
      {view.kind === "login" && (
        <>
          {notice && (
            <p className="auth-notice" role="status">
              {t(
                notice === "signed-out"
                  ? "auth.noticeSignedOut"
                  : "auth.noticeSessionExpired",
              )}
            </p>
          )}
          <LoginForm
            onAuthed={onAuthed}
            onPhase={setAuthPhase}
            resetAvailable={resetAvailable}
            /* The server's answer, passed through the ONE ui-preview override
               site. Off by default, in which case this is the identity
               function and the capability's real value reaches the form
               verbatim — the label callback included, which is never consulted
               on a served provider. */
            providers={previewedOidcProviders(
              capabilities.data?.oidc_providers ?? [],
              (providerKey) =>
                t("auth.continueWith", {
                  /* The key itself if we have no brand word for it: a preview
                     that invents a company's name is worse than one that shows
                     the operator's own identifier. */
                  brand: providerBrandName(providerKey) ?? providerKey,
                }),
            )}
            /* Empty in the product, always: the capability carries no
               availability field, so only the preview layer can mark a provider
               (app/ui-preview.ts). */
            unavailableProviders={previewedUnavailableProviders()}
            onForgot={() => setView({ kind: "forgot" })}
          />
        </>
      )}
      {view.kind === "forgot" && (
        <ForgotForm
          onSent={(email) => setView({ kind: "forgot-sent", email })}
          onBack={setLoginView}
        />
      )}
      {view.kind === "forgot-sent" && (
        <Notice
          title={t("auth.forgotSentTitle")}
          body={t("auth.forgotSentBody")}
          action={t("auth.backToLogin")}
          onAction={setLoginView}
        />
      )}
      {view.kind === "reset" && (
        <ResetForm
          token={view.token}
          onDone={() => setView({ kind: "reset-done" })}
          onRestart={() => setView({ kind: "forgot" })}
        />
      )}
      {view.kind === "reset-done" && (
        <Notice
          title={t("auth.resetDoneTitle")}
          body={t("auth.resetDoneBody")}
          action={t("auth.backToLogin")}
          onAction={setLoginView}
        />
      )}
      <LocaleFooter />
    </AuthExperience>
  );
}

// AvailabilityScreen is the boundary's non-authentication half (§4): the
// API cannot be reached (network / 5xx) or the installation is not ready
// (503 — pre-bootstrap, or a violated singleton invariant). A server
// outage must never read as "wrong password".
export function AvailabilityScreen({
  kind,
  onRetry,
}: Readonly<{ kind: "connection" | "installation"; onRetry: () => void }>) {
  const t = useT();
  usePageTitle(t("auth.pageTitle"));
  return (
    <AuthExperience phase="unavailable">
      <Wordmark alt={t("auth.title")} />
      <PhoneDisclosure />
      <section className="auth-card" role="alert">
        <h1>
          {t(
            kind === "connection"
              ? "auth.connectionTitle"
              : "auth.unavailableTitle",
          )}
        </h1>
        <p className="card-sub">
          {t(
            kind === "connection"
              ? "auth.connectionBody"
              : "auth.unavailableBody",
          )}
        </p>
        <div className="auth-actions">
          <Button variant="primary" onClick={onRetry}>
            {t("auth.retry")}
          </Button>
        </div>
      </section>
    </AuthExperience>
  );
}

// Wordmark renders the current Margince logo. Two source images (dark ink for
// the light theme, white for dark) swap via CSS on the data-theme toggle — no
// JS theme read needed. Shared with onboarding, which sizes it for its top bar
// through className: the product has ONE wordmark, and a screen that draws its
// own drifts the moment the real one changes.
export function Wordmark({
  alt,
  className = "auth-wordmark",
}: Readonly<{ alt: string; className?: string }>) {
  // The container carries the ONE accessible name: the theme swap hides
  // one <img> with display:none, so a name on either image alone would
  // vanish in the other theme.
  return (
    <span className={className} role="img" aria-label={alt}>
      <img className="auth-wordmark-light" src={wordmarkDark} alt="" />
      <img className="auth-wordmark-dark" src={wordmarkWhite} alt="" />
    </span>
  );
}

// usePageTitle stamps the document title for the unauthenticated surface
// (§7.1) and restores the product name on unmount.
function usePageTitle(title: string) {
  useEffect(() => {
    const previous = document.title;
    document.title = title;
    return () => {
      document.title = previous;
    };
  }, [title]);
}

// LocaleFooter is the one footer utility that actually works today (§3.3
// honesty: no Privacy/Help links exist yet, so none render). Language
// names are proper nouns, deliberately not translated.
function LocaleFooter() {
  const t = useT();
  const { locale, setLocale } = useLocale();
  return (
    <div className="auth-footer">
      <button
        type="button"
        className="auth-link"
        aria-pressed={locale === "de"}
        onClick={() => setLocale("de")}
      >
        {t("auth.langDeutsch")}
      </button>
      <span aria-hidden>·</span>
      <button
        type="button"
        className="auth-link"
        aria-pressed={locale === "en"}
        onClick={() => setLocale("en")}
      >
        {t("auth.langEnglish")}
      </button>
    </div>
  );
}

// loginFailureKind maps the login response status onto its UX state (§9):
// one non-enumerating message for bad credentials, an actionable one for
// rate limiting, and connectivity presented as connectivity — never parsed
// from human-readable detail strings.
type LoginFailure = "credentials" | "rate-limited" | "unreachable";

class LoginError extends Error {
  readonly failure: LoginFailure;
  constructor(failure: LoginFailure) {
    super(failure);
    this.name = "LoginError";
    this.failure = failure;
  }
}

function loginErrorKey(error: unknown): MessageKey {
  const failure = error instanceof LoginError ? error.failure : "unreachable";
  if (failure === "credentials") return "auth.errCredentials";
  if (failure === "rate-limited") return "auth.errRateLimited";
  return "auth.errUnreachable";
}

/**
 * Federated sign-in, above the password form (§11).
 *
 * Placement is an argument, not a preference: if the installation runs SSO the
 * password form is the FALLBACK path, and putting it first tells every user at
 * that installation to take the slower door. Hence the divider below, which
 * labels the form rather than the buttons.
 *
 * **Renders nothing when the capability is empty**, and that is the §19
 * enforcement point rather than a convenience: `oidc_providers` is served by
 * `/auth/capabilities`, so a control for a flow this installation cannot
 * complete never reaches the screen. This build's server serves `[]` until the
 * OIDC flow ships, which is why no provider button appears at runtime today. Do
 * not "fix" this to render a disabled button, and do not seed a provider list
 * into the capability response — the empty list IS the gate, and this component
 * must keep asking only "did I get providers?".
 *
 * The one thing that may put providers here without a server is
 * `app/ui-preview.ts`, and it is not an exception to the above: it substitutes
 * at the CALL SITE in `AuthScreen`, off unless `VITE_UI_PREVIEW_OIDC` is set at
 * build time, and it draws the block without making the flow work. This
 * component cannot tell the difference and must not try to.
 *
 * **The label is the server's string, not ours.** The contract types it as
 * `{ key, label }` and documents `label` as the button text, so the installation
 * owns the wording and `t()` is not involved. The consequence is real: a German
 * reader sees the installation's English label. Only the MARK is ours to choose,
 * from `key`.
 */
export function ProviderButtons({
  providers,
  disabled = false,
  unavailable = NO_UNAVAILABLE_PROVIDERS,
  onSelect,
}: Readonly<{
  providers: OidcProviders;
  disabled?: boolean;
  /**
   * Provider keys to render as not-yet-available. **Empty in the product**, and
   * structurally so: the capability's items are `{ key, label }` with no
   * availability field, so nothing on the wire can populate this — only
   * `app/ui-preview.ts` can, for a design review, and §3.3 keeps a dead provider
   * control illegal on the shipped surface. This component never infers
   * availability from a key: a provider it has no logo for is still a working one.
   */
  unavailable?: ReadonlySet<string>;
  onSelect: (providerKey: string) => void;
}>) {
  const t = useT();
  if (providers.length === 0) {
    return null;
  }
  return (
    <>
      <div className="auth-sso">
        {providers.map((provider) => {
          const isUnavailable = unavailable.has(provider.key);
          return (
            <button
              key={provider.key}
              type="button"
              /* A class rather than `:disabled` alone, because `disabled` is
                 also how the form marks every provider while a sign-in is in
                 flight, and the two want opposite treatments: in-flight is
                 momentary and the control is coming back, this is a resting
                 state. One selector for both tunes each at the other's expense.
                 The button carries no appended copy, so its accessible name
                 stays the installation's own label. */
              className={
                isUnavailable ? "auth-social is-unavailable" : "auth-social"
              }
              disabled={disabled || isUnavailable}
              onClick={() => onSelect(provider.key)}
            >
              <ProviderMark providerKey={provider.key} />
              {/* Two labels, and which one SHOWS is the stylesheet's business.
                  The served label is the installation's own string and is what
                  the button is called: it is the accessible name at every width.
                  The brand word is the short form for a key we recognise, so a
                  phone can show "Google" side by side instead of wrapping
                  "Continue with Google" over three lines. An unrecognised key has
                  no brand word and falls back to the served label, which is then
                  the only text present and needs no second copy. Either way the
                  button appends nothing of its own, so the accessible name stays
                  the installation's label — including for an unavailable one. */}
              <ProviderLabel
                label={provider.label}
                providerKey={provider.key}
              />
            </button>
          );
        })}
      </div>
      {/* Labels the path BELOW it, so a screen reader hears what the divider
          separates rather than a decorative rule. */}
      <p className="auth-or">
        <span>{t("auth.orWithEmail")}</span>
      </p>
    </>
  );
}

/**
 * The two forms of a provider's name, with the accessible name pinned to the
 * server's.
 *
 * When a brand word exists, the served label goes into an `.sr-only` span and the
 * visible text is `aria-hidden`, so the button announces the installation's own
 * words however narrow the layout gets. When it does not, there is one span and
 * one string — a duplicate that says the same thing twice would be read twice.
 */
function ProviderLabel({
  label,
  providerKey,
}: Readonly<{ label: string; providerKey: string }>) {
  const brand = providerBrandName(providerKey);
  // The short form is used ONLY when the served label already contains it.
  // WCAG 2.2 SC 2.5.3 (Label in Name) wants the accessible name to contain the
  // visible text, and an installation is free to label its `google` provider
  // "Firmen-Login" — showing "Google" there would both break that and put a
  // brand claim on screen that the operator never made.
  if (!brand || !label.toLowerCase().includes(brand.toLowerCase())) {
    return <span className="auth-social-label">{label}</span>;
  }
  return (
    <>
      <span className="sr-only">{label}</span>
      <span className="auth-social-label" aria-hidden>
        <span className="auth-social-full">{label}</span>
        <span className="auth-social-brand">{brand}</span>
      </span>
    </>
  );
}

// startFederatedSignIn is the hand-off itself, and today it is deliberately
// inert.
//
// The real flow is a full-page redirect to the provider and back to a callback
// route. crm.yaml documents NEITHER path — `AuthCapabilities.oidc_providers`
// ("empty until the OIDC flow ships") is the only OIDC thing in the contract —
// so there is no endpoint to send the browser to, and composing a start URL out
// of the provider key would be inventing a wire this build cannot honour.
//
// No USER reaches it in this build, and that is the honest part rather than a
// loose end: the server serves `oidc_providers: []`, so `ProviderButtons`
// renders nothing and no user can meet this (§19). The seeded story, the seeded
// e2e case and the `VITE_UI_PREVIEW_OIDC` preview build are review fixtures for
// the DESIGN — the preview draws the buttons and they land here, which is to say
// they do nothing, deliberately and visibly. When the flow ships, this function
// is the one thing that changes — the markup, the copy and the capability gate
// are already right.
function startFederatedSignIn(_providerKey: string): void {}

function LoginForm({
  onAuthed,
  onPhase,
  resetAvailable,
  providers,
  unavailableProviders,
  onForgot,
}: Readonly<{
  onAuthed: () => void | Promise<void>;
  onPhase: (phase: AuthPhase) => void;
  resetAvailable: boolean;
  /** §11: served by /auth/capabilities. Empty means no federated block. */
  providers: OidcProviders;
  /** Preview-only; empty in the product. See `ProviderButtons`. */
  unavailableProviders: ReadonlySet<string>;
  onForgot: () => void;
}>) {
  const t = useT();
  const emailId = useId();
  const passwordId = useId();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [capsLock, setCapsLock] = useState(false);
  const emailRef = useRef<HTMLInputElement>(null);
  const errorRef = useRef<HTMLDivElement>(null);

  // Focus lands on email at render (§8.2) — programmatic rather than the
  // autoFocus attribute, so the a11y lint's blanket rule stays intact and
  // the login page keeps the one justified exception.
  useEffect(() => {
    emailRef.current?.focus();
  }, []);

  const login = useMutation({
    mutationFn: async () => {
      const result = await api
        .POST("/auth/login", { body: { email: email.trim(), password } })
        .catch(() => null);
      if (!result) {
        throw new LoginError("unreachable");
      }
      const { data, error, response } = result;
      if (error) {
        if (response.status === 401) throw new LoginError("credentials");
        if (response.status === 429) throw new LoginError("rate-limited");
        if (response.status >= 500) throw new LoginError("unreachable");
        throw new Error(problemMessage(error));
      }
      // The login response only says the credential exchange succeeded. The
      // session is real when the app's authenticated /me probe accepts the
      // resulting cookie; keep the Core in its signing-in state until then.
      await onAuthed();
      return data;
    },
    onSuccess: () => {
      onPhase("success");
      // Restore the originally requested route (§8.5): a deep link the
      // user followed stays; only a bare entry lands on home.
      const hash = globalThis.location?.hash ?? "";
      if (!hash || hash === "#" || hash === "#/") {
        navigate({ screen: "home" });
      }
    },
    onError: (error) => {
      onPhase("error");
      if (error instanceof LoginError && error.failure === "credentials") {
        // A rejected credential clears the password (§9.2); the email
        // stays for the retry.
        setPassword("");
      }
      // The error summary is announced and receives focus; tab order then
      // leads back into the fields.
      requestAnimationFrame(() => errorRef.current?.focus());
    },
  });

  const ready = email.trim() !== "" && password !== "";
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (ready && !login.isPending) {
      onPhase("signing-in");
      login.mutate();
    }
  };

  return (
    <form className="auth-card" onSubmit={submit}>
      <h1>{t("auth.loginTitle")}</h1>
      <p className="card-sub">{t("auth.loginSub")}</p>
      <ProviderButtons
        providers={providers}
        disabled={login.isPending}
        unavailable={unavailableProviders}
        onSelect={startFederatedSignIn}
      />
      <div className="auth-fields">
        <Field id={emailId} label={t("auth.email")} icon={<Mail aria-hidden />}>
          <input
            id={emailId}
            ref={emailRef}
            className="auth-input"
            type="email"
            required
            /* A stable `name`, because `id` here cannot be one: useId() derives
               its value from the component's position in the tree, and this
               tree changes with the notice and the provider block. Chrome
               autofills from the autocomplete token alone, but Firefox and
               several password managers fall back to name/id to match a SAVED
               credential to a rendered field — with neither stable, they have
               nothing to match on. */
            name="email"
            autoComplete="username"
            inputMode="email"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            placeholder={t("auth.emailPlaceholder")}
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        </Field>
        <Field
          id={passwordId}
          label={t("auth.password")}
          icon={<Lock aria-hidden />}
          labelEnd={
            resetAvailable ? (
              <button type="button" className="auth-link" onClick={onForgot}>
                {t("auth.forgotLink")}
              </button>
            ) : undefined
          }
          hint={capsLock ? t("auth.capsLock") : undefined}
          trailing={
            <PasswordReveal
              shown={showPassword}
              onToggle={() => setShowPassword((shown) => !shown)}
            />
          }
        >
          <input
            id={passwordId}
            className="auth-input"
            type={showPassword ? "text" : "password"}
            required
            name="password"
            autoComplete="current-password"
            autoCapitalize="none"
            spellCheck={false}
            placeholder={t("auth.passwordPlaceholder")}
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            onKeyUp={(event) =>
              setCapsLock(event.getModifierState?.("CapsLock") ?? false)
            }
          />
        </Field>
      </div>
      {login.isError && (
        <div className="auth-error" role="alert" tabIndex={-1} ref={errorRef}>
          <p className="ae-t">{t(loginErrorKey(login.error))}</p>
        </div>
      )}
      <div className="auth-actions">
        {/* Disabled ONLY while a request is in flight (§8.4). An empty field is
            answered by native validation on the inputs, not by a pale control
            with nothing to say. */}
        <Button type="submit" variant="primary" disabled={login.isPending}>
          {login.isPending ? t("auth.signingIn") : t("auth.signIn")}
        </Button>
      </div>
    </form>
  );
}

/**
 * The reveal control. EVERY password field on this surface carries one, and the
 * new-password field has the stronger claim on it: a mistyped credential on
 * sign-in is refused by the server in one round trip, while a mistyped NEW
 * password simply becomes the password — a 12-character minimum with no confirm
 * field to disagree with it. Reading back what you are about to be locked out by
 * is the point.
 *
 * One button with `aria-pressed` rather than two: the name says which way it will
 * go, the state says where it is. `title` as well as `aria-label` because the
 * control is an icon and a sighted mouse user gets no name otherwise.
 */
function PasswordReveal({
  shown,
  onToggle,
}: Readonly<{ shown: boolean; onToggle: () => void }>) {
  const t = useT();
  const label = t(shown ? "auth.hidePassword" : "auth.showPassword");
  return (
    <button
      type="button"
      className="auth-reveal"
      aria-pressed={shown}
      aria-label={label}
      title={label}
      onClick={onToggle}
    >
      {shown ? <EyeOff aria-hidden /> : <Eye aria-hidden />}
    </button>
  );
}

function ForgotForm({
  onSent,
  onBack,
}: Readonly<{ onSent: (email: string) => void; onBack: () => void }>) {
  const t = useT();
  const emailId = useId();
  const [email, setEmail] = useState("");

  const request = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST("/auth/forgot-password", {
        body: { email: email.trim() },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => onSent(email.trim()),
  });

  const ready = email.trim() !== "";
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (ready && !request.isPending) {
      request.mutate();
    }
  };

  return (
    <form className="auth-card" onSubmit={submit}>
      <h1>{t("auth.forgotTitle")}</h1>
      <p className="card-sub">{t("auth.forgotSub")}</p>
      <div className="auth-fields">
        {/* Same icon as the sign-in card's email field. Without it the text
            starts 22px further left than on the screen the user just came from,
            and the two cards stop looking like one surface. */}
        <Field id={emailId} label={t("auth.email")} icon={<Mail aria-hidden />}>
          <input
            id={emailId}
            className="auth-input"
            type="email"
            required
            name="email"
            /* "email", not "username": this form never accepts a password, and
               labelling it username invites a manager to treat it as a sign-in
               field and offer to fill a credential that has nowhere to go. */
            autoComplete="email"
            inputMode="email"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            placeholder={t("auth.emailPlaceholder")}
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        </Field>
      </div>
      {request.isError && (
        <ErrorNote
          message={
            request.error instanceof Error ? request.error.message : null
          }
        />
      )}
      <div className="auth-actions">
        <Button
          type="submit"
          variant="primary"
          disabled={!ready || request.isPending}
        >
          {t("auth.sendResetLink")}
        </Button>
        <button type="button" className="auth-link" onClick={onBack}>
          {t("auth.backToLogin")}
        </button>
      </div>
    </form>
  );
}

const MIN_PASSWORD = 12;

/*
 * Why a reset failure has to be classified at all.
 *
 * Every failure used to render one string: "That reset link is invalid, used, or
 * expired", above a "Request a new link" button. For a spent token that is
 * exactly right. For anything else it is false, and the offered remedy is
 * actively harmful — minting a new link SUPERSEDES the outstanding token
 * (identity/reset.go), so a user whose request merely hit a network blip is told
 * their good link is dead and then invited to destroy it.
 *
 * `token` is the only failure where a new link is the answer. A refused password
 * belongs to the field, a budget refusal is a wait, and a server or transport
 * fault is neither the link's fault nor the user's.
 */
type ResetFailure = "token" | "password" | "rate-limited" | "server";

class ResetError extends Error {
  readonly failure: ResetFailure;
  constructor(failure: ResetFailure) {
    super(failure);
    this.name = "ResetError";
    this.failure = failure;
  }
}

function resetFailureOf(status: number | undefined): ResetFailure {
  // 401 is the ONLY token verdict, and it is deliberately one verdict: the
  // server refuses to distinguish unknown from used from expired so a token
  // cannot be probed. That is why the copy names all three.
  if (status === 401) return "token";
  if (status === 422) return "password";
  if (status === 429) return "rate-limited";
  return "server";
}

function resetErrorKey(failure: ResetFailure): MessageKey {
  if (failure === "token") return "auth.resetFailed";
  if (failure === "password") return "auth.resetRejectedPassword";
  // NOT auth.errRateLimited: that one says "sign-in attempts", and this user is
  // setting a password. Copy that names the wrong action reads as the wrong error.
  if (failure === "rate-limited") return "auth.resetRateLimited";
  return "auth.resetServerFailed";
}

function ResetForm({
  token,
  onDone,
  onRestart,
}: Readonly<{ token: string; onDone: () => void; onRestart: () => void }>) {
  const t = useT();
  const passwordId = useId();
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);

  const reset = useMutation({
    mutationFn: async () => {
      // `.catch(() => null)` for the same reason LoginForm does it: a rejected
      // fetch (offline, DNS, CORS) is not an HTTP error and arrives as a thrown
      // TypeError. Without this, a transport fault was indistinguishable from a
      // dead token — and the remedy offered for a dead token destroys a live one.
      const result = await api
        .POST("/auth/reset-password", {
          body: { token, new_password: password },
        })
        .catch(() => null);
      if (!result) {
        throw new ResetError("server");
      }
      if (result.error) {
        throw new ResetError(resetFailureOf(result.response?.status));
      }
    },
    onSuccess: onDone,
  });
  const failure =
    reset.error instanceof ResetError ? reset.error.failure : "server";

  const ready = password.length >= MIN_PASSWORD;
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (ready && !reset.isPending) {
      reset.mutate();
    }
  };

  return (
    <form className="auth-card" onSubmit={submit}>
      <h1>{t("auth.resetTitle")}</h1>
      <p className="card-sub">{t("auth.resetSub")}</p>
      <div className="auth-fields">
        <Field
          id={passwordId}
          label={t("auth.newPassword")}
          icon={<Lock aria-hidden />}
          hint={t("auth.passwordHint")}
          trailing={
            <PasswordReveal
              shown={showPassword}
              onToggle={() => setShowPassword((shown) => !shown)}
            />
          }
        >
          <input
            id={passwordId}
            className="auth-input"
            type={showPassword ? "text" : "password"}
            required
            minLength={MIN_PASSWORD}
            name="new-password"
            autoComplete="new-password"
            autoCapitalize="none"
            spellCheck={false}
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
      </div>
      {reset.isError && (
        <div className="auth-error" role="alert">
          <p className="ae-t">{t(resetErrorKey(failure))}</p>
          {/* The new-link offer appears ONLY for a token verdict. Everywhere else
              it is the one action that makes things worse: it invalidates the
              token the user is still holding, which for a network blip or a
              refused password is a working link thrown away. */}
          {failure === "token" && (
            <button type="button" className="auth-link" onClick={onRestart}>
              {t("auth.requestNewLink")}
            </button>
          )}
        </div>
      )}
      <div className="auth-actions">
        <Button
          type="submit"
          variant="primary"
          disabled={!ready || reset.isPending}
        >
          {t("auth.setNewPassword")}
        </Button>
      </div>
    </form>
  );
}

function Notice({
  title,
  body,
  action,
  onAction,
}: Readonly<{
  title: string;
  body: string;
  action: string;
  onAction: () => void;
}>) {
  return (
    <section className="auth-card">
      <h1>{title}</h1>
      <p className="card-sub">{body}</p>
      <div className="auth-actions">
        <Button variant="primary" onClick={onAction}>
          {action}
        </Button>
      </div>
    </section>
  );
}

function Field({
  id,
  label,
  labelEnd,
  hint,
  icon,
  trailing,
  children,
}: Readonly<{
  id: string;
  label: string;
  labelEnd?: ReactNode;
  hint?: string;
  /** Leading affordance inside the shell. Decorative: the label names the field. */
  icon?: ReactNode;
  /** In-shell control, e.g. the password reveal. */
  trailing?: ReactNode;
  children: ReactNode;
}>) {
  // The <label> names only the label text, so the input's accessible name is
  // exactly the label — the hint is a sibling below the shell, not part of it.
  //
  // The visible label STAYS, and that is a deliberate divergence from the
  // reference artifact, which labels its fields with a placeholder and an
  // aria-label. A placeholder is not a label: it vanishes the moment the field
  // has content, which is WCAG 3.3.2, and ADR-0076 Decision 6 binds §12's WCAG
  // list unamended. Where the picture and §12 disagree, §12 wins.
  return (
    <div className="auth-field">
      <div className="auth-label-row">
        <label htmlFor={id}>{label}</label>
        {labelEnd}
      </div>
      {/* The border and the focus ring live on the shell, not the input, so the
          leading icon sits inside the outline rather than beside it. */}
      <div className="auth-shell">
        {icon && (
          <span className="auth-shell-icon" aria-hidden>
            {icon}
          </span>
        )}
        {children}
        {trailing}
      </div>
      {hint && (
        <span className="auth-hint" role="status">
          {hint}
        </span>
      )}
    </div>
  );
}

function ErrorNote({ message }: Readonly<{ message: string | null }>) {
  const t = useT();
  return (
    <div className="auth-error" role="alert">
      <p className="ae-t">{t("auth.failed")}</p>
      {message && <p className="ae-m">{message}</p>}
    </div>
  );
}
