// Margince SPA — hash-routed, no framework. Views render into #app;
// state lives in the URL and the server (the CRM is the source of truth,
// this client caches nothing across navigations).

/* ---------- API client ---------- */

// The workspace slug rides a header in dev (the API gates it behind
// MARGINCE_ENV=dev); production resolves the tenant from the subdomain
// and this header is simply ignored there.
const slug = () => localStorage.getItem("margince.workspace") || "";

class ApiError extends Error {
  constructor(status, problem) {
    super(problem?.detail || problem?.title || `HTTP ${status}`);
    this.status = status;
    this.code = problem?.code;
  }
}

async function api(method, path, body) {
  const res = await fetch(`/v1${path}`, {
    method,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "X-Workspace-Slug": slug(),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return null;
  const payload = await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(res.status, payload);
  return payload;
}

/* ---------- tiny DOM helpers ---------- */

const app = document.getElementById("app");

// h builds DOM the safe way: children are nodes or text (never parsed as
// HTML — server data can't inject markup), attributes are set verbatim.
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null) el.setAttribute(k, v);
  }
  el.append(...children.filter((c) => c !== null && c !== undefined));
  return el;
}

let toastTimer;
function toast(message, isError = false) {
  const el = document.getElementById("toast");
  el.textContent = message;
  el.className = isError ? "error" : "";
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (el.hidden = true), 4200);
}

function field(label, input) {
  return h("label", { class: "field" }, h("span", {}, label), input);
}

const euro = new Intl.NumberFormat(undefined, { style: "currency", currency: "EUR" });
function money(minor, currency) {
  if (minor === null || minor === undefined) return "—";
  if (currency === "EUR" || !currency) return euro.format(minor / 100);
  return `${(minor / 100).toFixed(2)} ${currency}`;
}
const when = (iso) => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });

/* ---------- login / bootstrap ---------- */

function loginView(mode = "login") {
  const isBootstrap = mode === "bootstrap";
  const email = h("input", { type: "email", required: "", autocomplete: "username" });
  const password = h("input", { type: "password", required: "", autocomplete: "current-password" });
  const workspace = h("input", { type: "text", required: "", placeholder: "acme", value: slug() });
  const wsName = h("input", { type: "text", placeholder: "Acme GmbH" });
  const name = h("input", { type: "text", placeholder: "Ada Admin" });

  const form = h(
    "form",
    {
      onsubmit: async (e) => {
        e.preventDefault();
        try {
          if (isBootstrap) {
            const me = await api("POST", "/workspaces", {
              workspace_name: wsName.value,
              admin_email: email.value,
              admin_display_name: name.value || email.value,
              admin_password: password.value,
            });
            // slugify the same way the server does: it echoes nothing, so
            // derive from the workspace name (lowercase, dashes).
            localStorage.setItem("margince.workspace",
              wsName.value.toLowerCase().trim().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""));
            session = me;
          } else {
            localStorage.setItem("margince.workspace", workspace.value.trim());
            session = await api("POST", "/auth/login", { email: email.value, password: password.value });
          }
          location.hash = "#/people";
          render();
        } catch (err) {
          toast(err.message, true);
        }
      },
    },
    isBootstrap ? field("Workspace name", wsName) : field("Workspace", workspace),
    isBootstrap ? field("Your name", name) : null,
    field("Email", email),
    field(isBootstrap ? "Admin password (12+ chars)" : "Password", password),
    h("button", { class: "btn primary", style: "width:100%" }, isBootstrap ? "Create workspace" : "Sign in"),
  );

  app.replaceChildren(
    h("div", { class: "login-wrap" },
      h("div", { class: "card login-card" },
        h("div", { class: "brand" }, h("span", { class: "logo" }, "M"), "Margince"),
        form,
        h("p", { class: "switch" },
          isBootstrap ? "Already set up? " : "First run? ",
          h("a", { onclick: () => loginView(isBootstrap ? "login" : "bootstrap") },
            isBootstrap ? "Sign in" : "Create a workspace"),
        ),
      ),
    ),
  );
}

/* ---------- shell ---------- */

let session = null;

function shell(active, content) {
  const nav = [
    ["#/people", "People", "👤"],
    ["#/leads", "Leads", "◍"],
    ["#/deals", "Deals", "◫"],
    ["#/timeline", "Timeline", "☰"],
    ["#/approvals", "Approvals", "✓"],
  ];
  app.replaceChildren(
    h("div", { class: "shell" },
      h("nav", { class: "rail" },
        h("div", { class: "logo", title: "Margince" }, "M"),
        ...nav.map(([href, label, glyph]) =>
          h("a", { href, title: label, class: href === active ? "active" : "" }, glyph)),
        h("div", { class: "spacer" }),
        h("button", {
          title: `Sign out ${session?.user?.display_name ?? ""}`,
          onclick: async () => {
            await api("POST", "/auth/logout").catch(() => {});
            session = null;
            render();
          },
        }, "⏻"),
      ),
      h("main", { class: "main" }, h("div", { class: "page" }, ...content)),
    ),
  );
}

/* ---------- people ---------- */

async function peopleView() {
  const { data } = await api("GET", "/people?limit=100");

  const rows = data.map((p) =>
    h("tr", {},
      h("td", {}, h("div", { class: "primary-cell" }, p.full_name),
        h("div", { class: "meta" }, p.title || "")),
      h("td", {}, (p.emails || []).map((e) => e.email).join(", ") || h("span", { class: "meta" }, "—")),
      h("td", {}, h("span", { class: "meta" }, p.source)),
      h("td", {}, h("span", { class: "meta" }, when(p.created_at))),
    ));

  shell("#/people", [
    h("div", { class: "page-head" },
      h("div", {}, h("h1", {}, "People"),
        h("div", { class: "sub" }, `${data.length} in your scope`)),
      h("button", { class: "btn primary", onclick: newPersonDialog }, "+ New person"),
    ),
    data.length === 0
      ? h("div", { class: "empty" }, "No people yet — capture your first contact.")
      : h("div", { class: "card", style: "padding:0" },
          h("table", {},
            h("thead", {}, h("tr", {},
              h("th", {}, "Name"), h("th", {}, "Email"), h("th", {}, "Source"), h("th", {}, "Added"))),
            h("tbody", {}, ...rows))),
  ]);
}

function newPersonDialog() {
  const name = h("input", { type: "text", required: "" });
  const email = h("input", { type: "email" });
  const title = h("input", { type: "text" });
  openDialog("New person", [field("Full name", name), field("Email", email), field("Title", title)],
    async () => {
      await api("POST", "/people", {
        full_name: name.value,
        title: title.value || undefined,
        emails: email.value ? [{ email: email.value, is_primary: true }] : undefined,
        source: "manual",
        captured_by: "ui", // contract requires it; the server stamps the real principal
      });
      toast(`${name.value} added`);
      route();
    });
}

/* ---------- leads ---------- */

// Leads live outside the clean core (segregated by construction) and only
// graduate on genuine engagement — the Promote action asks for the
// trigger because cold outreach with no reply must never promote.
async function leadsView() {
  const { data } = await api("GET", "/leads?limit=100");

  const rows = data.map((l) =>
    h("tr", {},
      h("td", {}, h("div", { class: "primary-cell" }, l.full_name || l.email || "—"),
        h("div", { class: "meta" }, l.company_name || "")),
      h("td", {}, l.email || h("span", { class: "meta" }, "—")),
      h("td", {}, h("span", { class: "meta" }, l.status)),
      h("td", {}, h("span", { class: "meta" }, l.source)),
      h("td", { style: "text-align:right" },
        l.status === "promoted"
          ? h("span", { class: "meta" }, "promoted ✓")
          : h("button", { class: "btn", onclick: () => promoteLeadDialog(l) }, "Promote")),
    ));

  shell("#/leads", [
    h("div", { class: "page-head" },
      h("div", {}, h("h1", {}, "Leads"),
        h("div", { class: "sub" }, "raw and segregated — they become contacts only on engagement")),
      h("button", { class: "btn primary", onclick: newLeadDialog }, "+ New lead"),
    ),
    data.length === 0
      ? h("div", { class: "empty" }, "No leads — import or add your first prospect.")
      : h("div", { class: "card", style: "padding:0" },
          h("table", {},
            h("thead", {}, h("tr", {},
              h("th", {}, "Lead"), h("th", {}, "Email"), h("th", {}, "Status"),
              h("th", {}, "Source"), h("th", {}))),
            h("tbody", {}, ...rows))),
  ]);
}

function newLeadDialog() {
  const name = h("input", { type: "text" });
  const email = h("input", { type: "email" });
  const company = h("input", { type: "text" });
  openDialog("New lead", [field("Full name", name), field("Email", email), field("Company", company)],
    async () => {
      await api("POST", "/leads", {
        full_name: name.value || undefined,
        email: email.value || undefined,
        company_name: company.value || undefined,
        source: "manual",
        captured_by: "ui",
      });
      toast("Lead added");
      route();
    });
}

const promoteTriggers = [
  ["inbound_reply", "They replied to us"],
  ["meeting_booked", "A meeting is booked"],
  ["meeting_held", "A meeting was held"],
  ["human_qualify", "I qualify this lead myself"],
];

function promoteLeadDialog(lead) {
  const trigger = h("select", {}, ...promoteTriggers.map(([v, label]) => h("option", { value: v }, label)));
  const note = h("input", { type: "text", placeholder: "e.g. replied on Jul 4" });
  openDialog(`Promote ${lead.full_name || lead.email || "lead"}`,
    [field("What was the engagement?", trigger), field("Evidence note", note)],
    async () => {
      const res = await api("POST", `/leads/${lead.id}/promote`, {
        trigger: trigger.value,
        evidence: note.value ? { note: note.value } : undefined,
      });
      toast(res.merged
        ? `Merged into existing contact ${res.person.full_name}`
        : `${res.person.full_name} is now a contact`);
      route();
    });
}

/* ---------- deals ---------- */

async function dealsView() {
  const [{ data: pipelines }, { data: deals }] = await Promise.all([
    api("GET", "/pipelines"),
    api("GET", "/deals?limit=200"),
  ]);
  const pipeline = pipelines[0];
  const stages = (pipeline?.stages ?? []).slice().sort((a, b) => a.position - b.position);

  const columns = stages.map((stage) => {
    const inStage = deals.filter((d) => d.stage_id === stage.id);
    const total = inStage.reduce((sum, d) => sum + (d.amount_minor ?? 0), 0);
    return h("div", { class: "stage-col" },
      h("header", {},
        h("span", {}, `${stage.name} · ${inStage.length}`),
        h("span", { class: "sum" }, total ? money(total, "EUR") : "")),
      ...inStage.map((d) => dealCard(d, stages)),
    );
  });

  shell("#/deals", [
    h("div", { class: "page-head" },
      h("div", {}, h("h1", {}, "Deals"),
        h("div", { class: "sub" }, pipeline ? pipeline.name : "no pipeline")),
      h("button", { class: "btn primary", onclick: () => newDealDialog(pipeline, stages) }, "+ New deal"),
    ),
    deals.length === 0
      ? h("div", { class: "empty" }, "The board is clear — create the first deal.")
      : h("div", { class: "board" }, ...columns),
  ]);
}

function dealCard(deal, stages) {
  const move = h("select", {
    onchange: async (e) => {
      try {
        await api("POST", `/deals/${deal.id}/advance`, { to_stage_id: e.target.value });
        toast(`${deal.name} moved`);
      } catch (err) {
        toast(err.message, true);
      }
      route();
    },
  }, ...stages.map((s) =>
    h("option", { value: s.id, ...(s.id === deal.stage_id ? { selected: "" } : {}) }, `→ ${s.name}`)));

  return h("div", { class: "deal-card" },
    h("div", { class: "name" },
      h("span", { class: `status-dot status-${deal.status}` }), deal.name),
    h("div", { class: "amount" }, money(deal.amount_minor, deal.currency)),
    move,
  );
}

function newDealDialog(pipeline, stages) {
  if (!pipeline) return toast("No pipeline in this workspace", true);
  const name = h("input", { type: "text", required: "" });
  const amount = h("input", { type: "number", min: "0", step: "0.01", placeholder: "0.00" });
  const stage = h("select", {}, ...stages.map((s) => h("option", { value: s.id }, s.name)));
  openDialog("New deal", [field("Name", name), field("Amount (EUR)", amount), field("Stage", stage)],
    async () => {
      const cents = amount.value ? Math.round(parseFloat(amount.value) * 100) : undefined;
      await api("POST", "/deals", {
        name: name.value,
        pipeline_id: pipeline.id,
        stage_id: stage.value,
        amount_minor: cents,
        currency: cents === undefined ? undefined : "EUR",
        source: "manual",
        captured_by: "ui",
      });
      toast(`${name.value} created`);
      route();
    });
}

/* ---------- timeline ---------- */

const kindGlyphs = { note: "✎", email: "✉", call: "☎", meeting: "▣", task: "☑" };

async function timelineView() {
  const { data } = await api("GET", "/activities?limit=100");

  shell("#/timeline", [
    h("div", { class: "page-head" },
      h("div", {}, h("h1", {}, "Timeline"),
        h("div", { class: "sub" }, "everything captured, newest first")),
      h("button", { class: "btn primary", onclick: newNoteDialog }, "+ Log a note"),
    ),
    data.length === 0
      ? h("div", { class: "empty" }, "Nothing captured yet.")
      : h("div", {}, ...data.map((a) =>
          h("div", { class: "timeline-item" },
            h("div", { class: "kind" }, kindGlyphs[a.kind] ?? "•"),
            h("div", { class: "what" },
              h("div", { class: "subject" }, a.subject || a.kind),
              a.body ? h("div", { class: "body" }, a.body) : null,
              h("div", { class: "meta" },
                `${when(a.occurred_at)} · ${a.kind} · `,
                h("span", { class: "mono" }, a.captured_by)))))),
  ]);
}

function newNoteDialog() {
  const subject = h("input", { type: "text", required: "" });
  const body = h("textarea", { rows: "4" });
  openDialog("Log a note", [field("Subject", subject), field("Details", body)],
    async () => {
      await api("POST", "/activities", {
        kind: "note",
        subject: subject.value,
        body: body.value || undefined,
        source: "manual",
        captured_by: "ui",
      });
      toast("Note captured");
      route();
    });
}

/* ---------- approvals ---------- */

// The 🟡 confirm-first inbox: what an agent wanted to do but may not,
// until a human says yes. Approve/reject decide; the agent redeems.
async function approvalsView() {
  const { data } = await api("GET", "/approvals?status=pending&limit=100");

  const items = data.map((a) =>
    h("div", { class: "timeline-item" },
      h("div", { class: "kind" }, "🟡"),
      h("div", { class: "what", style: "flex:1" },
        h("div", { class: "subject" }, a.summary || a.kind),
        h("div", { class: "meta" },
          `${a.kind} · proposed by `,
          h("span", { class: "mono" }, a.proposed_by),
          ` · ${when(a.created_at)} · expires ${when(a.expires_at)}`)),
      h("div", { style: "display:flex; gap:8px; align-items:center" },
        h("button", { class: "btn", onclick: () => decide(a, "reject") }, "Reject"),
        h("button", { class: "btn primary", onclick: () => decide(a, "approve") }, "Approve")),
    ));

  shell("#/approvals", [
    h("div", { class: "page-head" },
      h("div", {}, h("h1", {}, "Approvals"),
        h("div", { class: "sub" }, "confirm-first agent actions waiting on you")),
    ),
    data.length === 0
      ? h("div", { class: "empty" }, "Nothing waiting — agents are inside their green lanes.")
      : h("div", {}, ...items),
  ]);
}

async function decide(approval, verdict) {
  try {
    await api("POST", `/approvals/${approval.id}/${verdict}`, verdict === "reject" ? { reason: "declined in inbox" } : {});
    toast(verdict === "approve"
      ? "Approved — the agent can now perform exactly this action once"
      : "Rejected — nothing will change");
  } catch (err) {
    toast(err.message, true);
  }
  route();
}

/* ---------- dialog plumbing ---------- */

function openDialog(title, fields, onSubmit) {
  const dialog = h("dialog", {},
    h("h2", {}, title),
    h("form", {
      method: "dialog",
      onsubmit: async (e) => {
        if (e.submitter?.value !== "ok") return;
        e.preventDefault();
        try {
          await onSubmit();
          dialog.close();
        } catch (err) {
          toast(err.message, true);
        }
      },
    },
      ...fields,
      h("div", { class: "actions" },
        h("button", { class: "btn", value: "cancel" }, "Cancel"),
        h("button", { class: "btn primary", value: "ok" }, "Save")),
    ),
  );
  dialog.addEventListener("close", () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
}

/* ---------- router ---------- */

const routes = { "#/people": peopleView, "#/leads": leadsView, "#/deals": dealsView, "#/timeline": timelineView, "#/approvals": approvalsView };

async function route() {
  const view = routes[location.hash] ?? peopleView;
  try {
    await view();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      session = null;
      loginView();
      return;
    }
    toast(err.message, true);
  }
}

async function render() {
  if (!session) {
    try {
      session = await api("GET", "/me");
    } catch {
      loginView();
      return;
    }
  }
  route();
}

window.addEventListener("hashchange", route);
render();
