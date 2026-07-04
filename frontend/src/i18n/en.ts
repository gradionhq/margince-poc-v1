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
} as const;

export type MessageKey = keyof typeof en;
