import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Send } from "lucide-react";
import type { Dispatch, SetStateAction } from "react";
import { useCallback, useMemo, useRef, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import type { MarginceCoreState } from "../../design-system/margince-core";
import { useLocale, useT } from "../../i18n";
import { problemMessage } from "../common";
import type { CompanyDraft } from "../onboarding";
import {
  changeDraftField,
  EMPTY_DRAFT,
  normalizeUrl,
  onboardingDraftPayload,
} from "../onboarding";
import {
  ConversationEntries,
  conversationHistory,
  type SuggestedCompanyChange,
  useCompanyConversation,
} from "../onboarding-read";
import type { ArtifactMode, FindingHighlight } from "./artifact";
import { CompanyActArtifact } from "./artifact";
import type { ClarifyAnswer } from "./company-proposal";
import {
  draftWithLegalEntity,
  missingRequiredFields,
  proposalFromRead,
  resolutionsFromAnswers,
} from "./company-proposal";
import { CompanyConfirmCard } from "./confirm-card";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { ConversationThread } from "./thread";
import { useCompanyRead } from "./use-company-read";
import { useWizardStatePersist } from "./use-wizard-state";
import { ConversationWorkbench } from "./workbench";

// The company act driver: the read lifecycle lives in useCompanyRead; this
// component owns the draft, the free-text chat, clarify answers (which also
// travel to the server as the authorizing selected_option), and the one
// explicit confirmation — all expressed as machine events, so the pure
// reducer stays the single truth about where the conversation is.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type CompanyProfile = components["schemas"]["CompanyProfile"];
type MessageReply = components["schemas"]["OnboardingCompanyMessageReply"];
type AiRunSummary = components["schemas"]["AiRunSummary"];

type CompanyActProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
}>;

type OptionSelection = Readonly<{
  clarifyId: string;
  field: string;
  value: string;
  label: string;
}>;

function corePresence(
  state: ConversationState,
  read: CompanySiteRead | null,
  startFailed: boolean,
): MarginceCoreState {
  if (startFailed || read?.status === "failed") {
    return "error";
  }
  if (read?.status === "deferred") {
    return "quiet";
  }
  if (
    state.phase === "co.reading" &&
    (read?.status === "queued" || read?.status === "reading")
  ) {
    return "working";
  }
  if (state.phase === "co.clarify") {
    return "attention";
  }
  if (state.phase === "co.review" || state.phase === "co.confirmed") {
    return "success";
  }
  return "listening";
}

// biome-ignore lint/complexity/noExcessiveCognitiveComplexity: the act driver is one machine-shaped surface; splitting it further would scatter the event wiring
export function CompanyAct({ state, dispatch }: CompanyActProps) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();

  // Draft state mirrors the classic coordinator: values + grounding +
  // human-edited marks move together, and a ref keeps callbacks current.
  const [draft, setDraftState] = useState<CompanyDraft>(EMPTY_DRAFT);
  const draftRef = useRef<CompanyDraft>(EMPTY_DRAFT);
  const setDraft = useCallback((update: SetStateAction<CompanyDraft>) => {
    const next =
      typeof update === "function" ? update(draftRef.current) : update;
    draftRef.current = next;
    setDraftState(next);
  }, []);

  const [selectedFactKeys, setSelectedFactKeys] = useState<string[]>([]);
  const [answers, setAnswers] = useState<ClarifyAnswer[]>([]);
  const [artifactMode, setArtifactMode] = useState<ArtifactMode>("dossier");
  const [applied, setApplied] = useState<ReadonlySet<string>>(new Set());
  const machine = useRef(state);
  machine.current = state;

  const { persistReadStart } = useWizardStatePersist();
  // The proposal endpoint joins through persisted wizard state, so the
  // running read is recorded the moment it starts.
  const onReadStarted = useCallback(
    (started: CompanySiteRead) => {
      void persistReadStart({
        url: started.root_url,
        readId: started.id,
        values: draftRef.current.values,
      });
    },
    [persistReadStart],
  );

  const { startRead, siteRead, proposal, prevSnapshot } = useCompanyRead({
    dispatch,
    machine,
    setDraft,
    setSelectedFactKeys,
    answers,
    onReadStarted,
  });

  const applyChanges = useCallback(
    (changes: readonly SuggestedCompanyChange[]) => {
      setDraft((current) => {
        let next = current;
        for (const change of changes) {
          next = changeDraftField(next, change.field, change.value);
        }
        return next;
      });
    },
    [setDraft],
  );

  const conversation = useCompanyConversation(
    "website",
    locale,
    t("ob.ai.readFirst"),
    onboardingDraftPayload(draft.values),
    "company",
  );

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
          history: conversationHistory(conversation.entries),
          company_draft: onboardingDraftPayload(draftRef.current.values),
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (reply, selection) => {
      // The selection authorizes exactly one change; anything else the model
      // volunteered still needs the human's explicit Apply.
      const authorized = reply.proposed_changes.filter(
        (change) =>
          change.field === selection.field && change.value === selection.value,
      );
      if (authorized.length > 0) {
        applyChanges(authorized);
      }
      queryClient.invalidateQueries({
        queryKey: ["onboarding-company-proposal"],
      });
    },
  });

  const answerClarify = useCallback(
    (clarifyId: string, value: string) => {
      const clarify = (proposal.data?.open_questions ?? []).find(
        (question) => question.id === clarifyId,
      );
      if (!clarify) {
        return;
      }
      const option = clarify.options.find(
        (candidate) => candidate.value === value,
      );
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
    [proposal.data, selectOption],
  );

  // The machine routes the answer itself: with the read outcome recorded
  // (readCompleted) it proceeds straight to review; remaining open questions
  // surface in the confirm card, since the run retired at the terminal and a
  // post-terminal CLARIFY would be stale.
  const handleAnswer = useCallback(
    (questionId: string, value: string) => {
      dispatch({ type: "QUESTION_ANSWERED", questionId, value });
      answerClarify(questionId, value);
    },
    [dispatch, answerClarify],
  );

  const confirm = useMutation({
    mutationFn: async (): Promise<CompanyProfile> => {
      const values = draftRef.current.values;
      const profile = {
        ...values,
        display_name: values.display_name.trim(),
        offer_summary: values.offer_summary.trim(),
        icp: values.icp.trim(),
        legal_name: values.legal_name.trim(),
        registered_address: values.registered_address.trim(),
        register_vat: values.register_vat.trim(),
        industry: values.industry.trim(),
      };
      const read = prevSnapshot.current;
      // When the proposal endpoint failed, the read snapshot carries the same
      // version pair, so the staged-confirm contract still holds.
      const proposalData =
        proposal.data ?? (read !== null ? proposalFromRead(read) : undefined);
      const result =
        read !== null &&
        (read.status === "ready" || read.status === "partial") &&
        proposalData?.draft_version !== undefined &&
        proposalData.proposal_hash !== undefined
          ? await api.POST("/company/site-reads/{readId}/confirm", {
              params: {
                path: { readId: read.id },
                header: { "Idempotency-Key": crypto.randomUUID() },
              },
              body: {
                draft_version: proposalData.draft_version,
                proposal_hash: proposalData.proposal_hash,
                profile,
                selected_fact_keys: selectedFactKeys,
                resolutions: resolutionsFromAnswers(read.comparisons, answers),
              },
            })
          : await api.PUT("/company", { body: profile });
      const { data, error } = result;
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (profileData) => {
      // The shell's onboarding gate reads the same ["company"] cache entry.
      queryClient.setQueryData(["company"], profileData);
      dispatch({ type: "COMPANY_CONFIRMED" });
    },
  });

  const submitComposer = () => {
    const text = conversation.draft.trim();
    if (text === "" || startRead.isPending || conversation.send.isPending) {
      return;
    }
    const norm = normalizeUrl(text);
    if (
      norm.ok &&
      (state.phase === "co.intro" || state.phase === "co.reading")
    ) {
      conversation.setDraft("");
      // The composer is this shell's website field: the confirm contract
      // sends the profile's website like the classic form does, so the
      // canonical URL lands in the draft the moment it is submitted.
      setDraft((current) => changeDraftField(current, "website", norm.full));
      dispatch({ type: "URL_SUBMITTED", url: norm.full });
      startRead.mutate(norm.full);
      return;
    }
    conversation.submit();
  };

  const read = siteRead.data ?? startRead.data ?? null;
  const missing = missingRequiredFields(draft.values);

  // The review renders even when the proposal endpoint failed: the site-read
  // snapshot carries the same evidence-gated mapping, just with no
  // server-detected open questions.
  const reviewProposal = useMemo(() => {
    if (proposal.data) {
      return proposal.data;
    }
    if (read && (read.status === "ready" || read.status === "partial")) {
      return proposalFromRead(read);
    }
    return null;
  }, [proposal.data, read]);

  // The runtime bar keeps the live read total unless a later chat reply saw
  // more calls — a reply is a point-in-time copy, never a freeze.
  let replyRuntime: AiRunSummary | undefined;
  for (const entry of conversation.entries) {
    if (entry.role === "assistant") {
      replyRuntime = entry.reply.ai_runtime;
    }
  }
  const readRuntime = read?.ai_runtime;
  const runtime =
    replyRuntime &&
    (!readRuntime || replyRuntime.call_attempts >= readRuntime.call_attempts)
      ? replyRuntime
      : readRuntime;

  const lastEntry = state.thread.at(-1);
  const highlight = useMemo<FindingHighlight | null>(() => {
    if (
      lastEntry?.kind === "narration" &&
      lastEntry.findingIds !== undefined &&
      lastEntry.findingIds.length > 0
    ) {
      return { key: lastEntry.id, ids: lastEntry.findingIds };
    }
    return null;
  }, [lastEntry]);

  // The manual path stays offered before any read and again whenever the
  // machine parked back in co.reading with the run retired — a failed or
  // deferred terminal, including the poll-failure fallback where the last
  // snapshot still claims "reading". A successful terminal never rests
  // there: it proceeds to clarify or review in the same conclusion.
  const showManualChip =
    state.phase === "co.intro" ||
    (state.phase === "co.reading" &&
      state.activeReadId === null &&
      !startRead.isPending);

  return (
    <ConversationWorkbench
      core={corePresence(state, read, startRead.isError)}
      status={read ? t(`ob.readStatus.${read.status}`) : t("ob.ai.ready")}
      runtime={runtime}
      artifact={
        <CompanyActArtifact
          mode={artifactMode}
          manual={state.phase === "co.manual"}
          read={read}
          draft={draft}
          setField={(field, value) =>
            setDraft((current) => changeDraftField(current, field, value))
          }
          onPickEntity={(entity) =>
            setDraft((current) => draftWithLegalEntity(current, entity))
          }
          selectedFactKeys={selectedFactKeys}
          setSelectedFactKeys={setSelectedFactKeys}
          missingRequired={missing}
          highlight={highlight}
          onSwitchMode={setArtifactMode}
          onConfirm={() => confirm.mutate()}
          confirmPending={confirm.isPending}
          confirmDisabled={missing.length > 0}
          saveError={confirm.isError ? confirm.error.message : null}
        />
      }
    >
      <div className="mw-thread">
        <NarrationBubble
          entry={{
            kind: "narration",
            id: "greeting",
            i18nKey: state.memberPath
              ? "ob.conv.welcomeMember"
              : "ob.conv.welcome",
          }}
        />
        <NarrationBubble
          entry={{ kind: "narration", id: "askurl", i18nKey: "ob.conv.askUrl" }}
        />
        {showManualChip && (
          <div className="ob-conv-chips">
            <Button small onClick={() => dispatch({ type: "MANUAL_CHOSEN" })}>
              {t("ob.conv.tellInstead")}
            </Button>
          </div>
        )}
        <ConversationThread
          entries={state.thread}
          pendingQuestionId={state.pendingQuestion?.id ?? null}
          onAnswer={handleAnswer}
        />
        <ConversationEntries
          entries={conversation.entries}
          applied={applied}
          onApply={applyChanges}
          onApplied={(entryID) =>
            setApplied((current) => new Set(current).add(entryID))
          }
        />
        {conversation.send.isPending && (
          <NarrationBubble
            entry={{
              kind: "narration",
              id: "thinking",
              i18nKey: "ob.ai.thinking",
            }}
          />
        )}
        {startRead.isError && (
          <p className="mw-send-error" role="alert">
            {startRead.error.message}
          </p>
        )}
        {conversation.send.isError && (
          <p className="mw-send-error" role="alert">
            {conversation.send.error.message}
          </p>
        )}
        {state.phase === "co.review" && reviewProposal && (
          <CompanyConfirmCard
            proposal={reviewProposal}
            draft={draft}
            answers={answers}
            pendingQuestionId={state.pendingQuestion?.id ?? null}
            selectedFactKeys={selectedFactKeys}
            setSelectedFactKeys={setSelectedFactKeys}
            missingRequired={missing}
            onAnswerClarify={answerClarify}
            onAcceptAll={() => confirm.mutate()}
            pending={confirm.isPending}
            error={confirm.isError ? confirm.error.message : null}
            onEditDirectly={() => setArtifactMode("edit")}
          />
        )}
      </div>
      <div className="mw-composer">
        <textarea
          value={conversation.draft}
          maxLength={2000}
          rows={2}
          placeholder={t("ob.conv.composer")}
          aria-label={t("ob.conv.composer")}
          onChange={(event) => conversation.setDraft(event.target.value)}
          onKeyDown={(event) => {
            if (
              event.key === "Enter" &&
              !event.shiftKey &&
              !event.nativeEvent.isComposing
            ) {
              event.preventDefault();
              submitComposer();
            }
          }}
        />
        <Button
          variant="primary"
          aria-label={t("ob.ai.send")}
          disabled={!conversation.draft.trim() || conversation.send.isPending}
          onClick={submitComposer}
        >
          <Send aria-hidden />
        </Button>
        <small>{t("ob.ai.reviewBoundary")}</small>
      </div>
    </ConversationWorkbench>
  );
}
