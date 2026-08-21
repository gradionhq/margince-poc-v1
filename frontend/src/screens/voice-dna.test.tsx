/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { VoiceDnaCard } from "./voice-dna";

// The Settings Voice DNA card is the ONLY surface outside onboarding where a
// voice can be started. Its empty state promises samples can be added "below",
// so the add control has to be there — and the first add must mint the one
// profile the owner is allowed to have, never a second one beside the
// onboarding flow's.

type VoiceProfile = components["schemas"]["VoiceProfile"];
type VoiceCorpusSummary = components["schemas"]["VoiceCorpusSummary"];

const PROFILE: VoiceProfile = {
  id: "vp-1",
  owner_id: "u1",
  status: "collecting",
  maturity: "collecting",
  quality_band: "thin",
  voice_profile_md: "",
  profile_version: 0,
  personality_md: "",
  auto_learning_enabled: false,
  active_source_hash: null,
  candidate_version: null,
  last_built_at: null,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  archived_at: null,
};

const SUMMARY: VoiceCorpusSummary = {
  total_words: 420,
  target_words: 30000,
  maturity: "collecting",
  quality_band: "thin",
  source_count: 1,
  register_words: { general: 420 },
};

const SOURCE: components["schemas"]["VoiceCorpusSource"] = {
  id: "vs-1",
  origin: "manual",
  kind: "other",
  register: "general",
  weight: 1,
  source_label: "Pasted writing",
  source_ref: "settings:paste:1",
  word_count: 420,
  included: true,
  exclusion_reason: null,
  extractor_version: "1",
  occurred_at: "2026-07-01T00:00:00Z",
  retention_until: null,
  content_erased_at: null,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  archived_at: null,
};

const STATS: components["schemas"]["VoiceIngestStats"] = {
  input_words: 420,
  kept_words: 420,
  kept_turns: 0,
  discarded_turns: 0,
  speakers_seen: [],
};

const emptyPage = { data: [], page: { next_cursor: null, has_more: false } };

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// Every write on this surface is one grant server-side (`voice_profile:update`,
// plus `create` for minting the first profile), so the seat the stub answers
// with decides whether the card offers any control at all.
const VOICE_EDITOR = {
  voice_profile: ["read", "create", "update"],
} as const;

// A stub that behaves like the server does across the whole mint: the profile
// list answers empty until POST /voice-profiles creates one, so a card that
// minted twice would be visible as two creates rather than hidden behind a
// canned response.
function stubApi(grants: GrantSpec = VOICE_EDITOR) {
  const calls: string[] = [];
  let profile: VoiceProfile | null = null;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const path = new URL(request.url).pathname.replace(/^\/v1/, "");
      calls.push(`${request.method} ${path}`);
      if (path === "/me") {
        return jsonResponse(meFixture({ allow: grants }));
      }
      if (path === "/voice-profiles") {
        if (request.method === "POST") {
          profile = PROFILE;
          return jsonResponse(PROFILE, 201);
        }
        return jsonResponse({
          data: profile ? [profile] : [],
          page: emptyPage.page,
        });
      }
      // Every source is previewed before it is written, pasted text included:
      // the server is the one that says whether writing carries speakers.
      if (path === "/voice-profiles/vp-1/sources/preview") {
        return jsonResponse({
          detected_format: "txt",
          total_words: 420,
          speakers: [],
          unattributed_words: 420,
          ingestible_as_transcript: false,
        });
      }
      if (path === "/voice-profiles/vp-1/sources") {
        if (request.method === "POST") {
          return jsonResponse(
            { source: SOURCE, summary: SUMMARY, ingest_stats: STATS },
            201,
          );
        }
        return jsonResponse({ data: [SOURCE], summary: SUMMARY });
      }
      return jsonResponse(emptyPage);
    }),
  );
  return calls;
}

function render(ui: ReactNode) {
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The paste box is a form — a whole email typed into one box and then
// committed — so the settings row keeps the verb and the dialog keeps the form.
// A test whose subject is that box opens the dialog first and scopes its
// queries to it; the claims themselves are unchanged.
async function openFirstSample(): Promise<HTMLElement> {
  await userEvent.click(
    await screen.findByRole("button", { name: "Paste your first sample" }),
  );
  return screen.getByRole("dialog");
}

describe("the Settings Voice DNA card with no profile yet", () => {
  it("offers the add control the empty state points at", async () => {
    stubApi();
    render(<VoiceDnaCard />);
    expect(await screen.findByText("No Voice DNA yet")).toBeTruthy();
    // The row names the sample and carries the verb that asks for one.
    expect(screen.getByText("Your first writing sample")).toBeTruthy();

    const dialog = await openFirstSample();
    expect(
      within(dialog).getByPlaceholderText(
        "Paste an email, post, or anything you've written…",
      ),
    ).toBeTruthy();
    expect(
      within(dialog).getByRole("button", {
        name: "Add it and start my Voice DNA",
      }),
    ).toBeTruthy();
  });

  it("mints exactly one profile on the first add and then shows the build control", async () => {
    const calls = stubApi();
    render(<VoiceDnaCard />);
    const dialog = await openFirstSample();
    await userEvent.type(
      within(dialog).getByPlaceholderText(
        "Paste an email, post, or anything you've written…",
      ),
      "Short sentences. Concrete nouns.",
    );
    await userEvent.click(
      within(dialog).getByRole("button", {
        name: "Add it and start my Voice DNA",
      }),
    );

    // The build control only exists inside the body that requires a profile,
    // so its appearance is the proof the card left the dead end.
    expect(
      await screen.findByRole("button", { name: /Rebuild Voice DNA/ }),
    ).toBeTruthy();
    expect(calls.filter((c) => c === "POST /voice-profiles")).toHaveLength(1);
    expect(calls).toContain("POST /voice-profiles/vp-1/sources");
  });

  it("keeps the add disabled until there is something to add", async () => {
    stubApi();
    render(<VoiceDnaCard />);
    const dialog = await openFirstSample();
    const add = within(dialog).getByRole("button", {
      name: "Add it and start my Voice DNA",
    });
    expect(add.hasAttribute("disabled")).toBe(true);
  });

  // A seat that may READ a Voice DNA but not change one is not offered a
  // control the server would refuse. The card keeps its place and says which
  // posture it is in, once — the design-system rule for a readable surface
  // whose write affordances are absent.
  it("withholds the first-sample control from a seat that cannot create one", async () => {
    stubApi({ voice_profile: ["read"] });
    render(<VoiceDnaCard />);
    expect(await screen.findByText("No Voice DNA yet")).toBeTruthy();
    expect(
      screen.getByText(/you do not have permission to change your Voice DNA/i),
    ).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: "Add it and start my Voice DNA" }),
    ).toBeNull();
  });

  // An owner with no voice yet has one thing to do. Splitting the surface into
  // a card per subject would hand them headings over empty bodies — a
  // description of a profile that does not exist.
  it("stays one card, naming no subject the owner has nothing in yet", async () => {
    stubApi();
    render(<VoiceDnaCard />);
    expect(await screen.findByText("No Voice DNA yet")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Voice DNA" })).toBeTruthy();
    for (const absent of ["Writing samples", "Builds"]) {
      expect(screen.queryByRole("heading", { name: absent })).toBeNull();
    }
    // The preferences and the derived text are rows inside the voice card once
    // there is a profile; with none, neither is named at all.
    for (const absent of ["Your preferences", "Your derived voice"]) {
      expect(screen.queryByText(absent)).toBeNull();
    }
  });
});

// A profile that exists answers three questions — what the voice IS, what it is
// built FROM, and what its builds have DONE — so it is three cards, and every
// decision inside one is a row with its own label. A reader looking for the
// rebuild button finds a heading that says which card it is in, and then a row
// that names it.
describe("the Settings Voice DNA card with a profile", () => {
  function stubProfile() {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const path = new URL(request.url).pathname.replace(/^\/v1/, "");
        if (path === "/me") {
          return jsonResponse(meFixture({ allow: VOICE_EDITOR }));
        }
        if (path === "/voice-profiles") {
          return jsonResponse({ data: [PROFILE], page: emptyPage.page });
        }
        if (path === "/voice-profiles/vp-1/sources") {
          return jsonResponse({ data: [SOURCE], summary: SUMMARY });
        }
        return jsonResponse(emptyPage);
      }),
    );
  }

  it("gives every subject its own named card", async () => {
    stubProfile();
    render(<VoiceDnaCard />);

    for (const heading of ["Voice DNA", "Writing samples", "Builds"]) {
      expect(
        await screen.findByRole("heading", { name: heading }),
      ).toBeTruthy();
    }
    // The preferences and the derived text belong to the voice itself, as rows
    // inside its card rather than as two more header bands. "Your derived
    // voice" is there only while the profile is not ready — a ready one is
    // described by the insights above instead.
    const voice = (
      await screen.findByRole("heading", { name: "Voice DNA" })
    ).closest("section");
    if (!voice) {
      throw new Error("the voice heading is not inside a card");
    }
    expect(within(voice).getByText("Your preferences")).toBeTruthy();
    expect(within(voice).getByText("Your derived voice")).toBeTruthy();
  });

  it("keeps the corpus and the way to add to it in one card", async () => {
    stubProfile();
    render(<VoiceDnaCard />);

    // The manifest is the subject of the card and stays in it at full width;
    // the box that adds to it is a form, so the card carries the verb. Both
    // are here, so the reader who just read "420 of 30,000 words" can act on
    // it without moving.
    const corpus = (
      await screen.findByRole("heading", { name: "Writing samples" })
    ).closest("section");
    if (!corpus) {
      throw new Error("the corpus heading is not inside a card");
    }
    expect(await within(corpus).findByText(/420 of 30,000 words/)).toBeTruthy();
    expect(
      within(corpus).getByRole("button", { name: "Paste writing" }),
    ).toBeTruthy();
    expect(
      within(corpus).getByRole("button", { name: /Choose files/ }),
    ).toBeTruthy();
  });

  // Every box on this card is a real form control with a real name. A
  // placeholder is not one: it is gone the moment a character is typed, so the
  // field a screen reader was told about loses its name exactly when its
  // content starts to matter.
  it("names every writing box without relying on its placeholder", async () => {
    stubProfile();
    render(<VoiceDnaCard />);
    // The preferences box takes its name from the row that names the decision,
    // so the words on screen and the name it announces are one string.
    expect(
      await screen.findByRole("textbox", { name: "Your preferences" }),
    ).toBeTruthy();
    // The add box draws its own label inside the dialog, and the label is tied
    // to the control — and it names the dialog too.
    await userEvent.click(
      screen.getByRole("button", { name: "Paste writing" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Add sample" });
    expect(within(dialog).getByLabelText("Add sample")).toBeTruthy();
  });
});

// A build is the longest-running act on this card and the one a reader is
// likeliest to report as broken, so what the card says about a failed one is
// the sentence that gets quoted. Keeping the failure itself readable on the
// console belongs to the client's mutation sink (app/queryclient.test).
describe("a build that fails", () => {
  // `collecting` is the server's "too thin to build" verdict and disables the
  // control; a provisional profile is the smallest fixture whose build button
  // can actually be pressed.
  const BUILDABLE: VoiceProfile = { ...PROFILE, maturity: "provisional" };

  function stubBuild(build: () => Response) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const path = new URL(request.url).pathname.replace(/^\/v1/, "");
        if (path === "/me") {
          return jsonResponse(meFixture({ allow: VOICE_EDITOR }));
        }
        if (path === "/voice-profiles") {
          return jsonResponse({ data: [BUILDABLE], page: emptyPage.page });
        }
        if (path === "/voice-profiles/vp-1/sources") {
          return jsonResponse({ data: [SOURCE], summary: SUMMARY });
        }
        if (path === "/voice-profiles/vp-1/builds") {
          return build();
        }
        return jsonResponse(emptyPage);
      }),
    );
  }

  async function pressRebuild() {
    render(<VoiceDnaCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Rebuild Voice DNA/ }),
    );
  }

  it("shows the shared line and never our own internals", async () => {
    stubBuild(() => {
      throw new TypeError("Cannot read properties of undefined");
    });

    await pressRebuild();

    expect(
      await screen.findByText("The request failed. No cause reported."),
    ).toBeTruthy();
    // Our own internals never become the reader's sentence.
    expect(screen.queryByText(/Cannot read properties/)).toBeNull();
  });

  it("shows the server's own cause when the server composed one", async () => {
    stubBuild(() =>
      jsonResponse(
        { code: "budget_exhausted", detail: "The AI budget is spent." },
        429,
      ),
    );

    await pressRebuild();

    expect(await screen.findByText("The AI budget is spent.")).toBeTruthy();
  });
});
