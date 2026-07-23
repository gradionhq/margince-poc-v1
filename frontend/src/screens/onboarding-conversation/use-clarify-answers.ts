import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import type { Locale } from "../../i18n";
import { problemMessage } from "../common";
import type { CompanyDraft } from "../onboarding";
import { onboardingDraftPayload } from "../onboarding";
import type { SuggestedCompanyChange } from "../onboarding-read";
import type { ClarifyAnswer } from "./company-proposal";
import { isCompanyField } from "./company-proposal";

// Clarify answering with server authorization: a clicked option travels as
// selected_option, and ONLY the change matching that exact field+value
// auto-applies. A choice counts as recorded only once the authorization
// round-trip lands — on failure (or a reply that never confirmed the
// change) the answer rolls back, the question re-opens, and the human is
// told, so Accept all can never ride on an unapplied decision.

type MessageReply = components["schemas"]["OnboardingCompanyMessageReply"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];

type OptionSelection = Readonly<{
  clarifyId: string;
  field: string;
  value: string;
  label: string;
}>;

export type ClarifyFailure =
  | { kind: "request"; detail: string }
  | { kind: "unconfirmed" };

type UseClarifyAnswersArgs = Readonly<{
  locale: Locale;
  /** Live view of the latest proposal; a ref because the proposal query
   * depends on this hook's answers (the two would otherwise cycle). */
  proposalRef: Readonly<{ current: Proposal | undefined }>;
  draftRef: Readonly<{ current: CompanyDraft }>;
  history: () => components["schemas"]["CompanySiteReadConversationTurn"][];
  applyChanges: (changes: readonly SuggestedCompanyChange[]) => void;
}>;

export function useClarifyAnswers({
  locale,
  proposalRef,
  draftRef,
  history,
  applyChanges,
}: UseClarifyAnswersArgs) {
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
          locale,
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
        throw new Error(problemMessage(error));
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
        applyChanges(authorized);
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
      setFailure({ kind: "request", detail: error.message });
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

  return {
    answers,
    answerClarify,
    authorizing: selectOption.isPending,
    failure,
  };
}
