import { useMutation } from "@tanstack/react-query";
import { FileCheck2, Lock, ShieldCheck } from "lucide-react";
import { type ReactNode, useId, useState } from "react";
import { api, setWorkspaceSlug } from "../api/client";
import { navigate } from "../app/router";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";
import "./auth.css";

// First-run auth (signup + login). A split hero on the funnel's design language:
// the value proposition beside the form. The product has no subdomain in local
// dev, so the workspace is resolved from the X-Workspace-Slug header the API
// client sends — signup derives it from the workspace name, login collects it.
// Both endpoints are the contract's public paths (POST /workspaces,
// /auth/login); success sets the httpOnly session cookie the rest of the app
// rides.

// Mirror of the server's identity.slugify (lowercase; keep [a-z0-9]; map
// space/-/_ to '-'; trim leading/trailing '-'). Pinned against the Go rule in
// auth.test.tsx so the two never drift — the dev slug must resolve the exact
// workspace the bootstrap created.
export function deriveWorkspaceSlug(name: string): string {
  let out = "";
  for (const ch of name.trim().toLowerCase()) {
    if ((ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9")) {
      out += ch;
    } else if (ch === " " || ch === "-" || ch === "_") {
      out += "-";
    }
  }
  return out.replace(/^-+/, "").replace(/-+$/, "");
}

const MIN_PASSWORD = 12;

type Mode = "signup" | "login";

export function AuthScreen({ onAuthed }: Readonly<{ onAuthed: () => void }>) {
  const t = useT();
  const [mode, setMode] = useState<Mode>("signup");

  return (
    <div className="auth-page">
      <div className="auth-shell">
        <div className="auth-hero">
          <span className="ob-wordmark">
            <span className="mk">M</span>
            {t("auth.title")}
          </span>
          <h1>{t("auth.heroTitle")}</h1>
          <p className="lede">{t("auth.heroSub")}</p>
          <ul className="auth-bullets">
            <li>
              <span className="bi">
                <FileCheck2 aria-hidden />
              </span>
              {t("auth.bulletEvidence")}
            </li>
            <li>
              <span className="bi">
                <ShieldCheck aria-hidden />
              </span>
              {t("auth.bulletConfirm")}
            </li>
            <li>
              <span className="bi">
                <Lock aria-hidden />
              </span>
              {t("auth.bulletOwn")}
            </li>
          </ul>
        </div>

        {mode === "signup" ? (
          <SignupForm onAuthed={onAuthed} onToLogin={() => setMode("login")} />
        ) : (
          <LoginForm onAuthed={onAuthed} onToSignup={() => setMode("signup")} />
        )}
      </div>
    </div>
  );
}

function SignupForm({
  onAuthed,
  onToLogin,
}: Readonly<{
  onAuthed: () => void;
  onToLogin: () => void;
}>) {
  const t = useT();
  const nameId = useId();
  const displayId = useId();
  const emailId = useId();
  const passwordId = useId();
  const [workspaceName, setWorkspaceName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const signup = useMutation({
    mutationFn: async () => {
      const timezone =
        Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
      const { data, error } = await api.POST("/workspaces", {
        body: {
          workspace_name: workspaceName.trim(),
          admin_email: email.trim(),
          admin_password: password,
          admin_display_name: displayName.trim(),
          timezone,
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      // The bootstrap ran unauthenticated (no slug yet); persist the slug it
      // derived so every later /v1 call resolves this workspace in dev.
      setWorkspaceSlug(deriveWorkspaceSlug(workspaceName));
      return data;
    },
    onSuccess: () => {
      onAuthed();
      navigate({ screen: "onboarding" });
    },
  });

  const ready =
    workspaceName.trim() !== "" &&
    displayName.trim() !== "" &&
    email.trim() !== "" &&
    password.length >= MIN_PASSWORD;

  return (
    <section className="auth-card">
      <h2>{t("auth.signupTitle")}</h2>
      <p className="card-sub">{t("auth.signupSub")}</p>
      <div className="auth-fields">
        <Field id={nameId} label={t("auth.workspaceName")}>
          <input
            id={nameId}
            className="auth-input"
            value={workspaceName}
            onChange={(event) => setWorkspaceName(event.target.value)}
          />
        </Field>
        <Field id={displayId} label={t("auth.displayName")}>
          <input
            id={displayId}
            className="auth-input"
            value={displayName}
            onChange={(event) => setDisplayName(event.target.value)}
          />
        </Field>
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
      {signup.isError && (
        <ErrorNote
          message={signup.error instanceof Error ? signup.error.message : null}
        />
      )}
      <div className="auth-actions">
        <Button
          variant="primary"
          disabled={!ready || signup.isPending}
          onClick={() => signup.mutate()}
        >
          {t("auth.createWorkspace")}
        </Button>
        <button type="button" className="auth-switch" onClick={onToLogin}>
          {t("auth.toLogin")}
        </button>
      </div>
    </section>
  );
}

function LoginForm({
  onAuthed,
  onToSignup,
}: Readonly<{
  onAuthed: () => void;
  onToSignup: () => void;
}>) {
  const t = useT();
  const slugId = useId();
  const emailId = useId();
  const passwordId = useId();
  const [slug, setSlug] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  const login = useMutation({
    mutationFn: async () => {
      // The workspace header must be set BEFORE the call — /auth/login is
      // public but still resolves the workspace from the slug (dev) / subdomain.
      setWorkspaceSlug(slug.trim());
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

  const ready = slug.trim() !== "" && email.trim() !== "" && password !== "";

  return (
    <section className="auth-card">
      <h2>{t("auth.loginTitle")}</h2>
      <p className="card-sub">{t("auth.loginSub")}</p>
      <div className="auth-fields">
        <Field id={slugId} label={t("auth.workspaceSlug")}>
          <input
            id={slugId}
            className="auth-input"
            value={slug}
            onChange={(event) => setSlug(event.target.value)}
          />
        </Field>
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
        <Field id={passwordId} label={t("auth.password")}>
          <input
            id={passwordId}
            className="auth-input"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
      </div>
      {login.isError && (
        <ErrorNote
          message={login.error instanceof Error ? login.error.message : null}
        />
      )}
      <div className="auth-actions">
        <Button
          variant="primary"
          disabled={!ready || login.isPending}
          onClick={() => login.mutate()}
        >
          {t("auth.signIn")}
        </Button>
        <button type="button" className="auth-switch" onClick={onToSignup}>
          {t("auth.toSignup")}
        </button>
      </div>
    </section>
  );
}

function Field({
  id,
  label,
  hint,
  children,
}: Readonly<{
  id: string;
  label: string;
  hint?: string;
  children: ReactNode;
}>) {
  // The <label> names only the label text, so the input's accessible name is
  // exactly the label — the hint is a sibling below the input, not part of it.
  return (
    <div className="auth-field">
      <label htmlFor={id}>{label}</label>
      {children}
      {hint && <span className="auth-hint">{hint}</span>}
    </div>
  );
}

function ErrorNote({ message }: Readonly<{ message: string | null }>) {
  const t = useT();
  return (
    <div className="auth-error">
      <p className="ae-t">{t("auth.failed")}</p>
      {message && <p className="ae-m">{message}</p>}
    </div>
  );
}
