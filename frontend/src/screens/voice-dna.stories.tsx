// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  meRoute,
  type RouteMap,
  StoryProviders,
} from "./story-utils";
import { VoiceDnaCard } from "./voice-dna";

// The Settings Voice DNA card off canned profile/corpus reads — never a live
// call. Covers the owner with no profile yet, the owner still collecting (no
// derived voice text to show), a ready profile with a full corpus, a corpus row
// the build excluded from that corpus, and the read-only seat.
//
// A profile that exists is three cards, and every decision inside one is a
// settings ROW: the preferences editor is a stacked row in the voice card, the
// derived text sits behind a disclosure beside it, the corpus manifest takes
// the full width of its own card with the add verbs under it, and the build
// verb is a row whose description carries the distance still to go.

type VoiceProfile = components["schemas"]["VoiceProfile"];
type VoiceCorpusSource = components["schemas"]["VoiceCorpusSource"];
type VoiceCorpusSummary = components["schemas"]["VoiceCorpusSummary"];

const PROFILE: VoiceProfile = {
  id: "vp-1",
  owner_id: "u1",
  status: "ready",
  maturity: "building",
  quality_band: "good",
  voice_profile_md: "Short sentences. Concrete nouns. No hedging.",
  profile_version: 3,
  personality_md: "Warm but brief.",
  auto_learning_enabled: false,
  active_source_hash: "h1",
  candidate_version: null,
  last_built_at: "2026-07-01T00:00:00Z",
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  archived_at: null,
};

const SUMMARY: VoiceCorpusSummary = {
  total_words: 12400,
  target_words: 30000,
  maturity: "building",
  quality_band: "good",
  source_count: 2,
  register_words: { email: 9400, spoken: 3000 },
};

const SOURCE: VoiceCorpusSource = {
  id: "vs-1",
  origin: "manual",
  kind: "email",
  register: "email",
  weight: 1,
  source_label: "Sent mail, Q2",
  source_ref: "settings:paste:1",
  word_count: 9400,
  included: true,
  exclusion_reason: null,
  extractor_version: "1",
  occurred_at: "2026-06-01T00:00:00Z",
  retention_until: null,
  content_erased_at: null,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
  archived_at: null,
};

// A profile that exists but has not built a derived voice yet: status isn't
// "ready", so the card falls back to DerivedVoice's empty placeholder instead
// of quoting a voice_profile_md nobody has produced.
const COLLECTING_PROFILE: VoiceProfile = {
  ...PROFILE,
  status: "collecting",
  maturity: "collecting",
  quality_band: "thin",
  voice_profile_md: "",
  profile_version: 0,
  active_source_hash: null,
  last_built_at: null,
};

const COLLECTING_SUMMARY: VoiceCorpusSummary = {
  total_words: 420,
  target_words: 30000,
  maturity: "collecting",
  quality_band: "thin",
  source_count: 1,
  register_words: { general: 420 },
};

const COLLECTING_SOURCE: VoiceCorpusSource = {
  ...SOURCE,
  register: "general",
  word_count: 420,
};

// A source the build dropped (too short, a duplicate, …): still listed so its
// owner can see why it isn't counted, marked "excluded" rather than removed
// from the manifest outright.
const EXCLUDED_SOURCE: VoiceCorpusSource = {
  ...SOURCE,
  id: "vs-2",
  source_label: "Old boilerplate signature",
  included: false,
  exclusion_reason: "too_short",
  word_count: 40,
};

const LEARNING = {
  drafted: 6,
  accepted: 2,
  edited_sent: 3,
  rejected: 1,
  qualifying_source_count: 1,
  qualifying_words: 420,
  transformations: [],
};

// The card's whole subtree reads the profile, its corpus, its version history
// and its learning aggregate; a story serves all four so no panel renders an
// error state it did not mean to capture.
function voiceStory(routes: RouteMap, seat: "full" | "read" = "full") {
  return () => {
    installFetchStub({
      // Every control here mutates, so the card asks useCanWrite — grant AND
      // seat. Both halves are named because they fail the same way on screen,
      // and a read-only story has to withdraw both or it is not the posture it
      // claims to draw.
      "GET /me": meRoute(
        {
          voice_profile:
            seat === "read" ? ["read"] : ["read", "create", "update"],
        },
        { seat },
      ),
      "GET /voice-profiles/vp-1/versions": () => jsonResponse(emptyPage),
      "GET /voice-profiles/vp-1/deltas": () => jsonResponse(emptyPage),
      "GET /voice-profiles/vp-1/learning": () => jsonResponse(LEARNING),
      ...routes,
    });
    return (
      <StoryProviders>
        <VoiceDnaCard />
      </StoryProviders>
    );
  };
}

const meta: Meta = {
  // Under the tab's own name, exactly as SETTINGS_TABS spells it, so the three
  // voice leaves sit together instead of one of them under a root of its own.
  title: "Settings/You/Writing voice/Voice DNA",
};
export default meta;

type Story = StoryObj;

// No profile yet: listVoiceProfiles answers an empty page and the card offers
// the empty state together with the add control that mints the profile — the
// state an owner who skipped the onboarding voice step lands on.
export const Empty: Story = {
  render: voiceStory({
    "GET /voice-profiles": () => jsonResponse(emptyPage),
  }),
};

// A built profile: the derived voice, the corpus meter and its register mix,
// the preferences editor, and the build control.
export const Ready: Story = {
  render: voiceStory({
    "GET /voice-profiles": () =>
      jsonResponse({ data: [PROFILE], page: emptyPage.page }),
    "GET /voice-profiles/vp-1/sources": () =>
      jsonResponse({ data: [SOURCE], summary: SUMMARY }),
  }),
};

// A profile that exists but hasn't built a voice yet: the derived-voice panel
// falls back to its empty placeholder (voice_profile_md is "" pre-build)
// instead of quoting text nobody produced, while the corpus/build controls
// underneath are already live.
export const Collecting: Story = {
  render: voiceStory({
    "GET /voice-profiles": () =>
      jsonResponse({ data: [COLLECTING_PROFILE], page: emptyPage.page }),
    "GET /voice-profiles/vp-1/sources": () =>
      jsonResponse({
        data: [COLLECTING_SOURCE],
        summary: COLLECTING_SUMMARY,
      }),
  }),
};

// The one dark story the voice tree needs, and it is this state rather than
// `Ready`: below the 800-word floor the card draws a FloorMeter, and a FloorMeter
// is a bare `<progress>` element. voice-dna.css gives it a flex basis and nothing
// else, and no sheet in this app declares `color-scheme`, so the browser paints
// that widget in its own light-mode colours no matter what `data-theme` says —
// every other pixel on the page re-resolves through a token and this one cannot.
// The thin quality band and the register mix beside it are the rest of the frame.
export const CollectingDark: Story = {
  globals: { theme: "dark" },
  render: voiceStory({
    "GET /voice-profiles": () =>
      jsonResponse({ data: [COLLECTING_PROFILE], page: emptyPage.page }),
    "GET /voice-profiles/vp-1/sources": () =>
      jsonResponse({
        data: [COLLECTING_SOURCE],
        summary: COLLECTING_SUMMARY,
      }),
  }),
};

// A corpus row the build excluded (too short, a duplicate, …): still listed
// — never silently dropped — and marked so its owner can see why it doesn't
// count toward the meter.
export const ExcludedSource: Story = {
  render: voiceStory({
    "GET /voice-profiles": () =>
      jsonResponse({ data: [PROFILE], page: emptyPage.page }),
    "GET /voice-profiles/vp-1/sources": () =>
      jsonResponse({ data: [SOURCE, EXCLUDED_SOURCE], summary: SUMMARY }),
  }),
};

// A seat that may READ a voice but not change one. The posture is stated once at
// the top and the write affordances are then absent: the preferences box is
// readOnly with no Save under it, the corpus rows keep their facts and lose
// their remove verbs, and the build row keeps the sentence about how far the
// corpus still has to go while offering no verb to act on it.
export const ReadOnly: Story = {
  render: voiceStory(
    {
      "GET /voice-profiles": () =>
        jsonResponse({ data: [COLLECTING_PROFILE], page: emptyPage.page }),
      "GET /voice-profiles/vp-1/sources": () =>
        jsonResponse({
          data: [COLLECTING_SOURCE],
          summary: COLLECTING_SUMMARY,
        }),
    },
    "read",
  ),
};
