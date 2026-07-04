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
} as const;

export type MessageKey = keyof typeof en;
