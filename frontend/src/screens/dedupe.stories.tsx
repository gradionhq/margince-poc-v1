// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../app/mefixture";
import { DedupeScreen } from "./dedupe";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The review queue, whose whole job is to show a reviewer what the detector saw
// before they merge two records together. The three signals — agree, conflict,
// one side only — are the reason this screen exists, so each story below carries
// all three: a queue in which every field agrees is a queue that never needed a
// reviewer.
//
// The screen also owns its own stylesheet now. It did not for a long time, and
// the symptom was invisible in the app and total here: mounted alone, with no
// other screen to have pulled the sheet into the bundle, it drew as a wireframe.
const evidence = [
  {
    field: "full_name",
    left_value: "Dana Buyer",
    right_value: "Dana Buyer",
    signal: "agree",
  },
  {
    field: "email",
    left_value: "dana@acme.example",
    right_value: "d.buyer@acme.example",
    signal: "collide",
  },
  {
    field: "phone",
    left_value: "+49 30 1234567",
    right_value: null,
    signal: "one_sided",
  },
];

function story(candidates: Record<string, unknown>[]) {
  return () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture({ allow: {} })),
      "GET /dedupe/candidates": () =>
        jsonResponse({ data: candidates, page: { next_cursor: null } }),
      // A reviewer decides which record survives, so both sides have to read as
      // records. Left unrouted, EntityRef falls back to the raw id and the
      // catalog shows somebody comparing two UUIDs — a screen nobody could act
      // on, under the name of one they can. The route keys are concrete paths
      // because the stub matches the URL it was actually given, not a template.
      // A person's name is `full_name`; an organization's is `display_name`.
      // EntityRef reads a differently-named field per kind, and the wrong one
      // resolves to null, which it renders as the raw id — the same output as
      // routing nothing at all.
      [`GET /people/${LEFT}`]: () =>
        jsonResponse({ id: LEFT, full_name: "Dana Buyer" }),
      [`GET /people/${RIGHT}`]: () =>
        jsonResponse({ id: RIGHT, full_name: "Dana K. Buyer" }),
      [`GET /organizations/${LEFT}`]: () =>
        jsonResponse({ id: LEFT, display_name: "Acme GmbH" }),
      [`GET /organizations/${RIGHT}`]: () =>
        jsonResponse({ id: RIGHT, display_name: "Acme Gesellschaft mbH" }),
    });
    return (
      <StoryProviders>
        <DedupeScreen />
      </StoryProviders>
    );
  };
}

const LEFT = "00000000-0000-7000-8000-000000000001";
const RIGHT = "00000000-0000-7000-8000-000000000002";

const candidate = {
  id: "dc1",
  entity_type: "person",
  left_id: LEFT,
  right_id: RIGHT,
  confidence: 0.87,
  evidence,
  status: "open",
};

const meta: Meta = {
  title: "Records/Duplicates",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

export const OnePair: Story = { render: story([candidate]) };

// Two pairs, because a queue is read as a queue: the reviewer's question is
// which pair to take first, and confidence alone does not answer it.
export const Queue: Story = {
  render: story([
    candidate,
    {
      ...candidate,
      id: "dc2",
      entity_type: "organization",
      confidence: 0.61,
      evidence: [
        {
          field: "domain",
          left_value: "acme.example",
          right_value: "acme-gmbh.example",
          signal: "collide",
        },
        {
          field: "name",
          left_value: "Acme GmbH",
          right_value: "Acme GmbH",
          signal: "agree",
        },
      ],
    },
  ]),
};

// A signal the wire carries that this release has no word for. The field is
// typed as a plain string, not a closed enum, so it renders as itself: a signal
// we cannot name is still one the detector acted on.
export const UnknownSignal: Story = {
  render: story([
    {
      ...candidate,
      id: "dc3",
      evidence: [
        {
          field: "vat_id",
          left_value: "DE123456789",
          right_value: "DE123456789",
          signal: "normalised_match",
        },
      ],
    },
  ]),
};

export const EmptyQueue: Story = { render: story([]) };
