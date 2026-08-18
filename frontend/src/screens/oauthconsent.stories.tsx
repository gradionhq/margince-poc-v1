// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { OAuthConsent } from "./oauthconsent";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The screen where a human hands an agent their own authority. It reads its
// whole request out of the redirect FRAGMENT — every authorize param plus the
// single-use `consent` nonce the server armed against an HttpOnly cookie — so a
// story arrives the way the server sends a visitor: by setting the hash before
// the component mounts (OAuthConsent reads it once per mount).
//
// The nonce is stubbed, not earned. Nothing here holds the cookie half of the
// pair, so no story submits: the forms are rendered for review, and the flow
// itself belongs to oauthconsent.test.tsx, which drives the real POST.
//
// The rule the screen keeps, and the reason the stories split the way they do: a
// submittable form is rendered ONLY where a nonce is actually held. A refusal the
// human's next choice can fix carries its nonce back and keeps the selector; a
// terminal one hands back the request alone and presents no action at all.
const NONCE = "n-4f21c8";

// The audience param rides every fixture: `resource` is the one authorize param
// whose loss would be silent, since the flow still completes bound to the wrong
// audience.
function consentHash(overrides: Record<string, string> = {}): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: "https://agent.example/mcp",
    redirect_uri: "https://agent.example/oauth/callback",
    scope: "crm.read crm.write",
    code_challenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
    code_challenge_method: "S256",
    resource: "https://margince.example/mcp",
    state: "night-state",
    ...overrides,
  });
  return `#/oauth-consent?${params.toString()}`;
}

const twoPassports = {
  client_name: "Fleet Copilot",
  offline: false,
  passports: [
    {
      id: "9f3a1c20-0000-4000-8000-0000000000a1",
      label: "Desk assistant",
      scopes: ["crm.read", "crm.write"],
      expires_at: "2026-12-31T00:00:00Z",
    },
    // No label at all — the server maps a NULL column to "" rather than
    // failing the read, and on the one screen where knowing WHICH credential
    // you are lending is the whole point, the id fragment is what still tells
    // two of them apart.
    {
      id: "3b7e05d4-0000-4000-8000-0000000000a2",
      label: "",
      scopes: ["crm.read"],
      expires_at: "2026-09-30T00:00:00Z",
    },
  ],
};

// A client asking to keep working when the human is not present. The extra line
// is the whole difference, and it is the one a reader must not miss.
const offlineClient = { ...twoPassports, offline: true };

const noPassports = {
  client_name: "Fleet Copilot",
  offline: false,
  passports: [],
};

// `payload` is the consent-request read. It is routed even on the states that
// never fire it (the refusals below hold no nonce, so the query stays disabled)
// so the map reads as the screen's full surface rather than a list of what each
// story happened to need.
function story(hash: string, payload: unknown, status = 200) {
  return () => {
    globalThis.location.hash = hash;
    installFetchStub({
      // An ordinary seat, not an admin: authorizing an agent is a decision any
      // signed-in human makes about their OWN authority, and the grant it lends
      // is capped by the seat they already hold.
      "GET /me": meRoute({}, { roles: ["rep"] }),
      "GET /oauth/consent-request": () => jsonResponse(payload, status),
    });
    return (
      <StoryProviders>
        <OAuthConsent />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof OAuthConsent> = {
  title: "Agents/Authorize access",
  component: OAuthConsent,
};
export default meta;
type Story = StoryObj<typeof OAuthConsent>;

// The consent prompt: who is asking, where the authorization is sent, which
// passport is being lent, the scopes that ride with it, and when it expires.
// Authorize and Deny are two separate forms, so neither can be reached by
// accident from the other.
export const ConsentPrompt: Story = {
  render: story(consentHash({ consent: NONCE }), twoPassports),
};

// The prompt in dark. This is a decision screen with no rail around it, so the
// card IS the page, and four things have to stay legible against a darker
// ground: the Select's field, the scope chips (a tinted badge over the card
// rather than over the page), and the two submits — an Authorize that must read
// as the loud one and a Deny that must still read as a real control.
export const ConsentPromptDark: Story = {
  globals: { theme: "dark" },
  render: story(consentHash({ consent: NONCE }), twoPassports),
};

// Same prompt, offline access requested — the client wants to act while the
// human is away.
export const OfflineAccessRequested: Story = {
  render: story(consentHash({ consent: NONCE }), offlineClient),
};

// The human holds no passport yet. There is no approve control to disable, so
// the screen offers the only move that exists — mint one — and stashes the
// re-entry URL for the trip back.
export const NoPassportToLend: Story = {
  render: story(consentHash({ consent: NONCE }), noPassports),
};

// A RECOVERABLE refusal: the passport that was chosen can no longer be lent, but
// the armed pair is untouched, so the nonce comes back with the marker and the
// selector is offered again inside the same window.
export const UnlendablePassport: Story = {
  render: story(
    consentHash({ consent: NONCE, error: "unlendable_passport" }),
    twoPassports,
  ),
};

// A TERMINAL refusal, and the one that most often cannot have the
// consent-request read either: invalid_request's likeliest cause is a client
// that went unknown, disabled or deleted, which 404s that same fetch. So this
// state is rendered ahead of the read, with copy that names no client and a way
// out of a rail-less dead end.
export const InvalidRequest: Story = {
  render: story(
    consentHash({ error: "invalid_request" }),
    { title: "Not Found", status: 404 },
    404,
  ),
};

// The request is spent. No nonce comes back, so nothing here could be submitted
// — and a state with no working action presents none. The consent-request read
// is routed as the same 404 for the same reason: this state must render without
// it.
export const StaleConsent: Story = {
  render: story(
    consentHash({ error: "stale_consent" }),
    { title: "Not Found", status: 404 },
    404,
  ),
};
