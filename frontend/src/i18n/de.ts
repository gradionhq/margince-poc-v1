import type { MessageKey } from "./en";

// German catalog — the A24 default locale. `satisfies` forces exact key
// parity with en at compile time; i18n.test.ts re-checks it at runtime so a
// build without typechecking still fails loudly.
export const de = {
  "app.title": "Margince Design-Tokens",
  "app.subtitle":
    "Ledger Green (ADR-0040) — die kanonischen Werte spiegeln die Spec-Mockups; Tests pinnen sie.",
  "theme.toDark": "Dunkles Design",
  "theme.toLight": "Helles Design",

  "section.surfaces": "Flächen",
  "section.accentAi": "Akzent & KI",
  "section.text": "Text",
  "section.status": "Status",
  "section.typeRamp": "Schriftgrößen",
  "section.trust": "Vertrauens-Primitive (B-EP09.3a)",

  "type.display": "Display — Outfit 600",
  "type.body": "Fließtext — DM Sans 400, die Standard-Leseschrift.",
  "type.mono": "Mono — JetBrains Mono, Evidenz-Snippets und IDs.",

  "trust.accept": "Übernehmen",
  "trust.edit": "Bearbeiten",
  "trust.dismiss": "Verwerfen",
  "trust.save": "Speichern",
  "trust.typedByYou": "von dir eingetragen",
  "trust.agentTag": "Agent: {agent}",
  "trust.dismissed": "Vorschlag verworfen.",
  "trust.stagedProposal": "vorgemerkter Vorschlag",
  "trust.resolvedValue": "übernommener Wert",
  "trust.editValue": "{description} bearbeiten",

  "confidence.high": "hoch",
  "confidence.med": "mittel",
  "confidence.low": "niedrig",

  "autonomy.auto": "automatisch",
  "autonomy.confirm": "erst bestätigen",

  "nav.home": "Start",
  "nav.contacts": "Kontakte",
  "nav.companies": "Firmen",
  "nav.leads": "Leads",
  "nav.deals": "Deals",
  "nav.tasks": "Aufgaben",
  "nav.inbox": "Eingang",
  "nav.reports": "Berichte",
  "nav.ai": "KI fragen",
  "nav.settings": "Einstellungen",
  "nav.design": "Designsystem",

  "shell.railAria": "Hauptnavigation",
  "shell.logoAria": "Margince",
  "shell.search": "Suche",
  "locale.toEnglish": "Auf Englisch umschalten",
  "locale.toGerman": "Auf Deutsch umschalten",

  "screen.pending":
    "Noch nicht gebaut — diese Oberfläche kommt mit ihrem Build-Ticket.",
} as const satisfies Record<MessageKey, string>;
