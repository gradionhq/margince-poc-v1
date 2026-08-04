import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { Locale } from "../i18n";
import { AuthScreen, AvailabilityScreen, ProviderButtons } from "./auth";
import { type AssistantProfile, AuthExperience } from "./auth-core";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

/**
 * The unauthenticated surface, whole (ADR-0076).
 *
 * The Core is a design-system primitive now, so its own states live in
 * `Design System/Margince Core`; these stories are about the SURFACE — the two
 * regions, the runtime posture the identity region reads from the server, the
 * two narrow layouts where the regions stop being side by side, and the states
 * that are not a failed password.
 *
 * Two kinds of failure are treated differently here, and the split is an
 * argument rather than an accident. A LOGIN failure changes one sentence, so the
 * credential error, the login rate limit and the pending button are still not in
 * this catalog — they are asserted in `auth.test.tsx`, where a sentence can be
 * compared instead of eyeballed. A RESET failure changes what the surface
 * OFFERS: only a token verdict may offer "Request a new link", because minting
 * one supersedes the token the user is still holding. That is a difference in
 * controls, so each of the four is a story, driven through a real submit.
 */
const meta: Meta = {
  title: "Screens/Auth/Unauthenticated surface",
  parameters: {
    layout: "fullscreen",
    // Two widths, named after the RULE each one exercises rather than after a
    // device: the surface changes shape at 959 and again at 560, and "iPad Pro
    // 11-in" tells a reviewer nothing about which of those is in force. The
    // viewport tool ships inside Storybook 9 itself, so this adds no addon to
    // `.storybook/main.ts`.
    viewport: {
      options: {
        stacked: {
          name: "Stacked (max 959px)",
          styles: { width: "834px", height: "1180px" },
        },
        taskOnly: {
          name: "Task only (max 560px)",
          styles: { width: "390px", height: "844px" },
        },
      },
    },
  },
};
export default meta;

type Story = StoryObj;

const configured: AssistantProfile = {
  name: "Margince",
  kind: "ai",
  state: "configured",
  inference_mode: "hybrid",
  providers: ["anthropic", "ollama"],
};

function AuthStory({
  profile,
  profileStatus = 200,
  notice,
  // Empty is what the running installation serves — the OIDC flow has not
  // shipped (§19) — so it stays the default here too. A STORY seeds providers to
  // review the federated design; the shipped app never seeds the capability
  // response. The one way to see this block on the running app is the
  // `VITE_UI_PREVIEW_OIDC` preview switch (`app/ui-preview.ts`), which overrides
  // at the render boundary and leaves the wire alone.
  oidcProviders = [],
  locale = "en",
}: Readonly<{
  profile: AssistantProfile;
  profileStatus?: number;
  notice?: "session-expired" | "signed-out";
  oidcProviders?: ReadonlyArray<{ key: string; label: string }>;
  locale?: Locale;
}>) {
  installFetchStub({
    "GET /assistant/profile": () =>
      jsonResponse(
        profileStatus === 200 ? profile : { title: "Unavailable" },
        profileStatus,
      ),
    "GET /auth/capabilities": () =>
      jsonResponse({
        password: true,
        password_reset: true,
        oidc_providers: oidcProviders,
      }),
  });
  return (
    <StoryProviders locale={locale}>
      <AuthScreen onAuthed={() => undefined} notice={notice} />
    </StoryProviders>
  );
}

export const ConfiguredHybrid: Story = {
  render: () => <AuthStory profile={configured} />,
};

/**
 * The installation's administrator has wired SSO (§11).
 *
 * The real server serves `oidc_providers: []` until the OIDC flow ships, and the
 * component renders nothing for an empty list. Seeding it here reviews the design
 * without claiming the build can complete the flow. The same block can be put on
 * the running app for a walkthrough with `VITE_UI_PREVIEW_OIDC=1 pnpm dev` — same
 * override discipline, same inert buttons (`app/ui-preview.ts`).
 *
 * The password form is still there and still complete — it is the fallback door,
 * which is why the divider labels IT rather than the buttons above.
 */
export const WithProviders: Story = {
  render: () => (
    <AuthStory
      profile={configured}
      oidcProviders={[
        { key: "google", label: "Continue with Google" },
        { key: "microsoft", label: "Continue with Microsoft" },
      ]}
    />
  ),
};

/**
 * One provider offered, one marked **not yet available** (§11 + `app/ui-preview.ts`).
 *
 * This state exists only for design review, and the reason is structural rather
 * than a policy someone could relax: `oidc_providers[]` items are `{ key, label }`
 * with no availability field, so no server can produce a marked provider —
 * `ProviderButtons` receives the marker from the preview layer or from nothing.
 * §3.3 forbids a dead provider control on the product surface by name (Google,
 * Microsoft, SSO) and ADR-0076 keeps §3.3 load-bearing, so a story and a
 * `VITE_UI_PREVIEW_OIDC=1` build are the only two places this may be seen.
 *
 * What it reviews: the note sits INSIDE the button, so it is part of the accessible
 * name — a natively disabled button is not focusable, so a description hung off it
 * would never be reached. And it wraps to its own line, which is what keeps the two
 * buttons the same height down to 320px.
 */
export const ProviderNotYetAvailable: Story = {
  render: () => (
    <StoryProviders>
      {/* The block itself, inside the real surface frame rather than inside
          `AuthScreen`. `AuthScreen` gets the marker from
          `previewedUnavailableProviders()`, which reads a BUILD-time var no story
          can set — so the honest way to review this state is to hand the marker
          to the component that renders it. Same component, same stylesheet, real
          task measure. */}
      <AuthExperience profile={configured} phase="idle">
        <div className="auth-card">
          <ProviderButtons
            providers={[
              { key: "google", label: "Continue with Google" },
              { key: "microsoft", label: "Continue with Microsoft" },
            ]}
            unavailable={new Set(["microsoft"])}
            onSelect={() => undefined}
          />
        </div>
      </AuthExperience>
    </StoryProviders>
  ),
};

/**
 * A provider this frontend has never heard of, which is the normal case for a
 * self-hosted product: `oidc_providers[].key` is an open string in the contract.
 *
 * Two things are load-bearing here. The label is the INSTALLATION's own text, so
 * it can be German while the rest of the catalog is English. And the mark falls
 * back to a neutral key icon rather than the block disappearing, because a
 * working sign-in path must not be hidden for want of a logo.
 */
export const UnknownProvider: Story = {
  render: () => (
    <AuthStory
      profile={configured}
      oidcProviders={[{ key: "corp-sso", label: "Anmeldung über Werk-IT" }]}
    />
  ),
};

export const Unconfigured: Story = {
  render: () => (
    <AuthStory
      profile={{
        name: "Margince",
        kind: "ai",
        state: "unconfigured",
        inference_mode: "none",
        providers: [],
      }}
    />
  ),
};

export const Development: Story = {
  render: () => (
    <AuthStory
      profile={{
        name: "Margince",
        kind: "ai",
        state: "development",
        inference_mode: "development",
        providers: [],
      }}
    />
  ),
};

export const ProfileUnavailable: Story = {
  render: () => <AuthStory profile={configured} profileStatus={500} />,
};

export const SessionExpired: Story = {
  render: () => <AuthStory profile={configured} notice="session-expired" />,
};

/**
 * §4: a server outage is not a wrong password. Same two-region frame, different
 * product state, and the Core reads `unavailable` — the one state that does not
 * breathe.
 */
export const ConnectionProblem: Story = {
  render: () => (
    <StoryProviders>
      <AvailabilityScreen kind="connection" onRetry={() => undefined} />
    </StoryProviders>
  ),
};

export const InstallationUnavailable: Story = {
  render: () => (
    <StoryProviders>
      <AvailabilityScreen kind="installation" onRetry={() => undefined} />
    </StoryProviders>
  ),
};

export const SignedOut: Story = {
  render: () => <AuthStory profile={configured} notice="signed-out" />,
};

// The two stories below pick their width with Storybook's viewport tool, and one
// thing about that is worth knowing before trusting a picture of either: the tool
// is applied by the MANAGER, which resizes the preview iframe. These breakpoints
// are viewport media queries, so nothing inside the preview can stand in for
// that — a story opened as a bare `iframe.html`, which is how the fe-uat capture
// gate renders, gets the harness's own width and draws the WIDE layout. Review
// these two in Storybook itself, or by narrowing the browser.

/**
 * Below 960 the two regions become two ROWS, and the story earns its place
 * because the change is not "the aside moved down".
 *
 * Both rows are centred on the SAME 440px measure the form uses, so the page
 * reads as one column down its middle rather than as a form above a full-bleed
 * band, and the identity region drops its card chrome for a hairline — at this
 * width it is a band of the page, not an inset card. The Core stays in flow
 * above the identity words, as it is on desktop: the sphere and the sentences it
 * is speaking are one cluster, and the layout that lifted it into the opposite
 * corner separated them. It costs the form nothing, because it is in the second
 * row.
 */
export const StackedTablet: Story = {
  globals: { viewport: { value: "stacked" } },
  render: () => <AuthStory profile={configured} />,
};

/**
 * At 560 and below the surface is the task alone: `aside.auth-identity` is gone,
 * the Core sits in the LEFT lane of the phone disclosure lining up with the
 * sentence it belongs to, and the card centres vertically in what is left.
 *
 * What to check here is not the sphere — it is `aria-hidden` decoration
 * (WDS-CORE-4) and carries nothing. It is that the disclosure SENTENCE is
 * present, because with the aside hidden it is the only thing still telling a
 * phone user, and every screen-reader user on one, that there is an AI here at
 * all. Exactly one of the two copies of that sentence is ever in the
 * accessibility tree; the e2e case pins the split in both directions.
 */
export const TaskOnlyPhone: Story = {
  globals: { viewport: { value: "taskOnly" } },
  render: () => <AuthStory profile={configured} />,
};

/**
 * The surface with the theme pinned dark, rather than left to the toolbar.
 *
 * Pinned because dark mode HERE is new: the theme used to be owned by the
 * authenticated chrome, so every unauthenticated surface rendered with no
 * `data-theme` at all and a reader whose OS is dark got the light sign-in page
 * however carefully the dark tokens were authored (`app/theme.ts`). It is
 * resolved and applied before React mounts now, which makes this rendering the
 * product's rather than a hypothetical.
 *
 * The Core is the part worth looking at: its halo, caustic and glass washes are
 * plain alpha over the page rather than a blend mode, because an additive glow
 * needs a dark canvas to read as light and turns to grey haze without one.
 */
export const DarkTheme: Story = {
  globals: { theme: "dark" },
  render: () => <AuthStory profile={configured} />,
};

/**
 * The surface in German — the A24 default locale, and the language every layout
 * rule on this screen was actually built for.
 *
 * It is a story rather than a toolbar switch because the German copy is what the
 * hard cases are made of, and none of them are visible in English: the limits
 * wrap where "Ich markiere jeden Wert, den ich geschrieben habe." is nearly twice
 * its English length, the legal line stacks because "Nutzungsbedingungen" alone is
 * 19 characters, and the typed statement's speed is derived from the string so
 * that the longer sentence still lands inside the 2000 ms budget. Reviewing only
 * the English page is reviewing the easy half.
 *
 * The provider buttons read German here for a reason that is NOT translation:
 * `oidc_providers[].label` is server-owned copy off the wire, and the client never
 * composes it. What speaks German is the ui-preview fixture standing in for a
 * German installation's server — a real installation's buttons say whatever its
 * operator configured, which is why `UnknownProvider` below keeps its own wording
 * in a story with an English catalog.
 */
export const GermanCopy: Story = {
  render: () => (
    <AuthStory
      profile={configured}
      locale="de"
      oidcProviders={[
        { key: "google", label: "Weiter mit Google" },
        { key: "microsoft", label: "Weiter mit Microsoft" },
      ]}
    />
  ),
};

// The emailed deep link, in the FRAGMENT: a fragment is never sent to a server,
// so the live single-use token cannot land in an access log or a Referer header.
// A story has to seed it before `AuthScreen`'s first render, because the screen
// reads the token in a `useState` initializer — `App.stories.tsx` seeds a hash
// route the same way.
const RESET_LINK = "#/reset-password?token=demo-reset-token";

/** The three refusals that arrive as a status, plus the one that arrives as nothing. */
type ResetFailure = 401 | 422 | 429 | "transport";

function ResetStory({ failure }: Readonly<{ failure?: ResetFailure }>) {
  globalThis.location.hash = RESET_LINK;
  const routes: RouteMap = {
    "GET /assistant/profile": () => jsonResponse(configured),
    "GET /auth/capabilities": () =>
      jsonResponse({
        password: true,
        password_reset: true,
        oidc_providers: [],
      }),
  };
  if (failure !== undefined) {
    routes["POST /auth/reset-password"] = () => {
      // The transport fault is the one failure that is not a status: a rejected
      // fetch (offline, DNS, CORS) never becomes a response at all, which is
      // exactly why it used to be indistinguishable from a dead token.
      if (failure === "transport") {
        throw new TypeError("Failed to fetch");
      }
      return jsonResponse({ title: "reset refused" }, failure);
    };
  }
  // Left unregistered when the story has no failure to show, so the stub's
  // fallback answers 200 and a submit lands on the real "Password updated"
  // notice — the honest behaviour for the form at rest.
  installFetchStub(routes);
  return (
    <StoryProviders>
      <AuthScreen onAuthed={() => undefined} />
    </StoryProviders>
  );
}

async function submitNewPassword(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  // Twelve characters is the form's minimum and the submit stays disabled below
  // it, so a shorter one would screenshot an untouched form and read as a pass.
  await userEvent.type(
    canvas.getByLabelText("New password"),
    "a decent new password",
  );
  await userEvent.click(
    canvas.getByRole("button", { name: "Set new password" }),
  );
}

/**
 * The reset card at rest, which is where all four failures below start.
 *
 * It is reached by the emailed link rather than by a control on this surface, so
 * without a story the one screen a user only ever meets from their inbox is the
 * one the catalog never shows.
 */
export const ResetLink: Story = {
  render: () => <ResetStory />,
};

/**
 * 401, the ONE token verdict: the server refuses to distinguish unknown from
 * used from expired, so a token cannot be probed, which is why the copy names
 * all three.
 *
 * This is the only one of the four that may offer a new link, and the offer is
 * the point of the story — for a spent token it is the remedy, and everywhere
 * else it destroys a link that still works.
 */
export const ResetLinkSpent: Story = {
  render: () => <ResetStory failure={401} />,
  play: async ({ canvasElement }) => {
    await submitNewPassword(canvasElement);
    await within(canvasElement).findByRole("button", {
      name: "Request a new link",
    });
  },
};

/**
 * 422: the PASSWORD was refused, not the link. The error belongs to the field
 * the user can act on, and no new link is offered, because the one they are
 * holding is still good.
 */
export const ResetPasswordRefused: Story = {
  render: () => <ResetStory failure={422} />,
  play: async ({ canvasElement }) => {
    await submitNewPassword(canvasElement);
    await within(canvasElement).findByText(/that password was refused/i);
  },
};

/**
 * 429: a budget refusal, so the answer is to wait. Nothing about the link
 * changed, and it is not offered for replacement.
 */
export const ResetRateLimited: Story = {
  render: () => <ResetStory failure={429} />,
  play: async ({ canvasElement }) => {
    await submitNewPassword(canvasElement);
    await within(canvasElement).findByText(/too many/i);
  },
};

/**
 * The fault that is neither the link's nor the user's, and the reason the other
 * three exist.
 *
 * Every reset failure used to render "That reset link is invalid, used, or
 * expired" above a "Request a new link" button. For a network blip that is false
 * twice over: the token is untouched, and the offered remedy SUPERSEDES it — so
 * a user whose request hit a bad moment was told their good link was dead and
 * then invited to destroy it. This copy says the link is still valid and offers
 * nothing but a retry.
 */
export const ResetServerFault: Story = {
  render: () => <ResetStory failure="transport" />,
  play: async ({ canvasElement }) => {
    await submitNewPassword(canvasElement);
    await within(canvasElement).findByText(/your link is still valid/i);
  },
};
