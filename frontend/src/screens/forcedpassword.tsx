import { useT } from "../i18n";
import { usePageTitle, Wordmark } from "./auth";
import { AuthExperience } from "./auth-core";
import { ChangePasswordCard } from "./passwordcard";
import "./auth.css";

// The account whose password an operator chose.
//
// A configured bootstrap hands the first admin a credential from a deployment
// file. The server refuses every route but the one that replaces it, so the
// person is authenticated and can do nothing — and the login screen is the
// wrong answer, because their credentials are correct and using them again
// lands in the same refusal.
//
// This stands at the boundary instead, carrying the same card the settings page
// uses. One implementation of "change your password", reached two ways: nothing
// about the act differs here, only the reason for being sent to it.
export function ForcedPasswordChangeScreen({
  onChanged,
}: Readonly<{ onChanged: () => void }>) {
  const t = useT();
  usePageTitle(t("forcedPassword.pageTitle"));
  return (
    <AuthExperience phase="unavailable">
      <Wordmark alt={t("auth.title")} />
      <section className="auth-card">
        <h1>{t("forcedPassword.title")}</h1>
        <p className="card-sub">{t("forcedPassword.body")}</p>
      </section>
      <ChangePasswordCard onChanged={onChanged} />
    </AuthExperience>
  );
}
