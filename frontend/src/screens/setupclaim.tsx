import { useState } from "react";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { Field, usePageTitle, Wordmark } from "./auth";
import { AuthExperience } from "./auth-core";
import "./auth.css";

// The first-run claim (ADR-0105). An installation whose deployment file names no
// bootstrap_admin holds no organization, so /v1/me answers 503 and the boundary
// would otherwise render "installation not ready" — true, but a dead end. When
// the installation is instead WAITING to be claimed, this screen is what stands
// there.
//
// It is the only screen in the product that creates an account without one, and
// it says so: the operator's setup token is the authorization, and the account
// it creates is the installation's root. An interface that mints root quietly is
// not honest about what it is doing.

export type SetupStatus = { claimable: boolean };

/**
 * fetchSetupStatus asks whether this installation is waiting to be claimed.
 *
 * A failure is reported as "not claimable" rather than thrown: this runs only
 * on a boundary that has ALREADY decided the installation is unavailable, and
 * the honest fallback there is the availability screen the user would otherwise
 * have seen. A probe that cannot answer must not replace a true message with a
 * blank one.
 */
export async function fetchSetupStatus(): Promise<SetupStatus> {
  try {
    const response = await fetch("/setup/status", {
      headers: { Accept: "application/json" },
    });
    if (!response.ok) return { claimable: false };
    const body: unknown = await response.json();
    return {
      claimable:
        typeof body === "object" &&
        body !== null &&
        (body as SetupStatus).claimable === true,
    };
  } catch {
    return { claimable: false };
  }
}

type ClaimFields = {
  organizationName: string;
  adminName: string;
  adminEmail: string;
  adminPassword: string;
  setupToken: string;
};

const EMPTY: ClaimFields = {
  organizationName: "",
  adminName: "",
  adminEmail: "",
  adminPassword: "",
  setupToken: "",
};

/** The floor the server applies, restated so the form can say it before submitting. */
const MIN_PASSWORD = 12;

export function SetupClaimScreen({
  onClaimed,
}: Readonly<{ onClaimed: () => void }>) {
  const t = useT();
  usePageTitle(t("setup.pageTitle"));
  const [fields, setFields] = useState<ClaimFields>(EMPTY);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const set = (key: keyof ClaimFields) => (value: string) =>
    setFields((current) => ({ ...current, [key]: value }));

  // The browser's own timezone, offered as the default. An installation is
  // almost always claimed from where it will be used, and the alternative is
  // asking a human to name a zone they have no reason to know.
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const response = await fetch("/setup/claim", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          setup_token: fields.setupToken.trim(),
          organization_name: fields.organizationName.trim(),
          timezone,
          base_currency: "EUR",
          admin_email: fields.adminEmail.trim(),
          admin_name: fields.adminName.trim(),
          admin_password: fields.adminPassword,
        }),
      });
      if (response.ok) {
        onClaimed();
        return;
      }
      // Each refusal means something different to the person reading it: a
      // token that is not this installation's, a claim that arrived after
      // someone else's, or a field to fix. Collapsing them into one message
      // would leave the second case looking like a typo.
      setError(
        t(
          response.status === 401
            ? "setup.errorToken"
            : response.status === 409
              ? "setup.errorAlready"
              : "setup.errorFields",
        ),
      );
    } catch {
      setError(t("setup.errorNetwork"));
    } finally {
      setSubmitting(false);
    }
  }

  const passwordShort =
    fields.adminPassword.length > 0 &&
    [...fields.adminPassword].length < MIN_PASSWORD;
  const complete =
    fields.setupToken.trim() !== "" &&
    fields.organizationName.trim() !== "" &&
    fields.adminName.trim() !== "" &&
    fields.adminEmail.trim() !== "" &&
    !passwordShort &&
    fields.adminPassword !== "";

  return (
    <AuthExperience phase="unavailable">
      <Wordmark alt={t("auth.title")} />
      <form className="auth-card" onSubmit={submit}>
        <h1>{t("setup.title")}</h1>
        <p className="card-sub">{t("setup.body")}</p>
        {error && (
          <p className="auth-error" role="alert">
            {error}
          </p>
        )}
        <div className="auth-fields">
          <Field
            id="setup-token"
            label={t("setup.token")}
            hint={t("setup.tokenHint")}
          >
            <input
              id="setup-token"
              className="auth-input"
              name="setup-token"
              required
              autoComplete="off"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              value={fields.setupToken}
              onChange={(event) => set("setupToken")(event.target.value)}
            />
          </Field>
          <Field id="setup-org" label={t("setup.organization")}>
            <input
              id="setup-org"
              className="auth-input"
              name="organization"
              required
              value={fields.organizationName}
              onChange={(event) => set("organizationName")(event.target.value)}
            />
          </Field>
          <Field id="setup-admin-name" label={t("setup.adminName")}>
            <input
              id="setup-admin-name"
              className="auth-input"
              name="admin-name"
              required
              autoComplete="name"
              value={fields.adminName}
              onChange={(event) => set("adminName")(event.target.value)}
            />
          </Field>
          <Field id="setup-admin-email" label={t("setup.adminEmail")}>
            <input
              id="setup-admin-email"
              className="auth-input"
              type="email"
              name="admin-email"
              required
              autoComplete="username"
              inputMode="email"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              value={fields.adminEmail}
              onChange={(event) => set("adminEmail")(event.target.value)}
            />
          </Field>
          <Field
            id="setup-admin-password"
            label={t("setup.adminPassword")}
            hint={
              passwordShort ? t("setup.passwordShort") : t("setup.passwordHint")
            }
          >
            <input
              id="setup-admin-password"
              className="auth-input"
              type="password"
              name="admin-password"
              required
              autoComplete="new-password"
              value={fields.adminPassword}
              onChange={(event) => set("adminPassword")(event.target.value)}
            />
          </Field>
        </div>
        <p className="card-sub">{t("setup.rootWarning")}</p>
        <div className="auth-actions">
          <Button
            type="submit"
            variant="primary"
            disabled={!complete || submitting}
          >
            {submitting ? t("setup.claiming") : t("setup.claim")}
          </Button>
        </div>
      </form>
    </AuthExperience>
  );
}
