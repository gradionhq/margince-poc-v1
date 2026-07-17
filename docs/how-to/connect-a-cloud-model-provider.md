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

| `provider` | Use it for | Key env var | `base_url` |
|---|---|---|---|
| `anthropic` | Claude (native Messages API) | `ANTHROPIC_API_KEY` | optional (default `api.anthropic.com`) |
| `openai` | GPT (native Responses API — reasoning effort, prompt-cache + reasoning token usage, image/PDF input) | `OPENAI_API_KEY` | optional (default `api.openai.com`) |
| `gemini` | Gemini (native `generateContent` — thinking level, thought-signature continuity, image/PDF input) | `GEMINI_API_KEY` | optional (default `…/v1beta`) |
| `openai_compatible` | the OpenAI-wire long tail — Mistral, DeepSeek, Groq, Together, OpenRouter, a self-hosted gateway, … | `OPENAI_COMPATIBLE_API_KEY` | **required** |

The routing file names only the provider — **the BYOK key lives in the
environment**, read from the var above at boot (12-factor; a stray `api_key:` in
the config is a startup error). Secrets never touch a config file.

Reach for a **native** adapter (`openai`/`gemini`) when you want that vendor's
reasoning/thinking knobs, document attachments, or itemized usage. Reach for
`openai_compatible` for any vendor that speaks `/v1/chat/completions` and isn't
worth a dedicated adapter — it is the correct default for everything that is not
Anthropic, OpenAI, or Gemini.

## 2. Bind a tier

Edit your local `config/ai-routing.yaml` (seeded from the template by
`make install` / `make dev`). Bind a capability tier to the provider — **no key
in the file** (the key comes from the env var in step 4). The shipped default
binds **gemini** on `cheap_cloud` + `premium`:

```yaml
# Native adapters — the key is read from GEMINI_API_KEY / OPENAI_API_KEY at boot:
tiers:
  cheap_cloud: { provider: gemini, model: gemini-2.5-flash }
  premium:     { provider: gemini, model: gemini-2.5-pro }

# …or any OpenAI-compatible vendor via the generic adapter. It needs a base_url
# (the key comes from OPENAI_COMPATIBLE_API_KEY):
tiers:
  cheap_cloud:
    provider: openai_compatible
    model: mistral-small-2506        # pin an explicit version — -latest aliases drift
    base_url: https://api.mistral.ai # host root, NO /v1 (see the caveat below)
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
chat-budget exhaustion. The shipped default is **gemini** (`gemini-embedding-001`,
key from `GEMINI_API_KEY`) so the stack needs no local Ollama:

```yaml
embeddings: { provider: gemini, model: gemini-embedding-001 }  # the default
# embeddings: { provider: ollama, model: bge-m3 }              # fully-local alternative
# embeddings: { provider: fake }                               # offline dev
```

> The retrieval store's column is a fixed **`vector(1024)`**, and cloud embedders
> default wider (Gemini 3072, OpenAI 1536). The adapter pins the width — Gemini
> via `outputDimensionality`, OpenAI via `dimensions` — so a cloud embedder drops
> in without a schema change. A binding that returns another width fails loudly.

> **`openai_compatible`'s `/embeddings` 404s on OpenRouter, Groq, and DeepSeek**
> — they serve chat only. Bind `embeddings:` to a vendor that has the lane
> (`gemini`, `openai`, Mistral) or a local model (`ollama` `bge-m3`).

## 4. Start the stack

Set the key for your bound provider in `.env.local` — `GEMINI_API_KEY`,
`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or `OPENAI_COMPATIBLE_API_KEY`. `make dev`
sources `.env.local`, so the api/worker inherit the var and read the key from the
environment at boot; the routing file stays keyless:

```sh
# .env.local:  GEMINI_API_KEY=…
make dev
```

To run without `make dev`, export the var yourself (production does the same via
its process manager):

```sh
cd backend && GEMINI_API_KEY=… go run ./cmd/api --ai-routing ../config/ai-routing.yaml
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
| Boot error *"needs an api key — set X_API_KEY …"* | The bound cloud provider's key env var is unset. Export the one the error names (e.g. `GEMINI_API_KEY`). |
| Boot error *"field api_key not found"* | You put an `api_key:` in the routing file — remove it; the key comes from the env var (see the table above). |
| Boot error *"needs a base_url …"* | `openai_compatible` has no `base_url`. Add the vendor host root (no `/v1`). |
| `http 404` on `/embeddings` | The `openai_compatible` vendor is chat-only. Rebind `embeddings:` to a lane-serving vendor or a local `bge-m3` (§3). |
| Model 404 / *"model not found"* | A drifting `-latest` alias or a wrong id. Pin an explicit versioned model, or resolve it from the vendor's `/models` endpoint. |
| Log says *"offline fake"* despite a cloud binding | `make dev` didn't flip into real routing — set any `ANTHROPIC_API_KEY` (§4) or run the api with `--ai-routing` directly. |
