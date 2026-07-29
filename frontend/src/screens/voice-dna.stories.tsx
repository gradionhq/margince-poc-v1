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
// call. The two states worth capturing are the owner who has no profile yet and
// the owner whose corpus is already feeding a built voice.

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
  title: "screens/voice-dna",
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
