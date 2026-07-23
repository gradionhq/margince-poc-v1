import type { components } from "../../api/schema";
import type {
  CompanyDraft,
  CompanyFieldName,
  CompanyForm,
} from "../onboarding";
import { REQUIRED_FIELDS } from "../onboarding";
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

// Choosing an entity fills one intact legal block and keeps its website
// provenance (the read grounded it; the human only chose which block). A
// detail the notice left blank is cleared rather than inherited. Mirrors the
// classic coordinator's entity pick so both shells stamp provenance alike.
export function draftWithLegalEntity(
  draft: CompanyDraft,
  entity: LegalEntity,
): CompanyDraft {
  const grounded = { ...draft.grounded };
  const edited = new Set(draft.edited);
  const values = { ...draft.values };
  const applied: Array<[ColdField["field"], string]> = [
    ["legal_name", entity.name],
    ["registered_address", entity.registered_address ?? ""],
    ["register_vat", entity.register_number ?? ""],
  ];
  for (const [field, value] of applied) {
    values[field] = value;
    edited.delete(field);
    if (value === "") {
      delete grounded[field];
      continue;
    }
    grounded[field] = {
      field,
      value,
      evidence_snippet: entity.evidence_snippet ?? value,
      source_kind: "url",
      source_url: entity.source_url,
      confidence: 1,
    };
  }
  return { values, grounded, edited };
}
