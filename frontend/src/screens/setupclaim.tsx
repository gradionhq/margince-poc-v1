import { useState } from "react";
import { Button, Field, TextInput } from "../design-system/atoms";
import { usePasswordReveal } from "../design-system/passwordreveal";
import { viewerZone } from "../format/timezone";
import { useT } from "../i18n";
import { usePageTitle, Wordmark } from "./auth";
import { AuthExperience } from "./auth-core";
import { isTooShort } from "./passwordrule";
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
    // Narrowed, not asserted: this body arrives from the network, and an `as`
    // here would promise the compiler a shape nothing checked. Anything that is
    // not literally `true` reads as not-claimable, which is the safe direction —
    // the cost of getting it wrong is a claim screen on an installation that
    // cannot be claimed.
    if (typeof body === "object" && body !== null && "claimable" in body) {
      return { claimable: body.claimable === true };
    }
    return { claimable: false };
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

export function SetupClaimScreen({
  onClaimed,
}: Readonly<{ onClaimed: () => void }>) {
  const t = useT();
  usePageTitle(t("setup.pageTitle"));
  const [fields, setFields] = useState<ClaimFields>(EMPTY);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  // The root credential is typed once with no confirm field to disagree with
  // it, so reading it back is the only check there is.
  const reveal = usePasswordReveal({
    show: t("auth.showPassword"),
    hide: t("auth.hidePassword"),
  });

  const set = (key: keyof ClaimFields) => (value: string) =>
    setFields((current) => ({ ...current, [key]: value }));

  // The browser's own timezone, offered as the default. An installation is
  // almost always claimed from where it will be used, and the alternative is
  // asking a human to name a zone they have no reason to know.
  const timezone = viewerZone();

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
      // Each refusal means something different to the person reading it, and
      // each implies a different next action: find the right token, sign in
      // instead, fix a field, or wait and try again. Collapsing them would
      // leave three of the four telling someone to correct a form that is
      // already correct — a 500 above all, which is not their fault and not
      // theirs to fix.
      setError(
        t(
          response.status === 401
            ? "setup.errorToken"
            : response.status === 409
              ? "setup.errorAlready"
              : response.status >= 500
                ? "setup.errorServer"
                : "setup.errorFields",
        ),
      );
    } catch {
      setError(t("setup.errorNetwork"));
    } finally {
      setSubmitting(false);
    }
  }

  const passwordShort = isTooShort(fields.adminPassword);
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
          <Field label={t("setup.token")} required hint={t("setup.tokenHint")}>
            {(control) => (
              <TextInput
                {...control}
                name="setup-token"
                autoComplete="off"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                value={fields.setupToken}
                onChange={(event) => set("setupToken")(event.target.value)}
              />
            )}
          </Field>
          <Field label={t("setup.organization")} required>
            {(control) => (
              <TextInput
                {...control}
                name="organization"
                value={fields.organizationName}
                onChange={(event) =>
                  set("organizationName")(event.target.value)
                }
              />
            )}
          </Field>
          <Field label={t("setup.adminName")} required>
            {(control) => (
              <TextInput
                {...control}
                name="admin-name"
                autoComplete="name"
                value={fields.adminName}
                onChange={(event) => set("adminName")(event.target.value)}
              />
            )}
          </Field>
          <Field label={t("setup.adminEmail")} required>
            {(control) => (
              <TextInput
                {...control}
                type="email"
                name="admin-email"
                autoComplete="username"
                inputMode="email"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                value={fields.adminEmail}
                onChange={(event) => set("adminEmail")(event.target.value)}
              />
            )}
          </Field>
          <Field
            label={t("setup.adminPassword")}
            required
            error={passwordShort ? t("setup.passwordShort") : undefined}
            // The rule, until the rule is being broken — at which point the
            // refusal restates it in the danger tone and a second grey copy of
            // the same sentence underneath is noise.
            hint={passwordShort ? undefined : t("setup.passwordHint")}
            trailing={reveal.trailing}
          >
            {(control) => (
              <TextInput
                {...control}
                type={reveal.type}
                name="admin-password"
                autoComplete="new-password"
                value={fields.adminPassword}
                onChange={(event) => set("adminPassword")(event.target.value)}
              />
            )}
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
