import type { components } from "../../api/schema";
import type {
  CompanyDraft,
  CompanyFieldName,
  CompanyForm,
} from "../onboarding";
import { provenanceOf, REQUIRED_FIELDS } from "../onboarding";
import type { ConversationQuestion } from "./conversation-machine";

// Pure mappings between the server's proposal/clarify payloads and the
// conversation machine's vocabulary. Nothing here renders or fetches; the
// company act driver calls these and dispatches the results.

type OnboardingClarify = components["schemas"]["OnboardingClarify"];
type Comparison = components["schemas"]["CompanySiteReadComparison"];
type Resolution = components["schemas"]["CompanySiteReadResolution"];
type ProposalField = components["schemas"]["OnboardingCompanyProposalField"];
type LegalEntity = components["schemas"]["CompanySiteReadLegalEntity"];
type ColdField = components["schemas"]["ColdStartField"];
type SiteReadPage = components["schemas"]["CompanySiteReadPage"];

export type ClarifyAnswer = {
  clarifyId: string;
  field: string;
  value: string;
  /** The human declined the question (humans outrank the reader): nothing
   * is written to the field, and it stops counting as an open decision. */
  dismissed?: boolean;
};

// Whether a clarify sits over a human_conflict comparison: dismissing one of
// those still needs an explicit server resolution (keep_current), so its
// dismiss action is labeled "keep my value" instead of "skip".
export function isConflictClarify(
  field: string,
  comparisons: readonly Comparison[] | undefined,
): boolean {
  return (comparisons ?? []).some(
    (comparison) =>
      comparison.key === field &&
      comparison.classification === "human_conflict",
  );
}

// A server clarify becomes a machine question: the deterministic server copy
// rides verbatim as params through passthrough catalog keys, so the renderer
// keeps its i18n-only contract without paraphrasing what the server asked.
// Every clarify is dismissible — an implausible question must never become
// an unanswerable gate.
export function toMachineQuestion(
  clarify: OnboardingClarify,
  comparisons?: readonly Comparison[],
): ConversationQuestion {
  return {
    id: clarify.id,
    i18nKey: "ob.conv.clarify.question",
    params: { question: clarify.question },
    dismissLabelKey: isConflictClarify(clarify.field, comparisons)
      ? "ob.conv.clarify.keepMine"
      : "ob.conv.clarify.dismiss",
    options: clarify.options.map((option) => {
      const detail = option.detail ?? option.evidence_snippet ?? null;
      return {
        value: option.value,
        label: option.label,
        ...(detail === null
          ? {}
          : {
              detailKey: "ob.conv.clarify.optionDetail" as const,
              params: { detail },
            }),
      };
    }),
  };
}

// The spec's evidence-or-omit floor (craftsmanship/threat model: an AI-shown
// field needs confidence >= 0.55 plus a verbatim snippet). The server
// proposal applies it; the client-side fallback must not weaken it.
const MIN_PROPOSAL_CONFIDENCE = 0.55;

// The review card's payload when the proposal endpoint is unavailable: the
// same deterministic mapping, computed client-side from the site-read
// snapshot the poll already delivered. The same confidence floor and
// evidence-or-omit gate apply; open questions are unknown here, so none are
// asked, and confirm keeps the read's own draft_version + proposal_hash.
export function proposalFromRead(
  read: components["schemas"]["CompanySiteRead"],
): components["schemas"]["OnboardingCompanyProposal"] {
  return {
    ready: true,
    fields: read.profile_fields
      .filter((field) => field.confidence >= MIN_PROPOSAL_CONFIDENCE)
      .map((field) => ({
        field: field.field,
        value: field.value,
        confidence: field.confidence,
        evidence_snippet: field.evidence_snippet,
        source_url: field.source_url ?? read.root_url,
      })),
    facts: [...read.facts],
    open_questions: [],
    remaining_required_fields: [],
    draft_version: read.draft_version,
    proposal_hash: read.proposal_hash,
  };
}

// Evidence-or-omit: a proposal row without a verbatim snippet never renders.
export function evidencedFields(
  fields: readonly ProposalField[] | undefined,
): ProposalField[] {
  return (fields ?? []).filter((field) => field.evidence_snippet.trim() !== "");
}

// The proposal names fields as plain strings; only ones the form vocabulary
// knows can be shown with the human's current draft value. Own-property
// check: an unexpected server field named like an Object.prototype member
// ("toString") must not masquerade as a form field.
export function isCompanyField(
  field: string,
  values: CompanyForm,
): field is CompanyFieldName {
  return Object.hasOwn(values, field);
}

export function missingRequiredFields(values: CompanyForm): CompanyFieldName[] {
  return REQUIRED_FIELDS.filter((field) => values[field].trim() === "");
}

// A clarify answered over a human_conflict comparison maps 1:1 onto the
// confirm request's resolution vocabulary. Other clarifies (the legal-entity
// choice) resolve through the profile values themselves and produce none —
// the server rejects a resolution whose key is not a current human conflict,
// and requires one for every conflict, so a dismissed conflict maps to
// keep_current while a dismissed census question sends nothing.
export function resolutionsFromAnswers(
  comparisons: readonly Comparison[],
  answers: readonly ClarifyAnswer[],
): Resolution[] {
  const resolutions: Resolution[] = [];
  for (const answer of answers) {
    const conflict = comparisons.find(
      (comparison) =>
        comparison.key === answer.field &&
        comparison.classification === "human_conflict",
    );
    if (!conflict) {
      continue;
    }
    if (answer.dismissed === true) {
      resolutions.push({ key: conflict.key, action: "keep_current" });
    } else if (answer.value === conflict.proposed_value) {
      resolutions.push({ key: conflict.key, action: "accept_proposal" });
    } else if (answer.value === (conflict.current_value ?? "")) {
      resolutions.push({ key: conflict.key, action: "keep_current" });
    } else {
      resolutions.push({
        key: conflict.key,
        action: "use_value",
        value: answer.value,
      });
    }
  }
  return resolutions;
}

// The one whitespace rule an option value is built with: trimmed, then every
// run of whitespace collapsed to a single space. The server normalizes each
// printed candidate this way before offering it (compose's clarify option
// builder), so a clicked value is a normalized copy of a name the read still
// carries exactly as the page printed it.
function collapseWhitespace(value: string): string {
  return value.trim().replace(/\s+/g, " ");
}

// The candidate an option value names, matched through that same rule on both
// sides — anything less calls a name with doubled or internal whitespace a
// candidate nobody offered, and every detail hanging off the candidate (its
// address, registration number, quote and page) goes missing with it.
//
// A candidate the read could not name matches nothing, whatever was clicked:
// the entity fill settles nothing about legal_name for one, by design, so
// treating it as picked would fill address and registration number from an
// entity nothing on screen identifies.
//
// Nor does an ambiguous one. Two candidates can print the same name and carry
// different registrations, and the option names only the name — so taking the
// first would hang one company's address and register number off another
// company's identity, the mixture this block exists to prevent. The
// confirmation refuses the same case for the same reason: it grounds a legal
// block only when the details match one and only one stored entity.
export function legalEntityForOption(
  entities: readonly LegalEntity[],
  optionValue: string,
): LegalEntity | undefined {
  const picked = collapseWhitespace(optionValue);
  if (picked === "") {
    return undefined;
  }
  const named = entities.filter(
    (candidate) => collapseWhitespace(candidate.name) === picked,
  );
  return named.length === 1 ? named[0] : undefined;
}

// Evidence-or-omit for a candidate: the read's verbatim quote when it captured
// one, and otherwise nothing at all. A value is never its own evidence.
function quotedEvidence(entity: LegalEntity): string | undefined {
  const snippet = entity.evidence_snippet;
  return snippet !== undefined && snippet.trim() !== "" ? snippet : undefined;
}

// Choosing an entity fills one intact legal block. There is one such gesture,
// wherever it is offered — the clarify's candidate list and the dossier's
// entity cards ask the same question of the same candidates — so it settles
// one way: the chosen name follows the pick even over a name the human typed
// earlier, because their old text standing beside this candidate's address and
// registration number would describe two companies at once. The details that
// ride along were never what was asked, so an edit there still wins, and a
// detail this candidate does not carry is left exactly as it was, never
// blanked.
//
// The block keeps the candidate's own provenance, and only what the candidate
// actually has: the page it was printed on, plus the verbatim quote when the
// read captured one. No confidence — see FieldGrounding.
export function draftWithLegalEntity(
  draft: CompanyDraft,
  entity: LegalEntity,
): CompanyDraft {
  const edited = new Set(draft.edited);
  if (entity.name.trim() !== "") {
    // The human mark goes with the value: they selected what the read had
    // already grounded instead of authoring anything. A candidate with no
    // name settles nothing about legal_name, and dropping the mark there
    // would leave the human's own text looking site-sourced.
    edited.delete("legal_name");
  }
  const grounded = { ...draft.grounded };
  const values = { ...draft.values };
  const snippet = quotedEvidence(entity);
  const candidates: ReadonlyArray<[ColdField["field"], string | undefined]> = [
    ["legal_name", entity.name],
    ["registered_address", entity.registered_address],
    ["register_vat", entity.register_number],
  ];
  for (const [field, value] of candidates) {
    if (edited.has(field) || value === undefined || value.trim() === "") {
      continue;
    }
    values[field] = value;
    grounded[field] = {
      field,
      value,
      evidence_snippet: snippet,
      source_kind: "url",
      source_url: entity.source_url,
    };
  }
  return { values, grounded, edited };
}

// One entity on the whole site is not a question, so nothing is asked and
// nothing is guessed: the read printed a company off its own imprint, and its
// block is a read-back value like any other. It is applied because the two
// extraction lanes disagree about the same page — the census can quote
// "ScaleCommerce GmbH, Horstweg 24" verbatim while the profile lane returns
// none of the trio — and without this the human is shown three blank fields
// while the read is holding the answer, snippet and source URL included.
//
// Only the unanswered fields: nobody chose this candidate, so it must not
// displace a value the profile lane did ground, nor one the human typed — nor
// a field the human deliberately emptied, because clearing a box is an answer
// and the empty box IS it. That last one needs saying separately: a cleared
// field looks blank, and draftWithLegalEntity settles the legal name over a
// human mark on purpose, which is right for a pick and wrong here, where
// nobody picked anything. Handing that function an entity stripped of the
// parts already answered says all of it in the vocabulary it already has.
export function draftWithSoleLegalEntity(
  draft: CompanyDraft,
  entities: readonly LegalEntity[] | undefined,
): CompanyDraft {
  const [entity, ...rest] = entities ?? [];
  if (entity === undefined || rest.length > 0) {
    return draft;
  }
  const unanswered = (field: CompanyFieldName): boolean =>
    draft.values[field].trim() === "" && !draft.edited.has(field);
  return draftWithLegalEntity(draft, {
    ...entity,
    name: unanswered("legal_name") ? entity.name : "",
    registered_address: unanswered("registered_address")
      ? entity.registered_address
      : undefined,
    register_number: unanswered("register_vat")
      ? entity.register_number
      : undefined,
  });
}

// The three fields only a legal/imprint page can ground — the same trio
// draftWithLegalEntity fills and the server's own legal gate governs. A
// blank display_name or offer_summary carries no such page to have checked,
// so this stays scoped to the trio rather than every empty field.
const LEGAL_TRIO_FIELDS: ReadonlySet<CompanyFieldName> = new Set([
  "legal_name",
  "registered_address",
  "register_vat",
]);

export type LegalFieldGap = "not-published" | "not-checked" | "unpicked";

// Why one of the legal trio is blank, from what the read's own crawl
// actually saw — never a guess. "not-published" only fires once a page the
// read classified as an imprint/legal notice was genuinely fetched: the
// read looked and the site simply does not state it. Anything short of
// that (no such page found, one found but skipped or failed to fetch) is
// "not-checked" — the honest admission that nothing was there TO read, as
// opposed to a claim that the site was searched and came up empty. A field
// outside the trio, or one that already carries a value, has no gap to name.
// Neither does a field on the manual path: with no crawl behind it there is
// no "did not find" to report, only a blank the person has not filled yet.
//
// A candidate that carries the field outranks both, because the page plainly
// states it and "the page does not state it" would be false. What is left to
// say then depends on how many companies stand on that page, and on whether
// this draft has already answered which one is ours. Several, unanswered: the
// read deliberately proposes none and waits for the human to choose, which is
// the choice "unpicked" names. Otherwise — one company on the page, or a
// choice already made — there is no choice left to point at, and naming one
// would send a reader back to a decision they have already taken.
export function legalFieldGap(
  field: CompanyFieldName,
  pages: readonly SiteReadPage[] | undefined,
  draft: CompanyDraft,
  entities?: readonly LegalEntity[],
): LegalFieldGap | null {
  if (!LEGAL_TRIO_FIELDS.has(field) || pages === undefined) {
    return null;
  }
  const candidates = entities ?? [];
  if (candidates.some((entity) => entityCarries(entity, field))) {
    return candidates.length > 1 && !legalBlockChosen(draft)
      ? "unpicked"
      : null;
  }
  const sawLegalPage = pages.some(
    (page) => page.kind === "impressum" && page.status === "fetched",
  );
  return sawLegalPage ? "not-published" : "not-checked";
}

// Whether this draft has already settled which candidate is ours. The census
// lane is the only one that grounds a value nothing measured — the graded
// extraction lane always carries a confidence (ColdStartField) — so an
// ungraded grounding anywhere in the legal trio IS a candidate's block sitting
// in the draft, which only a pick (or a one-company imprint) puts there.
//
// Asked of the whole block rather than the field in hand, because that is what
// survives what the reader does next: clearing a box drops the grounding of
// that field alone (changeDraftField), so the rest of the block still names
// the entity the choice landed on — including when the cleared box is the very
// name it was picked by.
function legalBlockChosen(draft: CompanyDraft): boolean {
  for (const field of LEGAL_TRIO_FIELDS) {
    const grounding = provenanceOf(draft, field);
    if (grounding !== null && grounding.confidence === undefined) {
      return true;
    }
  }
  return false;
}

function entityCarries(entity: LegalEntity, field: CompanyFieldName): boolean {
  const carried =
    field === "legal_name"
      ? entity.name
      : field === "registered_address"
        ? entity.registered_address
        : entity.register_number;
  return carried !== undefined && carried.trim() !== "";
}
