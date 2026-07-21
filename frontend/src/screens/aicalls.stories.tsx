import type { Meta, StoryObj } from "@storybook/react-vite";
import { AiCallsCard, CallDetailPanel } from "./aicalls";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

const summary = {
  id: "call-1",
  occurred_at: "2026-07-20T10:00:00Z",
  task: "capture_classify",
  tier: "cheap_cloud",
  provider: "gemini",
  model_id: "configured",
  served_model: "served",
  calls_attempted: 2,
  tokens_in: 100,
  tokens_out: 20,
  reasoning_tokens: 0,
  cached_tokens: 0,
  latency_ms: 900,
  cache_hit: false,
  degraded: true,
  error_sentinel: "provider_unavailable",
  has_payload: true,
};
const detail = {
  ...summary,
  served_identity_source: "response",
  context_scopes: ["identity"],
  context_fingerprint: "abc",
  attempts: [
    {
      attempt: 1,
      is_terminal: false,
      attempt_reason: "",
      tokens_in: 100,
      tokens_out: 0,
      latency_ms: 400,
      occurred_at: summary.occurred_at,
    },
    {
      attempt: 2,
      is_terminal: true,
      attempt_reason: "retry_on_5xx",
      tokens_in: 100,
      tokens_out: 20,
      latency_ms: 900,
      occurred_at: summary.occurred_at,
    },
  ],
  payload_captured: true,
  payload: { request: { system: "safe", messages: [] }, response: "ok" },
};

function list(data: unknown[], capture = true) {
  return () => {
    installFetchStub({
      "GET /ai/calls": () =>
        jsonResponse({
          data,
          page: { has_more: false },
          payload_capture_enabled: capture,
        }),
    });
    return (
      <StoryProviders>
        <AiCallsCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof AiCallsCard> = {
  title: "screens/ai-calls",
  component: AiCallsCard,
};
export default meta;
type Story = StoryObj<typeof AiCallsCard>;
export const List: Story = { render: list([summary]) };
export const Empty: Story = { render: list([]) };
export const PayloadOff: Story = {
  render: () => {
    installFetchStub({
      "GET /ai/calls/call-1": () =>
        jsonResponse({ ...detail, payload_captured: false, payload: null }),
    });
    return (
      <StoryProviders>
        <CallDetailPanel id="call-1" captureEnabled={false} />
      </StoryProviders>
    );
  },
};
export const WithPayload: Story = {
  render: () => {
    installFetchStub({ "GET /ai/calls/call-1": () => jsonResponse(detail) });
    return (
      <StoryProviders>
        <CallDetailPanel id="call-1" captureEnabled />
      </StoryProviders>
    );
  },
};
export const Expanded = WithPayload;
