import {
  type ConversationEvent,
  type ConversationQuestion,
  type ConversationState,
  conversationReducer,
  initialConversationState,
} from "./conversation-machine";

// Shared fixtures for the conversation-machine test suites (and nothing
// else): a fold-the-events helper and the two recurring questions.

export function run(
  events: readonly ConversationEvent[],
  from: ConversationState = initialConversationState,
): ConversationState {
  return events.reduce(conversationReducer, from);
}

export const entityQuestion: ConversationQuestion = {
  id: "clarify-entity",
  i18nKey: "ob.conv.clarify.entity",
  options: [
    { value: "acme-gmbh", label: "Acme GmbH" },
    { value: "acme-holding", label: "Acme Holding SE" },
  ],
};

export const speakerQuestion: ConversationQuestion = {
  id: "speaker",
  i18nKey: "ob.conv.voice.speakerQuestion",
  options: [
    { value: "Speaker 1", label: "Speaker 1" },
    { value: "Speaker 2", label: "Speaker 2" },
  ],
};
