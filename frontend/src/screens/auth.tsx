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
import { AuthExperience, type AuthPhase } from "./auth-core";
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

// resetTokenFromLocation reads the emailed deep link
// (/reset-password?token=…): the SPA serves every path, and the
// unauthenticated gate renders this screen wherever the link lands. The
// token is a live single-use credential, so it is scrubbed from the
// address bar (and browser history) the moment it is read — it lives on
// only in component state.
function resetTokenFromLocation(): string | null {
  if (typeof globalThis.location === "undefined") {
    return null;
  }
  if (!globalThis.location.pathname.endsWith("/reset-password")) {
    return null;
  }
  const token = new URLSearchParams(globalThis.location.search).get("token");
  if (token) {
    globalThis.history?.replaceState?.(null, "", globalThis.location.pathname);
  }
  return token;
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
  };

  return (
    <AuthExperience
      profile={assistantProfile.data}
      phase={view.kind === "login" ? authPhase : "quiet"}
    >
      <Wordmark alt={t("auth.title")} />
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
               verbatim. */
            providers={previewedOidcProviders(
              capabilities.data?.oidc_providers ?? [],
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
            autoComplete="username"
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
            <button
              type="button"
              className="auth-reveal"
              aria-pressed={showPassword}
              aria-label={t(
                showPassword ? "auth.hidePassword" : "auth.showPassword",
              )}
              title={t(
                showPassword ? "auth.hidePassword" : "auth.showPassword",
              )}
              onClick={() => setShowPassword((v) => !v)}
            >
              {showPassword ? <EyeOff aria-hidden /> : <Eye aria-hidden />}
            </button>
          }
        >
          <input
            id={passwordId}
            className="auth-input"
            type={showPassword ? "text" : "password"}
            required
            autoComplete="current-password"
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
            autoComplete="username"
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

function ResetForm({
  token,
  onDone,
  onRestart,
}: Readonly<{ token: string; onDone: () => void; onRestart: () => void }>) {
  const t = useT();
  const passwordId = useId();
  const [password, setPassword] = useState("");

  const reset = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST("/auth/reset-password", {
        body: { token, new_password: password },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: onDone,
  });

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
        >
          <input
            id={passwordId}
            className="auth-input"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
      </div>
      {reset.isError && (
        <div className="auth-error">
          <p className="ae-t">{t("auth.resetFailed")}</p>
          <button type="button" className="auth-link" onClick={onRestart}>
            {t("auth.requestNewLink")}
          </button>
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
