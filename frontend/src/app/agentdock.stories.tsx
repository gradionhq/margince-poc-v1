// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { Card } from "../design-system/atoms";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { AgentDock } from "./agentdock";
import type { Route } from "./router";

/**
 * The agent, floating at the foot of the content column, and everything the
 * runtime can honestly say about it.
 *
 * It is the ONLY floating AI affordance in the product — the separate "Ask about
 * this" FAB in the opposite corner is gone, and its composer moved in here — so
 * these frames are also the record of what one glowing corner now has to carry.
 *
 * Three densities: at rest it says who the agent is and what state it is in; on
 * hover it grows the chevron that says there is more; on click it opens the
 * panel. A waiting-approvals count is exempt from that ladder and shows at rest,
 * because a signal you have to hover to find is not a signal.
 *
 * What the panel may CLAIM is the constraint under all of it. The runtime knows
 * routing is CONFIGURED; it has proved nothing about a provider being reachable,
 * so the state line says configuration and the status dot is the neutral token
 * rather than a success green. The example block is fenced off on its own ground
 * and labelled before it is read, because the line between what the runtime
 * knows and what is standing in for it is the most important boundary here.
 *
 * fullscreen: the dock is `position: absolute` against `.main`, so it centres on
 * the CONTENT COLUMN and moves with the sidebar. Every frame renders that column
 * inside the real `.app` grid — a dock rendered on the bare canvas would float
 * against nothing, centred on a box the product does not have.
 */
const meta: Meta<typeof AgentDock> = {
  title: "Shell/Agent dock",
  component: AgentDock,
  parameters: { layout: "fullscreen" },
};
export default meta;
type Story = StoryObj<typeof AgentDock>;

type ToolCatalog = components["schemas"]["AgentToolListResponse"];

const catalogTool = (
  name: string,
  tier: ToolCatalog["data"][number]["tier"],
): ToolCatalog["data"][number] => ({
  name,
  title: name,
  description: `what ${name} does`,
  tier,
  egress: false,
});

/**
 * The tools the installation actually carries: two that act on their own and one
 * that has to be confirmed, so the panel's summary ("2 auto · 1 confirm") could
 * not come from a miscount that happened to match the total.
 *
 * Routed rather than left to the stub's list-shaped fallback, because an empty
 * catalog is a REAL state with its own rendering — the row is absent, since "0
 * auto-execute" would be a claim about the installation that a pending request
 * has not earned. Every frame here is about the loaded case.
 */
function stubToolCatalog() {
  installFetchStub({
    "GET /agent-tools": () =>
      jsonResponse({
        data: [
          catalogTool("progress_deal", "auto_execute"),
          catalogTool("enrich_organization", "auto_execute"),
          catalogTool("send_email", "confirmation_required"),
        ],
      } satisfies ToolCatalog),
  });
}

/**
 * The record's name, seeded into the entry `useEntityName` reads.
 *
 * The composer's context line asks the same question the trail at the top of the
 * window asks, through one spelling (`useRouteSubject`) — so on a record route it
 * names the RECORD, and it names it from a read. Left unseeded the panel would
 * offer to answer questions about `p-anna`, which is true and unquotable, and
 * the frame would be showing the fallback rather than the sentence.
 */
function SeedRecordName({
  record,
  children,
}: Readonly<{
  record?: { id: string; name: string };
  children: ReactNode;
}>) {
  const client = useQueryClient();
  if (
    record &&
    client.getQueryData(["person", "ref", record.id]) === undefined
  ) {
    client.setQueryData(["person", "ref", record.id], record.name);
  }
  return <>{children}</>;
}

/**
 * The column the dock is positioned against, with a page under it.
 *
 * The sidebar is present and empty on purpose: it is what gives the grid its
 * first column, and the dock centring on the column rather than on the window is
 * only visible against something taking up that width. The page content is here
 * for the same reason — the dock HOVERS over the page rather than sitting in a
 * row of it, which is what its hairline, its elevated ground and its shadow are
 * for, and none of that reads over an empty canvas.
 */
function DockFrame({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <div className="app railexpanded">
      <div className="rail expanded" />
      <main className="main">
        <div className="scroll">
          <div className="wrap">
            <Card as="div">The page the agent is standing on.</Card>
          </div>
        </div>
        {children}
      </main>
    </div>
  );
}

function DockStory({
  route,
  approvalsWaiting,
  record,
}: Readonly<{
  route: Route;
  approvalsWaiting?: number;
  record?: { id: string; name: string };
}>) {
  stubToolCatalog();
  return (
    <StoryProviders>
      <SeedRecordName record={record}>
        <DockFrame>
          <AgentDock route={route} approvalsWaiting={approvalsWaiting} />
        </DockFrame>
      </SeedRecordName>
    </StoryProviders>
  );
}

/** Open the dock, the way a reader does. The panel's state is component-local —
 *  there is no prop that forces it open, and adding one would put a control in
 *  the product that exists only for the catalog. */
async function openDock({ canvasElement }: { canvasElement: HTMLElement }) {
  const canvas = within(canvasElement);
  await userEvent.click(canvas.getByRole("button", { name: /Margince AI/ }));
}

/**
 * At rest, with nothing waiting.
 *
 * The whole of it is the orb, the agent's name, and one word of state. The
 * chevron is not drawn: it is the invitation to open the dock, and an invitation
 * is only needed once the pointer is here. Its space is reserved in both states,
 * so revealing it never nudges the pill.
 */
export const Closed: Story = {
  name: "at rest — a list route",
  render: () => <DockStory route={{ screen: "deals" }} />,
};

/**
 * Open on a list route, where the composer is scoped to the SCREEN.
 *
 * Read down, the panel is narrowest first: who you are talking to, the question
 * scoped to where you are standing, then the full Ask surface for anything
 * wider, then what is waiting and what the agent is allowed to do, and last the
 * block that is only example data. The composer leads because it is what a
 * reader opening this dock came to do — "Margince AI" as a destination only
 * reads as "wider than this" once the narrower thing is visible above it.
 *
 * The approvals row shows a zero here rather than being absent: nothing is
 * waiting, which is an answer, and it is a different state from having no answer
 * to give.
 */
export const Open: Story = {
  name: "open — the composer names the screen",
  play: openDock,
  render: () => <DockStory route={{ screen: "deals" }} approvalsWaiting={0} />,
};

/**
 * Open on a record, where the composer is scoped to THAT RECORD and names it.
 *
 * This is the frame the FAB's absorption has to be judged on: the record-scoped
 * ask is the thing the second floating corner used to carry, and the context
 * line has to name the record in the same words the trail at the top of the
 * window uses. The scope line under it is load-bearing rather than a caption —
 * the agent reads only the RBAC ∩ Passport intersection, and nothing on this
 * panel may imply more.
 */
export const OpenOnRecord: Story = {
  name: "open — the composer names the record",
  play: openDock,
  render: () => (
    <DockStory
      route={{ screen: "contacts", id: "p-anna" }}
      approvalsWaiting={2}
      record={{ id: "p-anna", name: "Anna Weber" }}
    />
  ),
};

/**
 * Open on the full Ask surface, where the composer is deliberately ABSENT.
 *
 * A scoped composer here would be a smaller copy of the page behind it, offering
 * to ask about the screen the reader is already asking on. What is left is what
 * the dock is for everywhere else: the way back to this surface, what is waiting,
 * what the agent may do, and the fenced example block.
 */
export const OpenOnAskSurface: Story = {
  name: "open on #/ai — no composer",
  play: openDock,
  render: () => <DockStory route={{ screen: "ai" }} approvalsWaiting={0} />,
};

/**
 * Something is waiting, at rest.
 *
 * The count is on the trigger before anything is opened, because approvals are
 * the one thing here that cannot wait for a hover. It is part of the trigger's
 * accessible NAME rather than a badge alone — a number only a sighted reader can
 * count is half a signal — and the panel behind it prints the same figure in the
 * accent, which is the row's whole reason for existing when it is not zero.
 */
export const Waiting: Story = {
  name: "at rest — approvals waiting",
  render: () => <DockStory route={{ screen: "deals" }} approvalsWaiting={3} />,
};

/**
 * Open, in dark.
 *
 * Three token layers stack here — the page ground, the panel's elevated card
 * over it, and the tinted scope line and the fenced example block inside that —
 * and each is a `color-mix()` that follows the dark accent lift separately. A
 * panel that separates cleanly from the page in light can flatten into it in
 * dark, and the example block's dashed fence is the first thing to go.
 */
export const OpenDark: Story = {
  name: "open (dark)",
  globals: { theme: "dark" },
  play: openDock,
  render: () => (
    <DockStory
      route={{ screen: "contacts", id: "p-anna" }}
      approvalsWaiting={3}
      record={{ id: "p-anna", name: "Anna Weber" }}
    />
  ),
};
