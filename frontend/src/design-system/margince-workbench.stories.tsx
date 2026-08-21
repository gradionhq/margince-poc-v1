// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { SunMoon } from "lucide-react";
import type { ComponentProps } from "react";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { Button, Card } from "./atoms";
import {
  MarginceWorkbench,
  type WorkbenchRuntimeLabels,
  type WorkbenchStep,
} from "./margince-workbench";

/**
 * The two-pane working surface onboarding runs in: the conversation on one
 * side, the artifact assembling itself on the other.
 *
 * Every word on it arrives as a prop. The design system owns no copy, so the
 * strings below are this story's own catalog standing in for the caller's —
 * they are the English onboarding runs with, which is what makes the stories a
 * picture of production rather than of lorem.
 *
 * fullscreen, because the shell IS the viewport during setup: it sizes itself
 * to 100dvh and the canvas frame would clip the rail's foot, which is the band
 * these stories are about.
 */
const meta = {
  title: "Design System/Workbench",
  component: MarginceWorkbench,
  parameters: { layout: "fullscreen" },
  decorators: [
    (Story) => (
      // Pinned to English rather than detected: the step list says each stop's
      // state in words for a screen reader, and a catalog that rendered in
      // whatever language the reviewer's browser asks for compares against
      // nothing.
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
} satisfies Meta<typeof MarginceWorkbench>;
export default meta;

type Story = StoryObj<typeof meta>;
type AiRunSummary = components["schemas"]["AiRunSummary"];

// A run that has actually happened, so the transparency chip shows a spend, a
// token count and a route instead of its "awaiting the first model call" line.
// Two models on two providers, because that is the configuration the plain
// footer sentence is FOR — one line a reader can parse, over ids they cannot.
const RUNTIME: AiRunSummary = {
  currency: "USD",
  call_attempts: 4,
  tokens_in: 18_420,
  tokens_out: 2_615,
  latency_ms: 4_180,
  estimated_cost_microusd: 12_400,
  unpriced_calls: 0,
  models: [
    {
      task: "company-read",
      tier: "premium",
      provider: "deepseek",
      configured_model: "deepseek-chat",
      served_model: "deepseek-chat",
      call_attempts: 3,
      tokens_in: 16_100,
      tokens_out: 2_240,
      cached_tokens: 0,
      cache_write_tokens: 0,
      reasoning_tokens: 0,
      latency_ms: 3_450,
      estimated_cost_microusd: 11_100,
      unpriced_calls: 0,
      last_used_at: "2026-08-17T09:14:00Z",
    },
    {
      task: "extract",
      tier: "local-small",
      provider: "ollama",
      configured_model: "llama3.1:8b",
      served_model: "llama3.1:8b",
      call_attempts: 1,
      tokens_in: 2_320,
      tokens_out: 375,
      cached_tokens: 0,
      cache_write_tokens: 0,
      reasoning_tokens: 0,
      latency_ms: 730,
      estimated_cost_microusd: 1_300,
      unpriced_calls: 0,
      last_used_at: "2026-08-17T09:14:22Z",
    },
  ],
};

const RUNTIME_LABELS: WorkbenchRuntimeLabels = {
  configured: "Configured AI",
  used: "Models used in this task",
  route: "Task · tier · provider",
  calls: "AI calls",
  tokens: "Tokens",
  latency: "Model latency",
  estimatedCost: "Estimated provider cost",
  partial: "Partial · unpriced usage exists",
  awaiting: "Shown after my first model call",
  unavailable: "Not available yet",
  chip: "What is answering, and what it costs",
  answering: "What is answering right now",
  scope: "This run only. The full log is in Settings → AI.",
  tokensShort: "tok",
};

const STEPS: readonly WorkbenchStep[] = [
  { label: "Read", state: "done" },
  { label: "Confirm", state: "now" },
  { label: "Voice", state: "todo" },
  { label: "Connect", state: "todo" },
  { label: "Ready", state: "todo" },
];

function Conversation() {
  return (
    <div className="mw-thread">
      <p>
        I read northwind.example and pulled out what the site claims about the
        company. Have a look at the record beside this and tell me where I got
        it wrong.
      </p>
      <p>
        The industry is the one I am least sure about — the site never says it
        outright, so I inferred it from the customers named on the front page.
      </p>
    </div>
  );
}

function Artifact() {
  return (
    <div className="wrap">
      <Card as="div">
        <h2>Northwind Traders GmbH</h2>
        <p>
          Wholesale food distribution for independent grocers across
          German-speaking Europe.
        </p>
      </Card>
    </div>
  );
}

// Everything both variants share, so the one story-visible difference between
// them is the `variant` prop itself.
const BASE = {
  state: "working",
  eyebrow: "Hi, I'm Margince",
  title: "Your company research AI",
  status: "I'm ready to research",
  configured: "deepseek-chat · llama3.1:8b",
  configuredSummary: "2 models, split between cloud and local",
  locale: "en",
  runtime: RUNTIME,
  runtimeLabels: RUNTIME_LABELS,
  steps: STEPS,
  children: <Conversation />,
  artifact: <Artifact />,
} satisfies Partial<ComponentProps<typeof MarginceWorkbench>>;

/**
 * The rail variant, and the surface the person row belongs to.
 *
 * The conversation narrows to a narrator column so the artifact can be the work
 * surface, and the rail then reads top-down: who is speaking, what the run
 * costs, where the journey is, the conversation, and — at the very foot — who
 * is signed in.
 *
 * That foot chip is the design system's `Avatar`, keyed on `identity` rather
 * than on the displayed name: the same reader appears in the transcript three
 * columns away, and a chip that took its tint from the name would move to
 * another colour the moment they were renamed while the transcript's chip
 * stayed put.
 *
 * `personAction` is a slot, never a control the design system chose — onboarding
 * is railless and has no top bar, so the foot row is the one place surface-level
 * chrome can live. The caller supplies both the copy and the behaviour.
 */
export const Rail: Story = {
  name: "Rail — with the signed-in person",
  args: {
    ...BASE,
    variant: "rail",
    footerLabel: "Tokens this setup",
    stepLabel: "Step 2 of 5 · Confirm",
    person: {
      name: "Alex Rivera",
      detail: "alex@northwind.test",
      identity: "alex@northwind.test",
    },
    personAction: (
      <Button small iconOnly aria-label="Switch theme">
        <SunMoon size={15} aria-hidden />
      </Button>
    ),
  },
};

/**
 * The rail with the person unresolved, which is what a reader sees for the
 * first moments of every session.
 *
 * The row survives it: `personAction` alone still renders, so the one piece of
 * chrome on a railless surface does not appear only once the session has
 * loaded — and no chip is drawn for somebody the product cannot yet name.
 */
export const RailWithoutPerson: Story = {
  name: "Rail — person not resolved",
  args: {
    ...BASE,
    variant: "rail",
    footerLabel: "Tokens this setup",
    stepLabel: "Step 2 of 5 · Confirm",
    personAction: (
      <Button small iconOnly aria-label="Switch theme">
        <SunMoon size={15} aria-hidden />
      </Button>
    ),
  },
};

/**
 * The split variant, for comparison.
 *
 * The conversation takes the wider column and the artifact is a reference
 * dossier beside it, so the chrome re-orders: the numbered step list runs above
 * the brand line rather than as a progress bar under it, and the transparency
 * chip rides in the header instead of a footer bar.
 *
 * `person` is deliberately passed and deliberately not drawn — the foot row is
 * rail-only, and a story that omitted the prop here would leave that a claim in
 * the prop comment rather than something a reviewer can see.
 */
export const Split: Story = {
  name: "Split — the default two panes",
  args: {
    ...BASE,
    variant: "split",
    person: {
      name: "Alex Rivera",
      detail: "alex@northwind.test",
      identity: "alex@northwind.test",
    },
  },
};
