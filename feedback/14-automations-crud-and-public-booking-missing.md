# 14 — EP09 screens blocked by missing contract surfaces: automations CRUD, public booking + consent passthrough

**Where:** `crm.yaml` vs B-EP09.15 (automations editor) and B-EP09.14
(public booking page).

**Automations (B-EP09.15):** the ticket depends on B-E15.1 (catalog
registry) and B-E15.4 (automation CRUD), but the contract defines NO
automations/workflows endpoints (the workflow engine exists server-side —
starter library, runner — with no REST management surface). The editor UI
cannot ship without list/enable/parameterize/pause/delete ops. B-EP09.15 is
**blocked**; the route renders the honest pending state.

**Booking (B-EP09.14):** `/availability` + `/bookings` are session-authed;
there is no public/anonymous access path (host token/slug) for the public
`book.html`, and `POST /bookings` has no consent fields, so the EP07
capture-surface contract (purpose + policy wording/version passthrough)
cannot be honoured on booking. The in-app booking shell shipped
(availability, duration toggle, recognized-contact welcome, honest
behind-backend degradation); the public variant + consent passthrough wait
on B-E04.16 — consistent with the ticket's own "[TS] may ship behind it".

**Proposed spec change:** add the automation management ops (closed
catalog: list catalog + CRUD on instances with enable/pause), and extend
the booking surface with a public access mechanism + consent capture
fields per features/07's capture-surface contract.
