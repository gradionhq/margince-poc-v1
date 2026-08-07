// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";
import { VoiceDnaCard } from "./voice-dna";

// The Settings Voice DNA card off canned profile/corpus reads — never a live
// call. Covers the owner with no profile yet, the owner still collecting (no
// derived voice text to show), a ready profile with a full corpus, and a
// corpus row the build excluded from that corpus.

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
function voiceStory(routes: RouteMap) {
  return () => {
    installFetchStub({
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
  title: "Screens/voice-dna",
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
