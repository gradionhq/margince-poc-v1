# Enrich a company with a local LLM (Ollama)

Run the AI lanes (company **enrich**, cold-start read-back) against a local or
self-hosted [Ollama](https://ollama.com) instead of a cloud model — no
Anthropic key needed. Retrieval is the app's job (it fetches the page under an
SSRF guard, then asks the model only to *extract* grounded facts), and the
`enrich` task routes to the `local_small` tier first, so a local model serves
it. See [explanation/agent-surface.md](../explanation/agent-surface.md) for the
model runtime and [reference/configuration.md](../reference/configuration.md)
for the flags/env below.

## 1. Run Ollama and pull the models

```sh
ollama serve                 # default http://localhost:11434
ollama pull gemma3           # the shipped local_small model (ai-routing.yaml)
ollama pull bge-m3           # only if you exercise search/retrieval (embeddings lane)
```

`mistral` follows the extraction JSON schema more reliably than `gemma3`; pull
it too (`ollama pull mistral`) if enrich grounding is weak.

## 2. Point the AI lanes at Ollama

Your local `config/ai-routing.yaml` (seeded from the template by `make install` /
`make dev`) already binds `local_small` to
`ollama`/`gemma3` with no `base_url` (so it defaults to `localhost:11434`), and
`enrich`'s tier ladder is `local_small` → `cheap_cloud`. **For a local Ollama,
enrich works with no config change.**

Edit the tiers only to:

- **use a remote/self-hosted Ollama** — add a `base_url` (no trailing slash; the
  adapter appends `/api/chat`):
  ```yaml
  local_small: { provider: ollama, model: mistral, base_url: https://ollama.internal:11434 }
  ```
- **run cold-start / offer-draft locally too** (they ladder `cheap_cloud` →
  `premium`, cloud by default) — rebind those tiers to `ollama` as well.

> `config/ai-routing.yaml` is **gitignored** — edit it freely for local dev; it
> can never be committed. To reset, delete it and re-run `make install` (or
> `make dev`) to re-seed from `config/ai-routing.example.yaml`.

## 3. Start the stack

`scripts/dev.sh` (`make dev`) only activates the real routing when
`ANTHROPIC_API_KEY` is set; otherwise it uses the offline fake (which would also
fake the *Ollama* call). A placeholder key flips it into real routing so the
Ollama-bound `local_small` tier is actually exercised:

```sh
ANTHROPIC_API_KEY=ollama-not-used make dev   # look for: "using real Anthropic model"
```

> The inline assignment avoids clobbering an existing `.env.local`; `make dev`
> reads `ANTHROPIC_API_KEY` from the environment. (Persist it in `.env.local`
> instead if you prefer — it is git-ignored.)
>
> ⚠️ The placeholder is **not a valid Anthropic credential**. Enrich starts on
> `local_small` (Ollama), so it stays local — but any flow that ladders up to a
> still-Anthropic tier (the cold-start read-back runs `cheap_cloud` → `premium`)
> will call Anthropic and fail. Rebind those tiers to Ollama first (§2) if you
> exercise them.

`make dev` brings up the api on `:8080`, API-seeds the demo workspace on boot,
and runs the Vite SPA on `:5173`. Open **http://localhost:5173** and log in
with the seeded admin (`admin@demo.test` / `demo-password-123`, workspace
`demo-workspace`).
Full first-run details:
[tutorials/getting-started.md](../tutorials/getting-started.md).

## 4. Add a company and enrich it

1. Go to **Companies** (`#/companies`) → **New company**. Give it a **crawlable**
   domain, e.g. `stripe.com`.
   > The fetcher sends `User-Agent: margince-enrich/1.0`; bot-protected sites
   > (e.g. `tesla.com`) answer **403**. Known-crawlable: `stripe.com`, `go.dev`,
   > `ollama.com`, `news.ycombinator.com`, `sqlite.org`.
2. Open the company → **Read now** on the *Read from the website* card.
3. **Expected:** a staged 🟡 enrichment proposal — a confirm-first banner with
   per-field confidence and evidence chips, and an **Open inbox** button.
   Nothing writes to the company until you accept it in the Inbox.

The model is constrained to emit the extraction JSON shape at generation
(Ollama's `format`), so a small model returns a well-formed object rather than
failing the parser. Grounding is still model-dependent: the evidence gate drops
any field whose snippet isn't a verbatim quote from the page — that refusal is
the anti-hallucination guarantee, not a bug.

## Troubleshooting

| Symptom | Meaning / fix |
|---|---|
| Log says *"no ANTHROPIC_API_KEY … offline fake"* | The placeholder key wasn't picked up — check `.env.local`, restart `make dev`. |
| *"Couldn't read enough from this company's site."* | The fetch failed: the offline fake is active (see above), a **403** from a bot-protected domain, or a genuinely thin page. Use a crawlable domain. |
| *"no field survived the no-guess evidence gate"* | The model returned JSON but no `evidence_snippet` was verbatim on the page (or confidences ≤ 0). Expected for weak models / thin pages — try a content-rich page, or `mistral` over `gemma3`. |
| A 500 mentioning *"cannot unmarshal … into … string"* | The model ignored the schema and emitted a wrong-typed field. Switch to `mistral`. |
| Logged out immediately after login on `:5173` | The api isn't reachable at the Vite `/v1` proxy target — make sure `make dev` is running (it starts both) and use the URLs it printed. |

Set `MARGINCE_LOG_LEVEL=debug` (in `.env.local` or via `--log-level`) for verbose
model-runtime logs. Small local models are hit-or-miss against the strict
evidence gate — a cloud model (real `ANTHROPIC_API_KEY`, tiers back on
`anthropic`) grounds more reliably; Ollama is ideal for exercising the pipeline
end to end.
