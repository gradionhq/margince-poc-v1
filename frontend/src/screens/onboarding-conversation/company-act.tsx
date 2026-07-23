import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Send } from "lucide-react";
import type { Dispatch, SetStateAction } from "react";
import { useCallback, useMemo, useRef, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { useLocale, useT } from "../../i18n";
import { problemMessage } from "../common";
import type { CompanyDraft } from "../onboarding";
import {
  changeDraftField,
  EMPTY_DRAFT,
  formFromProfile,
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
import { presenceFor } from "./presence";
import { ConversationThread } from "./thread";
import { useClarifyAnswers } from "./use-clarify-answers";
import { useCompanyRead } from "./use-company-read";
import type { WizardPersistInput } from "./use-wizard-state";
import { ConversationWorkbench } from "./workbench";

// The company act driver: the read lifecycle lives in useCompanyRead and
// clarify authorization in useClarifyAnswers; this component owns the draft,
// the free-text chat, and the one explicit confirmation — all expressed as
// machine events, so the pure reducer stays the single truth about where the
// conversation is.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type CompanyProfile = components["schemas"]["CompanyProfile"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type AiRunSummary = components["schemas"]["AiRunSummary"];

type CompanyActProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  /** The member path's existing company; the draft seeds from it so a
   * confirmation can never erase stored fields the read did not rediscover. */
  profile: CompanyProfile | null;
  persist: (input: WizardPersistInput) => Promise<boolean>;
}>;

function initialDraft(profile: CompanyProfile | null): CompanyDraft {
  return profile
    ? { values: formFromProfile(profile), grounded: {}, edited: new Set() }
    : EMPTY_DRAFT;
}

// biome-ignore lint/complexity/noExcessiveCognitiveComplexity: the act driver is one machine-shaped surface; splitting it further would scatter the event wiring
export function CompanyAct({
  state,
  dispatch,
  profile,
  persist,
}: CompanyActProps) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();

  // Draft state mirrors the classic coordinator: values + grounding +
  // human-edited marks move together, and a ref keeps callbacks current.
  const [draft, setDraftState] = useState<CompanyDraft>(() =>
    initialDraft(profile),
  );
  const draftRef = useRef<CompanyDraft>(draft);
  const setDraft = useCallback((update: SetStateAction<CompanyDraft>) => {
    const next =
      typeof update === "function" ? update(draftRef.current) : update;
    draftRef.current = next;
    setDraftState(next);
  }, []);

  const [selectedFactKeys, setSelectedFactKeys] = useState<string[]>([]);
  const [artifactMode, setArtifactMode] = useState<ArtifactMode>("dossier");
  const [applied, setApplied] = useState<ReadonlySet<string>>(new Set());
  const [proposalJoin, setProposalJoin] = useState<
    "pending" | "ready" | "failed"
  >("pending");
  const machine = useRef(state);
  machine.current = state;

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
  const conversationRef = useRef(conversation);
  conversationRef.current = conversation;

  const proposalRef = useRef<Proposal | undefined>(undefined);
  const clarify = useClarifyAnswers({
    locale,
    proposalRef,
    draftRef,
    history: () => conversationHistory(conversationRef.current.entries),
    applyChanges,
  });

  // The proposal endpoint joins through persisted wizard state, so the
  // running read is recorded the moment it starts — and the proposal fetch
  // waits for that write (a stale join would serve the previous read).
  const onReadStarted = useCallback(
    (started: CompanySiteRead) => {
      setProposalJoin("pending");
      void persist({
        nextStep: 0,
        mode: "website",
        readId: started.id,
        values: draftRef.current.values,
      }).then((ok) => setProposalJoin(ok ? "ready" : "failed"));
    },
    [persist],
  );

  const { startRead, siteRead, proposal, prevSnapshot } = useCompanyRead({
    dispatch,
    machine,
    setDraft,
    setSelectedFactKeys,
    answers: clarify.answers,
    onReadStarted,
    proposalJoin,
  });
  proposalRef.current = proposal.data;

  const handleAnswer = useCallback(
    (questionId: string, value: string) => {
      dispatch({ type: "QUESTION_ANSWERED", questionId, value });
      clarify.answerClarify(questionId, value);
    },
    [dispatch, clarify.answerClarify],
  );

  const confirm = useMutation({
    mutationFn: async (): Promise<CompanyProfile> => {
      const values = draftRef.current.values;
      const profileInput = {
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
                profile: profileInput,
                selected_fact_keys: selectedFactKeys,
                resolutions: resolutionsFromAnswers(
                  read.comparisons,
                  clarify.answers,
                ),
              },
            })
          : await api.PUT("/company", { body: profileInput });
      const { data, error } = result;
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (profileData) => {
      // The shell's onboarding gate reads the same ["company"] cache entry.
      queryClient.setQueryData(["company"], profileData);
      // Checkpoint the confirmed company so the classic coordinator resumes
      // at the right step and role if the user switches shells.
      void persist({
        nextStep: machine.current.memberPath ? 3 : 1,
        mode: prevSnapshot.current !== null ? "website" : "manual",
        readId: prevSnapshot.current?.id ?? null,
        values: draftRef.current.values,
        factKeys: selectedFactKeys,
      });
      dispatch({ type: "COMPANY_CONFIRMED" });
    },
  });

  const composer = useRef<HTMLTextAreaElement>(null);
  const submitComposer = () => {
    const text = conversation.draft.trim();
    if (text === "" || startRead.isPending || conversation.send.isPending) {
      return;
    }
    // Sending must not strand focus on the send button: the composer stays
    // the keyboard home while the conversation continues.
    composer.current?.focus();
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
  const readBroken = startRead.isError || siteRead.isError;

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
  // machine parked back in co.reading with the run retired (failed,
  // deferred, or the poll-failure fallback) — never while a POST is in
  // flight, so choosing manual cannot race a read that is about to start.
  const showManualChip =
    !startRead.isPending &&
    (state.phase === "co.intro" ||
      (state.phase === "co.reading" && state.activeReadId === null));

  const presence = presenceFor(state, { read, readBroken });

  return (
    <ConversationWorkbench
      core={presence.core}
      progress={presence.progress}
      status={
        readBroken
          ? t("ob.readStatus.failed")
          : read
            ? t(`ob.readStatus.${read.status}`)
            : t("ob.ai.ready")
      }
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
          confirmDisabled={
            missing.length > 0 ||
            !(state.phase === "co.review" || state.phase === "co.manual")
          }
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
        >
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
          {clarify.failure && (
            <div role="alert">
              <NarrationBubble
                entry={
                  clarify.failure.kind === "request"
                    ? {
                        kind: "narration",
                        id: "clarify:apply-failed",
                        i18nKey: "ob.conv.clarify.applyFailed",
                        params: { detail: clarify.failure.detail },
                      }
                    : {
                        kind: "narration",
                        id: "clarify:apply-missing",
                        i18nKey: "ob.conv.clarify.applyMissing",
                      }
                }
              />
            </div>
          )}
          {state.phase === "co.review" && reviewProposal && (
            <CompanyConfirmCard
              proposal={reviewProposal}
              draft={draft}
              answers={clarify.answers}
              pendingQuestionId={state.pendingQuestion?.id ?? null}
              selectedFactKeys={selectedFactKeys}
              setSelectedFactKeys={setSelectedFactKeys}
              missingRequired={missing}
              onAnswerClarify={clarify.answerClarify}
              onAcceptAll={() => confirm.mutate()}
              pending={confirm.isPending}
              authorizing={clarify.authorizing}
              error={confirm.isError ? confirm.error.message : null}
              onEditDirectly={() => setArtifactMode("edit")}
            />
          )}
        </ConversationThread>
      </div>
      <div className="mw-composer">
        <textarea
          ref={composer}
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
