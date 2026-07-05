// English catalog. Keys are the contract; de.ts must mirror them exactly
// (compile-time via satisfies, runtime via i18n.test.ts). Placeholders use
// {name} and are filled by t(key, params).
export const en = {
  "app.title": "Margince design tokens",
  "app.subtitle":
    "Ledger Green (ADR-0040) — canonical values mirror the spec mockups; tests pin them.",
  "theme.toDark": "Dark theme",
  "theme.toLight": "Light theme",

  "section.surfaces": "Surfaces",
  "section.accentAi": "Accent & AI",
  "section.text": "Text",
  "section.status": "Status",
  "section.typeRamp": "Type ramp",
  "section.trust": "Trust primitives (B-EP09.3a)",

  "type.display": "Display — Outfit 600",
  "type.body": "Body — DM Sans 400, the default reading face.",
  "type.mono": "Mono — JetBrains Mono, evidence snippets and IDs.",

  "trust.accept": "Accept",
  "trust.edit": "Edit",
  "trust.dismiss": "Dismiss",
  "trust.save": "Save",
  "trust.typedByYou": "typed by you",
  "trust.agentTag": "agent: {agent}",
  "trust.dismissed": "Suggestion dismissed.",
  "trust.stagedProposal": "staged proposal",
  "trust.resolvedValue": "resolved value",
  "trust.editValue": "Edit {description}",

  "confidence.high": "high",
  "confidence.med": "medium",
  "confidence.low": "low",

  "autonomy.auto": "auto-execute",
  "autonomy.confirm": "confirm-first",

  "nav.home": "Home",
  "nav.contacts": "Contacts",
  "nav.companies": "Companies",
  "nav.leads": "Leads",
  "nav.deals": "Deals",
  "nav.tasks": "Tasks",
  "nav.inbox": "Inbox",
  "nav.reports": "Reports",
  "nav.ai": "Ask AI",
  "nav.settings": "Settings",
  "nav.design": "Design system",

  "shell.railAria": "Primary navigation",
  "shell.logoAria": "Margince",
  "shell.search": "Search",
  "locale.toEnglish": "Switch to English",
  "locale.toGerman": "Switch to German",

  "screen.pending":
    "Not built yet — this surface arrives with its build ticket.",

  "palette.aria": "Command palette",
  "palette.placeholder": "Jump to, or ask anything…",
  "palette.empty": "No matches.",
  "palette.askAi": "Ask AI: \u201c{query}\u201d",
  "palette.typeScreen": "Screen",
  "palette.typeAction": "Action",
  "palette.typeRecord": "Record",
  "action.newDeal": "New deal",
  "action.readCompany": "Read a company",
  "action.booking": "Booking page",
  "action.connected": "Connected surfaces",

  "fab.open": "Ask about this",
  "fab.close": "Close",
  "fab.panelAria": "Ask about this record",
  "fab.context": "Ask about {context}",
  "fab.scope": "Your agent reads only what you can see.",
  "fab.inputAria": "Your question",
  "fab.placeholder": "Ask about what you're looking at…",
  "fab.send": "Ask",

  "explain.open": "Explain this number",
  "explain.title": "How this number is built",
  "explain.rate": "rate {rate} on {date}",

  "brief.nothingSent": "Nothing sent yet",
  "board.count": "{count} deals",
  "board.weighted": "weighted {value}",
  "deal.stalled": "stalled",
  "deal.singleThreaded": "single-threaded",
  "deal.staged": "staged",
  "record.timeline": "Timeline",

  "common.error": "Couldn't load this view.",
  "common.retry": "Retry",
  "common.empty": "Nothing here yet.",

  "people.name": "Name",
  "people.email": "Email",
  "people.capturedBy": "Captured by",
  "person.consent": "Consent by purpose",

  "org.name": "Company",
  "org.industry": "Industry",
  "org.size": "Size",
  "org.classification": "Type",
  "org.firmographics": "Firmographics",
  "org.evidenceOrOmit": "evidence-backed fields only — absent means unknown",
  "org.domains": "Domains",

  "lead.score": "Score",
  "lead.status": "Status",
  "lead.segregated": "leads live apart from the contact graph until promoted",
  "lead.promote": "Promote to contact",
  "lead.promoteIneligible": "needs an email and an open status",

  "deals.viewBoard": "Board",
  "deals.viewTable": "Table",
  "deals.amount": "Value",
  "deals.stage": "Stage",
  "deals.close": "Expected close",
  "deals.confirmAdvance": "Move to {stage}?",
  "deals.confirmTerminal":
    "This closes the deal as {status}. Confirm first — nothing happens until you do.",
  "deals.lostReason": "Lost reason",
  "deals.confirm": "Confirm",
  "deals.cancel": "Cancel",
  "deals.advanced": "Moved to {stage}",
  "deal.pendingApprovals": "Awaiting your confirmation",
  "deal.stakeholders": "Stakeholders",

  "inbox.sub":
    "everything staged, waiting on your call — nothing runs without it",
  "inbox.expires": "expires {at}",
  "inbox.approveEdited": "Approve edited",
  "inbox.reject": "Reject",

  "home.brief": "Morning brief",
  "home.sub": "ranked from live signals — staged actions first",
  "home.quiet": "All quiet. Nothing staged, nothing stalled.",
  "home.staged": "Waiting on you",
  "home.stalled": "Stalled deals",

  "tasks.overdue": "Overdue",
  "tasks.today": "Today",
  "tasks.upcoming": "Upcoming",
  "tasks.undated": "No due date",
  "tasks.complete": "Done",
  "tasks.snooze": "Snooze 1d",

  "reports.sub": "deals by stage — unweighted next to weighted",
  "reports.count": "Deals",
  "reports.unweighted": "Unweighted",
  "reports.weighted": "Weighted",
  "reports.planNote":
    "the executed plan and the rows this number reconciles to",

  "ai.sub": "bring your own agent — governed by the two-tier contract",
  "ai.fromPalette": "From the palette",
  "ai.tiers": "What an agent may do",
  "ai.tierGreen": "Read & draft run instantly.",
  "ai.tierGreenDetail":
    "Lookups, summaries, drafts — visible, reversible, logged.",
  "ai.tierYellow": "Write & send wait for you.",
  "ai.tierYellowDetail":
    "External sends and record changes stage into the inbox first.",
  "ai.connect": "Connect an agent",
  "ai.connectDetail":
    "Mint a passport in Settings and point any MCP-capable agent at your workspace. It reads only what you can see.",
  "ai.paletteHint": "Ask from anywhere with",

  "settings.identity": "You",
  "settings.workspace": "Workspace connection",
  "settings.workspaceSub":
    "local dev only — production resolves the workspace from the subdomain",
  "settings.slug": "Workspace slug",
  "settings.saved": "Saved.",
  "settings.passports": "Agent passports",
  "settings.passportsSub":
    "an agent acts as you, never above you — every call re-checks your RBAC",
  "settings.passportLabel": "Agent name",
  "settings.mint": "Mint passport",
  "settings.tokenOnce": "Copy it now — this token is shown once.",
  "settings.autonomy": "Autonomy tiers",
  "settings.autonomySub": "what runs instantly vs. what waits in the inbox",
  "settings.tierRead": "Read, summarize, draft — runs instantly, fully logged.",
  "settings.tierSend":
    "Send email, book meetings, change records — waits for your approval.",
  "settings.tierAdvance": "Advance a deal stage — always confirm-first.",
  "settings.locked": "locked",
  "settings.purposes": "Consent purposes",
  "settings.privacy": "Privacy inbox",
  "settings.privacySub": "data-subject requests with their statutory deadlines",
  "settings.due": "due {date}",
} as const;

export type MessageKey = keyof typeof en;
