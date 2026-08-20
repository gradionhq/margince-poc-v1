import { useAuthCapabilities } from "../app/capabilities";
import { releaseSkew } from "../app/release";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { usePageTitle, Wordmark } from "./auth";
import { AuthExperience } from "./auth-core";

/**
 * The gate that stops this bundle rendering against an api from another release.
 *
 * WHERE IT SITS AND WHY. Ahead of every other gate in App, including the session
 * boundary and the public screens. A mixed-release set breaks the login request
 * itself — the SPA calls a contract the api has never served — so a guard behind
 * authentication would only ever run on installations that were already fine.
 * The capability probe is anonymous precisely so this can be asked before anyone
 * is signed in.
 *
 * IT DOES NOT LATCH. The skew is recomputed from the probe on every render, so a
 * rolling deploy that momentarily serves one release from one api replica and
 * another from the next clears itself as soon as the fleet converges — no state
 * to reset, no stuck screen. What it will NOT clear on its own is a torn tag
 * pull, because nothing about a torn pull resolves itself; that is what the
 * message asks a human to fix.
 */
export function useSkewedApiRelease(mine: string): string | null {
  const capabilities = useAuthCapabilities();
  const server = capabilities.data?.release_version;
  // A pending or failed probe is NOT a skew. Blocking while the answer is
  // unknown would flash this screen on every cold load, and blocking on a
  // failed probe would put a network blip on a screen that names a deployment
  // defect — the availability boundary below already tells that story honestly.
  //
  // Answering the api's release rather than a boolean, because the screen owes
  // the reader both numbers and this is the one place that holds both. The
  // explicit undefined check is what narrows it: releaseSkew already implies a
  // known value, but implying it is not the same as proving it to a reader.
  if (server === undefined || !releaseSkew(mine, server)) {
    return null;
  }
  return server;
}

/**
 * ReleaseSkewScreen is what a reader sees instead of the app.
 *
 * It names both releases. Most readers cannot act on the numbers, but this
 * screen exists only for a broken deployment, and the one person who can fix it
 * needs to know which of the two images is the odd one out — asking them to open
 * a console for it would be withholding the only fact on the page that matters.
 *
 * Reload is offered because it is genuinely the fix for the common case: a tab
 * held open across a deploy is running an old bundle, and fetching the document
 * again picks up the current one. It cannot fix a torn pull, which is why the
 * copy says what to do when it does not.
 */
export function ReleaseSkewScreen({
  app,
  server,
}: Readonly<{ app: string; server: string }>) {
  const t = useT();
  usePageTitle(t("release.skewTitle"));
  return (
    <AuthExperience phase="unavailable">
      <Wordmark alt={t("auth.title")} />
      <section className="auth-card" role="alert">
        <h1>{t("release.skewTitle")}</h1>
        <p className="card-sub">{t("release.skewBody")}</p>
        <p className="card-sub">{t("release.skewVersions", { app, server })}</p>
        <div className="auth-actions">
          <Button variant="primary" onClick={() => window.location.reload()}>
            {t("release.skewReload")}
          </Button>
        </div>
      </section>
    </AuthExperience>
  );
}
