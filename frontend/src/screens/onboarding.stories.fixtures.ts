import type { components } from "../api/schema";

export const configuredAiProfile: components["schemas"]["AiProfile"] = {
  name: "Margince",
  kind: "ai",
  state: "configured",
  inference_mode: "hybrid",
  providers: ["gemini", "ollama"],
  configured_models: [
    {
      tier: "cheap_cloud",
      provider: "gemini",
      model: "gemini-3.1-flash-lite",
    },
    { tier: "local_small", provider: "ollama", model: "gemma3" },
    {
      tier: "premium",
      provider: "gemini",
      model: "gemini-3.5-flash",
    },
  ],
};
