// Pure helpers for the custom-fields admin screen — no React, unit-tested in
// isolation so the screen's derivations (immutable API key, the pending DDL
// preview, and the structural-word refusal) are proven independently.

export type CfObject = "deal" | "organization" | "person" | "lead";
export type CfType =
  | "text"
  | "number"
  | "date"
  | "currency"
  | "picklist"
  | "boolean";

// Chip order is normative (AC-custom-fields-2): Deal, Company, Contact, Lead.
// PARAM-2 also pins `activity` as a target object; the screen AC enumerates
// only these four, so we follow the screen AC and omit activity here — flagged
// for the docs chain-rule rather than silently added.
export const CF_OBJECTS: readonly CfObject[] = [
  "deal",
  "organization",
  "person",
  "lead",
];

export const CF_TYPES: readonly CfType[] = [
  "text",
  "number",
  "date",
  "currency",
  "picklist",
  "boolean",
];

export function slug(label: string): string {
  return label
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_|_$/g, "");
}

export function columnName(label: string): string {
  return `cf_${slug(label) || "…"}`;
}

export function apiKey(object: CfObject, label: string): string {
  return `${object}.${columnName(label)}`;
}

// The physical storage note per type (CUSTOM-FIELDS-PARAM-4) — mirrors what the
// governed engine emits, shown before Confirm so the admin sees the exact
// pending schema change.
function storageNote(type: CfType, currency: string): string {
  switch (type) {
    case "text":
      return "text";
    case "number":
      return "numeric";
    case "date":
      return "date";
    case "currency":
      return `numeric · cents · ${currency || "EUR"}`;
    case "picklist":
      return "enum";
    case "boolean":
      return "boolean";
  }
}

export function ddlPreview(
  object: CfObject,
  label: string,
  type: CfType,
  currency: string,
): string {
  return `ALTER ${object} ADD COLUMN ${columnName(label)} (${storageNote(
    type,
    currency,
  )}) · backfilled NULL · reversible`;
}

// A field is data; an object or a relationship is structure. A label that reads
// like a structural request is refused pre-emptively (AC-custom-fields-5).
//
// Single, bare structural nouns match on WORD BOUNDARIES so an innocent label
// that merely contains one as a substring ("Objective score" has "object",
// "Notable accounts" / "Portable device" have "table") is not refused. The
// multi-word relationship phrases are distinctive enough to match as substrings.
// Word-boundary alternatives, singular and plural (entity → entities), so
// "New objects" / "Related entities" are refused, not just their singular.
const STRUCTURE_TOKENS: readonly string[] = [
  "objects?",
  "tables?",
  "entit(?:y|ies)",
];

const STRUCTURE_PHRASES: readonly string[] = [
  "relationship",
  "link to",
  "related to",
  "lookup to",
  "connect to",
  "associate with",
  "child of",
  "parent of",
  "many-to-many",
  "one-to-many",
];

const STRUCTURE_TOKEN_RE = new RegExp(
  `\\b(?:${STRUCTURE_TOKENS.join("|")})\\b`,
);

export function looksStructural(label: string): boolean {
  const l = label.toLowerCase();
  if (STRUCTURE_TOKEN_RE.test(l)) {
    return true;
  }
  return STRUCTURE_PHRASES.some((phrase) => l.includes(phrase));
}
