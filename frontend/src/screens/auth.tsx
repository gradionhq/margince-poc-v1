import { useMutation, useQuery } from "@tanstack/react-query";
import { Eye, EyeOff } from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { api } from "../api/client";
import { navigate } from "../app/router";
import { Button } from "../design-system/atoms";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage } from "./common";
import "./auth.css";

// The default unauthenticated screen is LOGIN, not setup or signup
// (A107/ADR-0061): one installation serves one organization, provisioned at
// API boot from the deployment file — the browser never creates a tenant and
// never selects one. A single centered column, a real <form> (Enter submits),
// and only the authentication methods the capabilities probe reports as
// operational: the forgot-password flow renders exactly when the server can
// complete it.

// AuthNotice is the boundary's transient context for the login screen: a
// deliberate sign-out or an expired session — informational, never danger
// styling (§9.5: the user has nothing to correct).
export type AuthNotice = "signed-out" | "session-expired" | null;

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
}: Readonly<{ onAuthed: () => void; notice?: AuthNotice }>) {
  const t = useT();
  const [view, setView] = useState<View>(() => {
    const token = resetTokenFromLocation();
    return token ? { kind: "reset", token } : { kind: "login" };
  });
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
  const resetAvailable = capabilities.data?.password_reset === true;

  return (
    <div className="auth-page">
      <main className="auth-column">
        <span className="auth-wordmark">
          <span className="mk">M</span>
          {t("auth.title")}
        </span>
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
              resetAvailable={resetAvailable}
              onForgot={() => setView({ kind: "forgot" })}
            />
          </>
        )}
        {view.kind === "forgot" && (
          <ForgotForm
            onSent={(email) => setView({ kind: "forgot-sent", email })}
            onBack={() => setView({ kind: "login" })}
          />
        )}
        {view.kind === "forgot-sent" && (
          <Notice
            title={t("auth.forgotSentTitle")}
            body={t("auth.forgotSentBody")}
            action={t("auth.backToLogin")}
            onAction={() => setView({ kind: "login" })}
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
            onAction={() => setView({ kind: "login" })}
          />
        )}
        <LocaleFooter />
      </main>
    </div>
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
    <div className="auth-page">
      <main className="auth-column">
        <span className="auth-wordmark">
          <span className="mk">M</span>
          {t("auth.title")}
        </span>
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
      </main>
    </div>
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

function LoginForm({
  onAuthed,
  resetAvailable,
  onForgot,
}: Readonly<{
  onAuthed: () => void;
  resetAvailable: boolean;
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
      return data;
    },
    onSuccess: () => {
      onAuthed();
      // Restore the originally requested route (§8.5): a deep link the
      // user followed stays; only a bare entry lands on home.
      const hash = globalThis.location?.hash ?? "";
      if (!hash || hash === "#" || hash === "#/") {
        navigate({ screen: "home" });
      }
    },
    onError: (error) => {
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
      login.mutate();
    }
  };

  return (
    <form className="auth-card" onSubmit={submit}>
      <h1>{t("auth.loginTitle")}</h1>
      <div className="auth-fields">
        <Field id={emailId} label={t("auth.email")}>
          <input
            id={emailId}
            ref={emailRef}
            className="auth-input"
            type="email"
            autoComplete="username"
            placeholder={t("auth.emailPlaceholder")}
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        </Field>
        <Field
          id={passwordId}
          label={t("auth.password")}
          labelEnd={
            resetAvailable ? (
              <button type="button" className="auth-link" onClick={onForgot}>
                {t("auth.forgotLink")}
              </button>
            ) : undefined
          }
          hint={capsLock ? t("auth.capsLock") : undefined}
        >
          <div className="auth-password-row">
            <input
              id={passwordId}
              className="auth-input auth-input-reveal"
              type={showPassword ? "text" : "password"}
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              onKeyUp={(event) =>
                setCapsLock(event.getModifierState?.("CapsLock") ?? false)
              }
            />
            <button
              type="button"
              className="auth-reveal"
              aria-pressed={showPassword}
              aria-label={
                showPassword ? t("auth.hidePassword") : t("auth.showPassword")
              }
              title={
                showPassword ? t("auth.hidePassword") : t("auth.showPassword")
              }
              onClick={() => setShowPassword((v) => !v)}
            >
              {showPassword ? <EyeOff aria-hidden /> : <Eye aria-hidden />}
            </button>
          </div>
        </Field>
      </div>
      {login.isError && (
        <div className="auth-error" role="alert" tabIndex={-1} ref={errorRef}>
          <p className="ae-t">{t(loginErrorKey(login.error))}</p>
        </div>
      )}
      <div className="auth-actions">
        <Button
          type="submit"
          variant="primary"
          disabled={!ready || login.isPending}
        >
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
        <Field id={emailId} label={t("auth.email")}>
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
  children,
}: Readonly<{
  id: string;
  label: string;
  labelEnd?: ReactNode;
  hint?: string;
  children: ReactNode;
}>) {
  // The <label> names only the label text, so the input's accessible name is
  // exactly the label — the hint is a sibling below the input, not part of it.
  return (
    <div className="auth-field">
      <div className="auth-label-row">
        <label htmlFor={id}>{label}</label>
        {labelEnd}
      </div>
      {children}
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
