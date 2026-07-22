import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../../i18n";
import type { ThreadEntry } from "./conversation-machine";
import {
  NarrationBubble,
  OutcomeCard,
  QuestionCard,
  UserTurn,
} from "./entries";
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
  id: "field:industry",
  i18nKey: "ob.conv.read.learnedField",
  params: { field: "industry", value: "Industrial robotics" },
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
  id: "company:confirmed",
  i18nKey: "ob.conv.company.confirmed",
};

export const Narration: Story = {
  render: () => <NarrationBubble entry={narration} />,
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

export const Thread: Story = {
  render: () => (
    <ConversationThread
      entries={[
        {
          kind: "narration",
          id: "pages:5",
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
