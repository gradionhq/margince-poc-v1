import type { components } from "../api/schema";

/**
 * UI-preview scaffolding for design review. **Not a feature flag.**
 *
 * Nothing here changes what the product can do. Each switch below makes a piece
 * of already-built, capability-gated MARKUP visible on the running app so it can
 * be reviewed at full size, in both themes, at real breakpoints — the things
 * Storybook shows in isolation but a stakeholder walking the screen never sees.
 * Every switch is OFF unless its env var is set at dev/build time, so a plain
 * `pnpm build` and a plain `pnpm dev` behave exactly as if this file did not
 * exist.
 *
 * The naming rule is the whole point: `VITE_UI_PREVIEW_*`. A reader who greps
 * for it must not be able to mistake it for a capability toggle. These switches
 * cannot make a flow work — only draw one.
 */

// Both spellings, so a hurried demo command works either way.
const isOn = (value: unknown) => value === "1" || value === "true";

/**
 * `VITE_UI_PREVIEW_OIDC=1` — draw the federated sign-in buttons on the login
 * screen even though this installation serves none.
 *
 * Why it exists: `/auth/capabilities` serves `oidc_providers: []` because the
 * OIDC flow has not shipped (§19), and `ProviderButtons` correctly renders
 * nothing for an empty list. That gate is right and stays right — but it also
 * means the federated block, which IS designed and built, can only be seen in
 * Storybook. A UI/UX review of the login screen needs it on the login screen.
 *
 * What it does NOT do: it does not touch the wire, the query cache, or
 * `startFederatedSignIn`, which stays inert. The buttons render, take focus, and
 * do nothing when clicked — there is no OIDC endpoint in the contract to send a
 * browser to, and inventing one would be the exact lie this switch is written to
 * avoid.
 *
 * Read at the call rather than at module load so a test can pin BOTH positions
 * of the switch without re-evaluating the module graph. `import.meta.env` is a
 * compile-time substitution either way: with the var unset this is
 * `isOn(undefined)`, i.e. `false`, in every build.
 */
export function uiPreviewOidcEnabled(): boolean {
  return isOn(import.meta.env.VITE_UI_PREVIEW_OIDC);
}

/**
 * `VITE_UI_PREVIEW_RESET=1` — draw the "Forgot password?" link even though this
 * installation reports the reset capability as off.
 *
 * Why it exists: unlike OIDC, this flow is finished on BOTH sides. The contract
 * documents `POST /auth/forgot-password` and `POST /auth/reset-password`, the
 * handlers are `backend/internal/modules/identity/reset.go`, and this screen
 * already renders all four views behind the link — the request form, the neutral
 * "check your inbox" confirmation, the emailed deep link's new-password form, and
 * the spent-token refusal. What is missing is one operator setting.
 * `AuthCapabilities.password_reset` is computed live as `h.resetMailer != nil`,
 * and the shipped `config/margince.yaml` carries no `email:` block, so
 * `Email.Enabled` is false, no mailer is wired, and the capability answers
 * `false` on every local run. The link is right to be absent there — and a design
 * review that only ever sees the local run never sees the link, or the card behind
 * it, at all.
 *
 * What it does NOT do: it does not wire a mailer, and it does not fake one. What
 * becomes reviewable is the link and the request card it opens — the real one,
 * posting to the real endpoint. Against a mailer-less installation that post is
 * answered `501 not_implemented` (`httperr.NotImplemented`; a status this path does
 * not enumerate in `crm.yaml`) and the card shows it as its failure note, so the
 * confirmation, the deep-link form and the spent-token refusal stay where they
 * were — asserted in `screens/auth.test.tsx`, not on this screen. The switch draws
 * the door and the door opens; the room behind it is the installation's to
 * configure.
 *
 * Read at the call rather than at module load, for the same reason as the OIDC
 * switch above.
 */
export function uiPreviewResetEnabled(): boolean {
  return isOn(import.meta.env.VITE_UI_PREVIEW_RESET);
}

let warnedReset = false;

/**
 * The single override site for the reset link: what the server said in, what the
 * screen draws out.
 *
 * A served `true` wins and is returned untouched — an installation that really
 * has a mailer is the truth, and the preview only ever fills a genuine `false`.
 */
export function previewedPasswordReset(served: boolean): boolean {
  if (served || !uiPreviewResetEnabled()) {
    return served;
  }
  if (!warnedReset) {
    warnedReset = true;
    // Loud on purpose, once: a build that draws controls the installation cannot
    // honour has to say so where anyone inspecting it will see it.
    console.warn(
      "[ui-preview] VITE_UI_PREVIEW_RESET is on: the forgot-password link is drawn for design review. This installation has no outbound mailer configured, so a submitted request is answered 501 by the server.",
    );
  }
  return true;
}

type OidcProviders =
  components["schemas"]["AuthCapabilities"]["oidc_providers"];

/**
 * The two providers the preview draws.
 *
 * These are stand-ins for a SERVER's strings. `oidc_providers[].label` is
 * server-owned copy — the contract documents it as the button text and §11.5
 * says it is never translated — so they deliberately do NOT live in the i18n
 * catalogs, and a German reviewer sees them in English exactly as a real
 * installation's English labels would appear. The wording matches the contract's
 * own example label and the Storybook story, so the preview and the catalog show
 * the same thing.
 *
 * They live in this `.ts` module rather than in `auth.tsx` for a second reason:
 * the no-inline-copy gate (`design-system/conformance.test.ts`) walks JSX text
 * and four user-facing attributes in `.tsx` files. A fixture array is neither,
 * so it would have passed the gate wherever it sat — but keeping it out of the
 * component file means the screen still contains no English string at all, and
 * the gate needs no exemption to say so.
 */
const PREVIEW_OIDC_PROVIDERS: OidcProviders = [
  { key: "google", label: "Continue with Google" },
  { key: "microsoft", label: "Continue with Microsoft" },
];

let warned = false;

/**
 * The single override site: what the server said in, what the screen draws out.
 *
 * The server said `[]`. With the preview switch on, the UI substitutes two
 * providers HERE, at the render boundary — after the query, never inside it. The
 * cached capability response is untouched, so anything else reading
 * `/auth/capabilities` still sees the installation's real, empty answer, and the
 * override cannot outlive this call.
 *
 * A non-empty served list always wins: a real installation's providers are the
 * truth, and the preview only ever fills a genuine emptiness.
 */
export function previewedOidcProviders(served: OidcProviders): OidcProviders {
  if (served.length > 0 || !uiPreviewOidcEnabled()) {
    return served;
  }
  if (!warned) {
    warned = true;
    // Loud on purpose, once: a build that draws controls the installation cannot
    // honour has to say so where anyone inspecting it will see it.
    console.warn(
      "[ui-preview] VITE_UI_PREVIEW_OIDC is on: the federated sign-in buttons are drawn for design review. This installation serves no OIDC providers and these buttons complete no sign-in.",
    );
  }
  return PREVIEW_OIDC_PROVIDERS;
}

const NO_UNAVAILABLE_PROVIDERS: ReadonlySet<string> = new Set();
const PREVIEW_UNAVAILABLE_PROVIDERS: ReadonlySet<string> = new Set([
  "microsoft",
]);

/**
 * The provider keys the preview marks as **not yet available**, so a reviewer can
 * see both halves of the design: a provider offered normally beside one the
 * installation has not finished wiring.
 *
 * This can only ever come from the preview layer, and that is the load-bearing
 * part rather than an implementation note. `AuthCapabilities.oidc_providers`
 * items are typed `{ key, label }` and carry NO availability field — there is no
 * wire shape a real server could use to say this, so a real server can never
 * produce a marked provider. `ProviderButtons` therefore behaves identically in
 * the product: it receives an empty set, and an empty set is the same component
 * it was before this existed. Nothing about the shipped surface changes, which is
 * why this needs no spec amendment.
 *
 * The spec position, plainly: §3.3 of the login spec ("honest functionality")
 * forbids a disabled provider control, naming Google, Microsoft and SSO
 * explicitly, and ADR-0076 keeps §3.3 load-bearing rather than relaxed — Decision
 * 1's own prose bans a control whose behaviour does not exist. A marked button is
 * exactly that control, so it is illegal on the PRODUCT surface and stays
 * illegal. What makes this legal is what it is: a design-review fixture in the
 * same class as a Storybook story, off unless a build-time var is set, never
 * reachable by a user of a shipped build.
 */
export function previewedUnavailableProviders(): ReadonlySet<string> {
  return uiPreviewOidcEnabled()
    ? PREVIEW_UNAVAILABLE_PROVIDERS
    : NO_UNAVAILABLE_PROVIDERS;
}
