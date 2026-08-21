// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { Disclosure } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";
import { VoiceChangeLog, VoiceHistory } from "./voice-versions";

// Every build this profile has had, and what each one changed. A `candidate`
// is a finished build waiting for a human to accept or reject it — the state
// that makes this surface actionable rather than a log.
//
// Neither component titles itself any more: in the Builds card the version list
// is a stacked `SettingRow` and the delta log sits behind a `Disclosure`, so the
// row and the summary are what name them. These stories draw that composition
// rather than the bare list, because a list with no label is not a state the
// product has.
const ACTIVE = {
  id: "vv-3",
  profile_version: 3,
  status: "active",
  created_at: "2026-07-30T10:00:00Z",
};

const CANDIDATE = {
  id: "vv-4",
  profile_version: 4,
  status: "candidate",
  created_at: "2026-08-12T09:15:00Z",
};

const DELTA = {
  id: "vd-1",
  from_version: 3,
  to_version: 4,
  classification: "refinement",
  activation_outcome: "pending",
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

function stub(
  versions: Record<string, unknown>[],
  deltas: Record<string, unknown>[],
) {
  installFetchStub({
    "GET /me": meRoute({ voice_profile: ["read", "update"] }),
    "GET /voice-profiles/vp-1/versions": () => jsonResponse({ data: versions }),
    "GET /voice-profiles/vp-1/deltas": () => jsonResponse({ data: deltas }),
    "GET /voice-profiles/vp-1/learning": () => jsonResponse(LEARNING),
  });
}

function story(
  versions: Record<string, unknown>[],
  canEdit: boolean,
  deltas: Record<string, unknown>[] = [DELTA],
) {
  return () => {
    stub(versions, deltas);
    return (
      <StoryProviders>
        <Panel title="Builds">
          <PanelBody>
            <SettingList>
              <SettingRow
                label="Versions and learning"
                layout="stack"
                control={
                  <VoiceHistory
                    profileId="vp-1"
                    canEdit={canEdit}
                    onChanged={() => undefined}
                  />
                }
              />
              {/* Open here, closed in the product: a story that draws a shut
                  disclosure catalogues the chevron and nothing else. */}
              <Disclosure summary="What changed" open>
                <VoiceChangeLog profileId="vp-1" />
              </Disclosure>
            </SettingList>
          </PanelBody>
        </Panel>
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof VoiceHistory> = {
  title: "Settings/You/Writing voice/Build history",
  component: VoiceHistory,
};
export default meta;
type Story = StoryObj<typeof VoiceHistory>;

export const CandidateWaiting: Story = {
  render: story([CANDIDATE, ACTIVE], true),
};

export const OnlyActive: Story = {
  render: story([ACTIVE], true, []),
};

// The history is a read this seat keeps; accepting or rolling back a build is
// not. The rows stay and the verbs go.
export const ReadOnly: Story = {
  render: story([CANDIDATE, ACTIVE], false),
};
