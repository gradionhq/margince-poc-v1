# Video walkthrough — the extension tier, `crm-demo` end to end

`crm-demo.mp4` (1.0 MB, 1360×860, 1:23) — same take as `crm-demo.webm` (5.6 MB), re-encoded.
Attach the **mp4** to the PR; the webm is the Playwright original.

One continuous browser session, recorded by Playwright at `feat/extension-tier-capabilities`
`11e05fee`. Nothing is cut or re-ordered. The dark strip along the bottom is a caption overlay
injected by the recording script — it labels the step and, where it quotes a number, that number was
computed live in the page at that moment (the DOM check in step 2, the button counts in step 6).
Everything above the strip is the real application.

## Timeline

| Time | What is on screen |
|---|---|
| 0:00 | Sign-in page; signing in as `admin@demo.test`. |
| 0:14 | **1/6** `#/ext/crm-demo` — the **Demo Notepad** screen, reached from the composed extension registry. Breadcrumb reads `ext · crm-demo`. |
| 0:20 | **2/6** A signing key is typed into the `Signing key` field and `Store key` clicked. |
| 0:29 | The status chip flips to **Connected**. The caption reports a check run in the page at that instant: the key string appears **nowhere** in the DOM (`false`) and **nowhere** in local/sessionStorage (`false`). The `Signing key` input is empty again. |
| 0:33 | **3/6** `hello-from-the-video` is signed. A real signature renders under the button: `hmac-sha256 b25ef76473352036e75e752c75f97dd6b63349bdc1a04272c0eb4b344eda4102`. |
| 0:38 | **4/6** A note is typed and `Add` clicked; the row appears at the top of the Notes list. |
| 0:50 | A **full page reload**. The note is still there — it lives in the unit's own `ext.ext_crm_demo_note` table. |
| 0:58 | **5/6** Nothing is clicked from here. Caption records 3 heartbeat rows visible and waits. |
| 1:03 | A **fourth heartbeat row arrives unprompted** (3 → 4) — `⟳ heartbeat — workspace 019fe43b-a518-7b95-be24-642be40ef8f1`. The unit's scheduled job ticks every 60 s; the screen polls every 15 s. Each row names the workspace the tick was for. |
| 1:08 | **6/6** The session cookie is cleared and the read-only seat `readonly@demo.test` signs in. |
| 1:17 | The same screen on the **read-only seat**: the Notes list renders all five rows, and `Add`, per-row `Remove`, the `New note` field and `Store key` are all **gone** (counts in the caption: 0 / 0 / 0). The status still reads `Connected` and `Sign` is still offered — that seat holds `read` on `ext_crm_demo_signing_key` but not `update`. |

## Stills

`01-screen.png` · `02-connected.png` · `03-signature.png` · `04-note-added.png` ·
`05-after-reload.png` · `06-heartbeat.png` · `07-readonly.png` — fallback if the video does not
embed.

## Corroboration taken outside the video

The signature the **screen** rendered is a real HMAC of the key the tester stored:

```
$ python3 -c "import hmac,hashlib;print(hmac.new(b'VIDEO-DEMO-KEY-0123456789abcdef',b'hello-from-the-video',hashlib.sha256).hexdigest())"
b25ef76473352036e75e752c75f97dd6b63349bdc1a04272c0eb4b344eda4102     ← matches the screen
```

The read-only seat was created through the product's own endpoints (`POST /v1/users` →
`POST /v1/users/{id}/password-link` → `POST /v1/auth/reset-password`), not by SQL.

## How the environment was prepared

```bash
make dev DEV_SLUG=vid            # db margince_dev_vid, api :18401, composed vite :8401

# prerequisite 0 — RBAC grants, raw SQL (there is no /roles endpoint).
# TWO objects now: the signing operations gained their own, ext_crm_demo_signing_key.
psql -h localhost -p 55432 -U margince_owner -d margince_dev_vid <<'SQL'
UPDATE role SET permissions = jsonb_set(permissions,'{objects,ext_crm_demo_note}',
   '{"create":true,"read":true,"update":true,"delete":true}'::jsonb,true), updated_at=now()
 WHERE key='admin' AND archived_at IS NULL;
UPDATE role SET permissions = jsonb_set(permissions,'{objects,ext_crm_demo_signing_key}',
   '{"create":true,"read":true,"update":true,"delete":true}'::jsonb,true), updated_at=now()
 WHERE key='admin' AND archived_at IS NULL;
UPDATE role SET permissions = jsonb_set(permissions,'{objects,ext_crm_demo_note}',
   '{"create":false,"read":true,"update":false,"delete":false}'::jsonb,true), updated_at=now()
 WHERE key='read_only' AND archived_at IS NULL;
UPDATE role SET permissions = jsonb_set(permissions,'{objects,ext_crm_demo_signing_key}',
   '{"create":false,"read":true,"update":false,"delete":false}'::jsonb,true), updated_at=now()
 WHERE key='read_only' AND archived_at IS NULL;
SQL

# prerequisite 0b — an agent seat, or the heartbeat tick cannot run (issue #656)
psql … -c "INSERT INTO app_user (workspace_id,email,display_name,status,is_agent,seat_type)
           SELECT id,'agent@demo.test','Demo Agent Seat','active',true,'full' FROM workspace LIMIT 1;"

node .superpowers/sdd/extension-tier-slices/video/record.mjs
```

`record.mjs` is the throwaway driver, kept here rather than in `frontend/e2e/`. This whole directory
is git-ignored; no tracked file was touched to make the recording.

## What the video does not show

- **The agent path.** `demo_list_notes` and friends over MCP are not a browser surface, so they are
  absent. See `UAT-EVIDENCE-RERUN.md` step 6.
- **The API refusing the read-only seat's write.** The video shows the control being *absent*; it
  does not show the 403 the API returns if you call `notes/add` anyway (also in the evidence file).
- **The removal / empty-tree legs** (step 8 of the acceptance run) — nothing about them is visible
  in a browser.
- The cold-start company-context onboarding gate is cleared in the first seconds of the recording.
  It is core onboarding, not the extension tier.
