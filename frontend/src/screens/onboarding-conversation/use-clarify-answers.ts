import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { type Locale, useT } from "../../i18n";
import { ProblemError, problemMessageOf } from "../common";
import type { CompanyDraft } from "../onboarding";
import { onboardingDraftPayload } from "../onboarding";
import type { SuggestedCompanyChange } from "../onboarding-read";
import type { ClarifyAnswer } from "./company-proposal";
import { isCompanyField, legalEntityForOption } from "./company-proposal";
import { onboardingLocale } from "./onboarding-locale";

// Clarify answering with server authorization: a clicked option travels as
// selected_option, and ONLY the change matching that exact field+value
// auto-applies. A choice counts as recorded only once the authorization
// round-trip lands — on failure (or a reply that never confirmed the
// change) the answer rolls back, the question re-opens, and the human is
// told, so Continue can never ride on an unapplied decision.
//
// The legal-entity clarify is the one exception to "one field per answer":
// the contract authorizes exactly legal_name, by design (interfaces.md — a
// selected_option verifies a single field+value tuple and nothing else), so
// address and registration number are never in the server's reply. They
// were already on screen, in the SAME candidate the human just picked
// (CompanySiteRead.legal_entities, matched by name) — the entity fill pulls
// them from there once legal_name itself is authorized, with the same
// grounded provenance as any other site-read value, never as if it were a
// manual edit.
//
// One decision leaves one provenance, so the authorized legal_name travels
// with them rather than down the ordinary change path: that path is for a
// value the human TYPED and stamps it as their own assertion, which would
// leave the two fields the same pick filled reading as the site's evidence
// and the name they were picked by reading as hand-entered.

type MessageReply = components["schemas"]["OnboardingCompanyMessageReply"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type LegalEntity = components["schemas"]["CompanySiteReadLegalEntity"];

type OptionSelection = Readonly<{
  clarifyId: string;
  field: string;
  value: string;
  label: string;
}>;

export type ClarifyFailure =
  | { kind: "request"; detail: string }
  | { kind: "unconfirmed" };

// The one error mutationFn is allowed to throw whose words may reach the
// reader: it carries the messages endpoint's own RFC 7807 body, so the shown
// detail is a sentence the server composed. Any OTHER exception — a bug in
// this hook's own onSuccess handling included — is never trusted with a raw
// .message: onError below treats it the same as an unconfirmed choice rather
// than surfacing whatever it happened to say.
class ClarifyRequestError extends ProblemError {}

// The one field name the legal-entity clarify always answers; only an
// authorized change to THIS field ever triggers the entity-detail fill.
const LEGAL_ENTITY_FIELD = "legal_name";

// The candidate the human just picked, matched by the name the server
// authorized — never a different entity, never a guess. The match itself is
// legalEntityForOption, shared with every other surface that resolves an
// option value back to its candidate, so what counts as picked is one rule.
//
// Only the legal-entity clarify can fill a block: another question's option
// value may happen to read like a candidate's name without being an answer
// about which company this is. A pick with no candidate behind it goes down
// the ordinary change path instead.
function pickedLegalEntity(
  selection: OptionSelection,
  legalEntities: readonly LegalEntity[],
): LegalEntity | undefined {
  if (selection.field !== LEGAL_ENTITY_FIELD) {
    return undefined;
  }
  return legalEntityForOption(legalEntities, selection.value);
}

// Where an authorized change lands: through the entity fill when the human
// chose one of the read's own candidates, so the whole legal block keeps the
// site's evidence, and down the ordinary change path otherwise.
//
// The fallback covers the two cases where no candidate can carry the value:
// the read has none this pick resolves to (see pickedLegalEntity, which
// counts a nameless one as none), and a fill that throws. The choice is
// already recorded server-side by the time this runs, so it has to reach the
// draft either way — and a bug in the fill is a lesser, separate failure
// that must never be reported as if the choice itself had failed, so it is
// caught and logged rather than left to bubble into onError's (unrelated)
// authorization-failure handling.
function applyAuthorizedChoice(
  authorized: readonly SuggestedCompanyChange[],
  entity: LegalEntity | undefined,
  applyChanges: (changes: readonly SuggestedCompanyChange[]) => void,
  applyLegalEntity: (entity: LegalEntity) => void,
): void {
  if (entity === undefined) {
    applyChanges(authorized);
    return;
  }
  try {
    applyLegalEntity(entity);
  } catch (fillError) {
    console.error("legal-entity fill failed", fillError);
    applyChanges(authorized);
  }
}

type UseClarifyAnswersArgs = Readonly<{
  locale: Locale;
  /** Live view of the latest proposal; a ref because the proposal query
   * depends on this hook's answers (the two would otherwise cycle). */
  proposalRef: Readonly<{ current: Proposal | undefined }>;
  draftRef: Readonly<{ current: CompanyDraft }>;
  /** Live view of the read's own candidates, for the legal-entity fill
   * below; a ref for the same reason draftRef and proposalRef are. */
  legalEntitiesRef: Readonly<{ current: readonly LegalEntity[] }>;
  history: () => components["schemas"]["CompanySiteReadConversationTurn"][];
  applyChanges: (changes: readonly SuggestedCompanyChange[]) => void;
  /** Merges the chosen entity's whole grounded block — the authorized name
   * included — into the draft. Kept apart from applyChanges: an entity pick
   * carries its own provenance and its own never-overwrite-an-edit guard,
   * both decided by the draft helper it calls rather than at this call
   * site. */
  applyLegalEntity: (entity: LegalEntity) => void;
}>;

export function useClarifyAnswers({
  locale,
  proposalRef,
  draftRef,
  legalEntitiesRef,
  history,
  applyChanges,
  applyLegalEntity,
}: UseClarifyAnswersArgs) {
  const t = useT();
  const queryClient = useQueryClient();
  const [answers, setAnswers] = useState<ClarifyAnswer[]>([]);
  const [failure, setFailure] = useState<ClarifyFailure | null>(null);

  const rollback = useCallback((clarifyId: string) => {
    setAnswers((current) =>
      current.filter((answer) => answer.clarifyId !== clarifyId),
    );
  }, []);

  const selectOption = useMutation({
    mutationFn: async (selection: OptionSelection): Promise<MessageReply> => {
      const { data, error } = await api.POST("/onboarding/company/messages", {
        body: {
          message: selection.label,
          locale: onboardingLocale(locale),
          act: "company",
          selected_option: {
            clarify_id: selection.clarifyId,
            field: selection.field,
            value: selection.value,
          },
          history: history(),
          company_draft: onboardingDraftPayload(draftRef.current.values),
        },
      });
      if (error) {
        throw new ClarifyRequestError(error);
      }
      return data;
    },
    onSuccess: (reply, selection) => {
      const authorized = reply.proposed_changes.filter(
        (change) =>
          change.field === selection.field && change.value === selection.value,
      );
      // "Keep what I already have" needs no change; anything else without a
      // server-confirmed change would save the old, ambiguous value.
      const values = draftRef.current.values;
      const changeNeeded =
        isCompanyField(selection.field, values) &&
        values[selection.field] !== selection.value;
      if (authorized.length > 0) {
        applyAuthorizedChoice(
          authorized,
          pickedLegalEntity(selection, legalEntitiesRef.current),
          applyChanges,
          applyLegalEntity,
        );
      } else if (changeNeeded) {
        rollback(selection.clarifyId);
        setFailure({ kind: "unconfirmed" });
      }
      queryClient.invalidateQueries({
        queryKey: ["onboarding-company-proposal"],
      });
    },
    onError: (error, selection) => {
      rollback(selection.clarifyId);
      if (error instanceof ClarifyRequestError) {
        setFailure({ kind: "request", detail: problemMessageOf(error, t) });
        return;
      }
      // Never a raw exception message in the sentence the human reads. The
      // failure itself needs no report from here: every mutation the
      // application runs is observed by the client's own sink, which keeps
      // exactly the ones nobody wrote words for (app/queryclient.ts,
      // FE-PARAM-4) — a second copy here would report one failure twice.
      setFailure({ kind: "unconfirmed" });
    },
  });

  const answerClarify = useCallback(
    (clarifyId: string, value: string) => {
      const clarify = (proposalRef.current?.open_questions ?? []).find(
        (question) => question.id === clarifyId,
      );
      if (!clarify) {
        return;
      }
      const option = clarify.options.find(
        (candidate) => candidate.value === value,
      );
      setFailure(null);
      setAnswers((current) => [
        ...current.filter((answer) => answer.clarifyId !== clarifyId),
        { clarifyId, field: clarify.field, value },
      ]);
      selectOption.mutate({
        clarifyId,
        field: clarify.field,
        value,
        label: option?.label ?? value,
      });
    },
    [proposalRef, selectOption],
  );

  // Dismissal is a LOCAL decision, no authorization round trip: nothing is
  // written to the field, so there is nothing for the server to confirm.
  // Recording it as an answer stops the question from counting as an open
  // decision; the confirm resolutions map it per its comparison kind.
  const dismissClarify = useCallback(
    (clarifyId: string, autoResolved = false) => {
      const clarify = (proposalRef.current?.open_questions ?? []).find(
        (question) => question.id === clarifyId,
      );
      if (!clarify) {
        return;
      }
      setFailure(null);
      setAnswers((current) => [
        ...current.filter((answer) => answer.clarifyId !== clarifyId),
        {
          clarifyId,
          field: clarify.field,
          value: "",
          dismissed: true,
          autoResolved,
        },
      ]);
    },
    [proposalRef],
  );

  return {
    answers,
    answerClarify,
    dismissClarify,
    authorizing: selectOption.isPending,
    failure,
    // The clarify round trip is a real model call now that the rail carries
    // no free-text chat to report one from — the cost bar must still show
    // it, so the latest authorized reply's own tally rides along.
    runtime: selectOption.data?.ai_runtime,
  };
}
