// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../../app/mefixture";
import { configuredAiProfile } from "../onboarding.stories.fixtures";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  type RouteMap,
  StoryProviders,
} from "../story-utils";
import { initialConversationState } from "./conversation-machine";
import type { ConversationState, ThreadEntry } from "./conversation-types";
import { ConversationThread } from "./thread";
import { ConversationWorkbench, WorkbenchEntranceScope } from "./workbench";

// The shell all four conversation acts render inside: brand line, orb, rail of
// steps, the runtime footer, and the split thread/artifact body. Everything on
// it comes from two requests — the configured model (GET /ai/profile) and who
// is signed in (GET /me) — so the stories are the states of those two answers.
//
// The signed-in chip at the rail's foot is the one that has bitten before: an
// unrouted session probe reads as a MALFORMED session, not as an absent one,
// and the foot then draws an anonymous chip in a story that claims a person.
// Every story here routes the probe explicitly and says which answer it means.

const state: ConversationState = {
  ...initialConversationState,
  act: "company",
  phase: "co.review",
};

const thread: readonly ThreadEntry[] = [
  {
    kind: "narration",
    id: "1:pages:5",
    i18nKey: "ob.conv.read.pages",
    params: { pages: 5 },
  },
  {
    kind: "narration",
    id: "2:field:industry",
    i18nKey: "ob.conv.read.learnedField",
    params: { value: "Industrial robotics" },
    paramKeys: { field: "ob.field.industry" },
    findingIds: ["industry"],
  },
  {
    kind: "outcome",
    id: "3:company:confirmed",
    i18nKey: "ob.conv.company.confirmed",
    tone: "success",
  },
];

function Shell({ session }: Readonly<{ session: RouteMap[string] }>) {
  installFetchStub({
    "GET /me": session,
    "GET /ai/profile": () => jsonResponse(configuredAiProfile),
  });
  return (
    <StoryProviders>
      {/* Each story gets its own entrance scope, so every capture shows the
          workspace assembling once — the shell's first appearance — rather
          than inheriting a "already shown" flag from the story before it. */}
      <WorkbenchEntranceScope>
        <ConversationWorkbench
          core="listening"
          railState={state}
          status="Ready"
          artifact={
            <div className="mw-review ob-conv-artifact">
              <div className="mw-review-heading">
                <span>Live artifact</span>
                <h2>Company profile</h2>
                <p>
                  The work surface each act fills; here it stands in for one.
                </p>
              </div>
            </div>
          }
        >
          <div className="mw-thread">
            <ConversationThread
              entries={thread}
              pendingQuestionId={null}
              onAnswer={() => {}}
            />
          </div>
        </ConversationWorkbench>
      </WorkbenchEntranceScope>
    </StoryProviders>
  );
}

const meta: Meta<typeof Shell> = {
  title: "Onboarding/Conversation workbench",
  component: Shell,
  parameters: { layout: "fullscreen" },
};
export default meta;

type Story = StoryObj<typeof Shell>;

// The ordinary case: the session resolved and carries a display name, so the
// rail's foot names the person and keys their chip's tint on their address —
// the stable identity, so a later rename does not move them to a new colour.
export const SignedIn: Story = {
  render: () => <Shell session={meRoute({})} />,
};

// A seat that never set a display name. The name falls back to the address,
// which is why the monogram splits on `@` and `.` as well as whitespace:
// "jana.roth@gradion.test" reads as JR here, not as a lone J shared with
// every colleague whose address starts the same way.
export const NameFromAddress: Story = {
  render: () => {
    const me = meFixture({});
    return (
      <Shell
        session={() =>
          jsonResponse({
            ...me,
            user: {
              ...me.user,
              display_name: "",
              email: "jana.roth@gradion.test",
            },
          })
        }
      />
    );
  },
};

// The probe has not landed yet. The rail is fully usable meanwhile — steps,
// status, runtime, and the theme control at the foot — and the person row is
// simply absent rather than drawn as an anonymous stand-in for nobody.
export const IdentityUnresolved: Story = {
  render: () => (
    <Shell session={() => new Promise<Response>(() => undefined)} />
  ),
};
