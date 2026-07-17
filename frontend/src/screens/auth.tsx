import { useMutation, useQuery } from "@tanstack/react-query";
import { type FormEvent, type ReactNode, useId, useState } from "react";
import { api } from "../api/client";
import { navigate } from "../app/router";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";
import "./auth.css";

// The default unauthenticated screen is LOGIN, not setup or signup
// (A107/ADR-0061): one installation serves one organization, provisioned at
// API boot from the deployment file — the browser never creates a tenant and
// never selects one. A single centered column, a real <form> (Enter submits),
// and only the authentication methods the capabilities probe reports as
// operational: the forgot-password flow renders exactly when the server can
// complete it.

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

export function AuthScreen({ onAuthed }: Readonly<{ onAuthed: () => void }>) {
  const t = useT();
  const [view, setView] = useState<View>(() => {
    const token = resetTokenFromLocation();
    return token ? { kind: "reset", token } : { kind: "login" };
  });

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
          <LoginForm
            onAuthed={onAuthed}
            resetAvailable={resetAvailable}
            onForgot={() => setView({ kind: "forgot" })}
          />
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
      </main>
    </div>
  );
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

  const login = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/auth/login", {
        body: { email: email.trim(), password },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => {
      onAuthed();
      navigate({ screen: "home" });
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
            className="auth-input"
            type="email"
            autoComplete="email"
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
              className="auth-input"
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
              className="auth-link"
              aria-pressed={showPassword}
              onClick={() => setShowPassword((v) => !v)}
            >
              {showPassword ? t("auth.hidePassword") : t("auth.showPassword")}
            </button>
          </div>
        </Field>
      </div>
      {login.isError && (
        <ErrorNote
          message={login.error instanceof Error ? login.error.message : null}
        />
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
            autoComplete="email"
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
