import { useMutation } from "@tanstack/react-query";
import { type ReactNode, useId, useState } from "react";
import { api, setWorkspaceSlug } from "../api/client";
import { navigate } from "../app/router";
import { Button, SectionHeader, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";

// First-run auth (signup + login). The product has no subdomain in local dev,
// so the workspace is resolved from the X-Workspace-Slug header the API client
// sends — signup derives it from the workspace name, login collects it. Both
// endpoints are the contract's public paths (POST /workspaces, /auth/login);
// success sets the httpOnly session cookie the rest of the app rides.

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
  return out.replace(/^-+|-+$/g, "");
}

const MIN_PASSWORD = 12;

type Mode = "signup" | "login";

export function AuthScreen({ onAuthed }: { onAuthed: () => void }) {
  const t = useT();
  const [mode, setMode] = useState<Mode>("signup");

  return (
    <div className="wrap narrow ob-top">
      <SectionHeader title={t("auth.title")} sub={t("auth.sub")} />
      {mode === "signup" ? (
        <SignupForm onAuthed={onAuthed} onToLogin={() => setMode("login")} />
      ) : (
        <LoginForm onAuthed={onAuthed} onToSignup={() => setMode("signup")} />
      )}
    </div>
  );
}

function SignupForm({
  onAuthed,
  onToLogin,
}: {
  onAuthed: () => void;
  onToLogin: () => void;
}) {
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
    <section className="card">
      <SectionHeader title={t("auth.signupTitle")} sub={t("auth.signupSub")} />
      <div className="auth-fields">
        <Field id={nameId} label={t("auth.workspaceName")}>
          <TextInput
            id={nameId}
            value={workspaceName}
            onChange={(event) => setWorkspaceName(event.target.value)}
          />
        </Field>
        <Field id={displayId} label={t("auth.displayName")}>
          <TextInput
            id={displayId}
            value={displayName}
            onChange={(event) => setDisplayName(event.target.value)}
          />
        </Field>
        <Field id={emailId} label={t("auth.email")}>
          <TextInput
            id={emailId}
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
          <TextInput
            id={passwordId}
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
}: {
  onAuthed: () => void;
  onToSignup: () => void;
}) {
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
    <section className="card">
      <SectionHeader title={t("auth.loginTitle")} sub={t("auth.loginSub")} />
      <div className="auth-fields">
        <Field id={slugId} label={t("auth.workspaceSlug")}>
          <TextInput
            id={slugId}
            value={slug}
            onChange={(event) => setSlug(event.target.value)}
          />
        </Field>
        <Field id={emailId} label={t("auth.email")}>
          <TextInput
            id={emailId}
            type="email"
            autoComplete="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        </Field>
        <Field id={passwordId} label={t("auth.password")}>
          <TextInput
            id={passwordId}
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
}: {
  id: string;
  label: string;
  hint?: string;
  children: ReactNode;
}) {
  // The <label> wraps only the label text, so the input's accessible name is
  // exactly the label — the hint is a sibling, described-by, not part of it.
  const hintId = `${id}-hint`;
  return (
    <div className="auth-field">
      <label className="t-label" htmlFor={id}>
        {label}
      </label>
      {children}
      {hint && (
        <span className="t-caption" id={hintId}>
          {hint}
        </span>
      )}
    </div>
  );
}

function ErrorNote({ message }: { message: string | null }) {
  const t = useT();
  return (
    <div className="card card-inset auth-error">
      <p className="t-label">{t("auth.failed")}</p>
      {message && (
        <p className="t-caption" style={{ marginTop: 4 }}>
          {message}
        </p>
      )}
    </div>
  );
}
