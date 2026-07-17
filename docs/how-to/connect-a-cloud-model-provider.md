# Connect a cloud model provider (BYOK)

Point the AI lanes at a **customer-supplied cloud key** — Anthropic, OpenAI,
Gemini, or any OpenAI-compatible vendor. Margince runs no inference of its own
(ADR-0020): the key, the endpoint, and the DPA are yours. Providers are config
rows in `ai-routing.yaml`, never binary flags — swapping one is an edit here, not
a deploy. See [explanation/agent-surface.md](../explanation/agent-surface.md) for
the model runtime and [reference/configuration.md](../reference/configuration.md)
for the full provider matrix. For the no-cloud path, see
[enrich-with-a-local-llm.md](enrich-with-a-local-llm.md).

## 1. Pick a provider

| `provider` | Use it for | Auth | `base_url` |
|---|---|---|---|
| `anthropic` | Claude (native Messages API) | `api_key` | optional (default `api.anthropic.com`) |
| `openai` | GPT (native Responses API — reasoning effort, prompt-cache + reasoning token usage, image/PDF input) | `api_key` | optional (default `api.openai.com`) |
| `gemini` | Gemini (native `generateContent` — thinking level, thought-signature continuity, image/PDF input) | `api_key` | optional (default `…/v1beta`) |
| `openai_compatible` | the OpenAI-wire long tail — Mistral, DeepSeek, Groq, Together, OpenRouter, a self-hosted gateway, … | `api_key` | **required** |

Reach for a **native** adapter (`openai`/`gemini`) when you want that vendor's
reasoning/thinking knobs, document attachments, or itemized usage. Reach for
`openai_compatible` for any vendor that speaks `/v1/chat/completions` and isn't
worth a dedicated adapter — it is the correct default for everything that is not
Anthropic, OpenAI, or Gemini.

## 2. Bind a tier

Edit your local `config/ai-routing.yaml` (seeded from the template by
`make install` / `make dev`). Bind a capability tier to the provider; `api_key`
is read **literally** — the parser does not expand `${ENV}`.

```yaml
# GPT on the cheap-cloud tier, Gemini on premium (native adapters):
tiers:
  cheap_cloud: { provider: openai, model: gpt-5-mini, api_key: sk-… }
  premium:     { provider: gemini, model: gemini-2.5-pro, api_key: … }

# …or any OpenAI-compatible vendor via the generic adapter:
tiers:
  cheap_cloud:
    provider: openai_compatible
    model: mistral-small-2506        # pin an explicit version — -latest aliases drift
    base_url: https://api.mistral.ai # host root, NO /v1 (see the caveat below)
    api_key: …
```

> **`base_url` for the OpenAI-wire providers (`openai_compatible`, `openai`,
> `vllm`) is the vendor host root with _no_ version segment.** The adapter
> appends `/v1/chat/completions` (or `/v1/responses`), so a base ending in `/v1`
> doubles it — `https://api.mistral.ai/v1` becomes `…/v1/v1/chat/completions` and
> 404s. Use `https://api.mistral.ai`. `gemini` is the mirror: its default base
> keeps `/v1beta` and the paths are version-relative, so leave `base_url` unset.

> `config/ai-routing.yaml` is **gitignored** — edit it freely; it can never be
> committed. To reset, delete it and re-run `make install` (or `make dev`) to
> re-seed from `config/ai-routing.example.yaml` (which is schema-validated in any
> editor with a YAML language server — autocomplete, enum checks, hover docs).

## 3. Bind the embeddings lane separately

The embedding lane is bound apart from the chat tiers so retrieval survives a
chat-budget exhaustion. Not every OpenAI-compatible vendor serves it:

```yaml
embeddings: { provider: ollama, model: bge-m3 }   # a local embedder always works
```

> **`openai_compatible`'s `/embeddings` 404s on OpenRouter, Groq, and DeepSeek**
> — they serve chat only. Bind `embeddings:` to a vendor that has the lane
> (`openai`, Mistral, Gemini) or to a local model (`ollama` `bge-m3`). `gemini`
> embeds via `gemini-embedding-001`.

## 4. Start the stack

`make dev` flips from the offline fake into real routing when **`ANTHROPIC_API_KEY`**
is set, and injects that key onto the *anthropic* tiers of a scratch config. For
an `openai`/`gemini`/`openai_compatible` binding, put the key **literally** in
`config/ai-routing.yaml` (step 2) and either:

```sh
# a) flip make dev into real routing with any placeholder Anthropic key:
ANTHROPIC_API_KEY=flip-real-routing make dev
# b) or run the api binary directly against your config (from backend/):
cd backend && go run ./cmd/api --ai-routing ../config/ai-routing.yaml
```

The api comes up on `:8080`. Exercise a lane that ladders to your tier — e.g.
open a company and **Read now** (cold-start read-back runs `cheap_cloud` →
`premium`). Set `MARGINCE_LOG_LEVEL=debug` for verbose model-runtime logs.

## The sovereign profile refuses every cloud provider

Under `profile: sovereign` (zero egress by construction) a cloud provider on any
tier — or the embeddings lane — is a **startup error**, not a runtime surprise.
The refusal is bound to the provider _name_, not a config flag, so pointing
`openai_compatible` at a localhost URL is still refused: only `ollama`, `vllm`,
and `fake` are sovereign-eligible. Use `eu_hosted` or `cloud_frontier` for a BYOK
cloud binding.

## Troubleshooting

| Symptom | Meaning / fix |
|---|---|
| `http 404` on `…/v1/v1/chat/completions` or `…/v1/v1/responses` | `base_url` includes a `/v1` segment — drop it (§2 caveat); the adapter adds it. |
| Boot error *"profile sovereign forbids cloud provider …"* | A cloud provider is bound under `profile: sovereign`. Switch to `eu_hosted`/`cloud_frontier`, or bind that tier to `ollama`/`vllm`. |
| Boot error *"needs an api key (BYOK …)"* | A cloud provider has no `api_key`. Margince provides no inference — add the key (literally, no `${ENV}`). |
| Boot error *"needs a base_url …"* | `openai_compatible` has no `base_url`. Add the vendor host root (no `/v1`). |
| `http 404` on `/embeddings` | The `openai_compatible` vendor is chat-only. Rebind `embeddings:` to a lane-serving vendor or a local `bge-m3` (§3). |
| Model 404 / *"model not found"* | A drifting `-latest` alias or a wrong id. Pin an explicit versioned model, or resolve it from the vendor's `/models` endpoint. |
| Log says *"offline fake"* despite a cloud binding | `make dev` didn't flip into real routing — set any `ANTHROPIC_API_KEY` (§4) or run the api with `--ai-routing` directly. |
