import type { Meta, StoryObj } from "@storybook/react-vite";
import { MarginceCoreScene } from "../../design-system/margince-core";
import { LocaleProvider } from "../../i18n";
import {
  type ConversationState,
  initialConversationState,
  type ThreadEntry,
} from "./conversation-machine";
import {
  NarrationBubble,
  OutcomeCard,
  QuestionCard,
  UserTurn,
} from "./entries";
import { presenceFor } from "./presence";
import { ConversationThread } from "./thread";

const meta: Meta = {
  title: "Screens/OnboardingConversation/Entries",
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;
type Story = StoryObj;

const narration: Extract<ThreadEntry, { kind: "narration" }> = {
  kind: "narration",
  id: "1:field:industry",
  i18nKey: "ob.conv.read.learnedField",
  params: { value: "Industrial robotics" },
  paramKeys: { field: "ob.field.industry" },
  findingIds: ["industry"],
};

const question: Extract<ThreadEntry, { kind: "question" }> = {
  kind: "question",
  id: "question:clarify-entity",
  question: {
    id: "clarify-entity",
    i18nKey: "ob.conv.clarify.entity",
    options: [
      { value: "acme-gmbh", label: "Acme GmbH" },
      { value: "acme-holding", label: "Acme Holding SE" },
    ],
  },
};

const userTurn: Extract<ThreadEntry, { kind: "user" }> = {
  kind: "user",
  id: "answer:clarify-entity",
  text: "Acme GmbH",
};

const outcome: Extract<ThreadEntry, { kind: "outcome" }> = {
  kind: "outcome",
  id: "4:company:confirmed",
  i18nKey: "ob.conv.company.confirmed",
  tone: "success",
};

const deferredOutcome: Extract<ThreadEntry, { kind: "outcome" }> = {
  kind: "outcome",
  id: "5:build:deferred",
  i18nKey: "ob.conv.build.deferred",
  tone: "deferred",
};

const failureOutcome: Extract<ThreadEntry, { kind: "outcome" }> = {
  kind: "outcome",
  id: "6:read:failed",
  i18nKey: "ob.conv.read.failed",
  tone: "failure",
};

export const Narration: Story = {
  render: () => <NarrationBubble entry={narration} />,
};

// The live-arrival treatment: words stagger in (capped total duration);
// under prefers-reduced-motion the sentence is simply there.
export const NarrationRevealed: Story = {
  render: () => <NarrationBubble entry={narration} reveal />,
};

// The orb choreography at a glance: one presence per conversation moment,
// derived through the same presenceFor mapping the acts use.
const choreography: ReadonlyArray<{
  label: string;
  state: ConversationState;
  read?: Parameters<typeof presenceFor>[1];
}> = [
  {
    label: "co.intro",
    state: { ...initialConversationState, act: "company", phase: "co.intro" },
  },
  {
    label: "co.reading",
    state: { ...initialConversationState, act: "company", phase: "co.reading" },
    read: { read: { status: "reading", phase: "crawling", pages_read: 14 } },
  },
  {
    label: "co.clarify",
    state: { ...initialConversationState, act: "company", phase: "co.clarify" },
  },
  {
    label: "co.review",
    state: { ...initialConversationState, act: "company", phase: "co.review" },
  },
  {
    label: "vo.building",
    state: {
      ...initialConversationState,
      act: "voice",
      phase: "vo.building",
      lastBuildStage: "evaluate",
    },
  },
  {
    label: "vo.result failed",
    state: {
      ...initialConversationState,
      act: "voice",
      phase: "vo.result",
      lastBuildStatus: "failed",
    },
  },
  {
    label: "cn.done",
    state: { ...initialConversationState, act: "done", phase: "cn.done" },
  },
];

export const OrbChoreography: Story = {
  render: () => (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "repeat(4, 1fr)",
        gap: "var(--space-4)",
      }}
    >
      {choreography.map((moment) => {
        const presence = presenceFor(moment.state, moment.read);
        return (
          <figure key={moment.label} style={{ margin: 0, textAlign: "center" }}>
            <MarginceCoreScene
              state={presence.core}
              progress={presence.progress}
            />
            <figcaption className="t-small">{moment.label}</figcaption>
          </figure>
        );
      })}
    </div>
  ),
};

export const Question: Story = {
  render: () => (
    <QuestionCard question={question.question} onAnswer={() => {}} />
  ),
};

export const QuestionAnswered: Story = {
  render: () => (
    <QuestionCard question={question.question} answered onAnswer={() => {}} />
  ),
};

export const User: Story = {
  render: () => <UserTurn entry={userTurn} />,
};

export const Outcome: Story = {
  render: () => <OutcomeCard entry={outcome} />,
};

export const OutcomeDeferred: Story = {
  render: () => <OutcomeCard entry={deferredOutcome} />,
};

export const OutcomeFailure: Story = {
  render: () => <OutcomeCard entry={failureOutcome} />,
};

export const Thread: Story = {
  render: () => (
    <ConversationThread
      entries={[
        {
          kind: "narration",
          id: "0:pages:5",
          i18nKey: "ob.conv.read.pages",
          params: { pages: 5 },
        },
        narration,
        question,
        userTurn,
        outcome,
      ]}
      pendingQuestionId={null}
      onAnswer={() => {}}
    />
  ),
};
