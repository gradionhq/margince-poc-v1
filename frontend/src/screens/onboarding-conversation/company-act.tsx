import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { Dispatch, SetStateAction } from "react";
import { useCallback, useMemo, useRef, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { useLocale, useT } from "../../i18n";
import type { MessageKey } from "../../i18n/en";
import { coldFieldLabel, problemMessage, useMe } from "../common";
import type { CompanyDraft } from "../onboarding";
import {
  changeDraftField,
  EMPTY_DRAFT,
  formFromProfile,
  isRequired,
  normalizeUrl,
} from "../onboarding";
import { OnboardingGate } from "../onboarding-gate";
import type { SuggestedCompanyChange } from "../onboarding-read";
import type { ArtifactMode, FindingHighlight } from "./artifact";
import { CompanyActArtifact } from "./artifact";
import {
  draftWithLegalEntity,
  evidencedFields,
  isCompanyField,
  missingRequiredFields,
  proposalFromRead,
  resolutionsFromAnswers,
} from "./company-proposal";
import {
  isWork,
  type ReviewRow,
  reviewFields,
  rowFor,
} from "./company-review-state";
import { CompanyConfirmCard } from "./confirm-card";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { DecisionScene } from "./decision-scene";
import { jumpToFindings, NarrationBubble } from "./entries";
import { gateNoticeFor } from "./gate-notice";
import { NextStepBar } from "./next-step-bar";
import { presenceFor } from "./presence";
import { railStops } from "./rail";
import { ConversationThread, selectionFor } from "./thread";
import { useClarifyAnswers } from "./use-clarify-answers";
import { useCompanyRead } from "./use-company-read";
import type { WizardPersistInput } from "./use-wizard-state";
import { ConversationWorkbench, useConfiguredModel } from "./workbench";

// The company act driver: the read lifecycle lives in useCompanyRead and
// clarify authorization in useClarifyAnswers; this component owns the draft
// and the one explicit confirmation — all expressed as machine events, so
// the pure reducer stays the single truth about where the conversation is.
// The rail takes no free text: a legal-entity pick answers on its own
// DecisionScene, a field is typed on the review surface, and every other
// reply the human can give is one of the rail's own chips or jump links —
// never a composer.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type CompanyProfile = components["schemas"]["CompanyProfile"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type LegalEntity = components["schemas"]["CompanySiteReadLegalEntity"];

type CompanyActProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  /** The member path's existing company; the draft seeds from it so a
   * confirmation can never erase stored fields the read did not rediscover. */
  profile: CompanyProfile | null;
  persist: (input: WizardPersistInput) => Promise<boolean>;
  /** The restored snapshot of the machine's already-active read (reload
   * adoption); null in a live session. */
  adoptedRead?: CompanySiteRead | null;
}>;

function initialDraft(profile: CompanyProfile | null): CompanyDraft {
  return profile
    ? { values: formFromProfile(profile), grounded: {}, edited: new Set() }
    : EMPTY_DRAFT;
}

// The fields one legal-entity pick settles as a block.
const LEGAL_BLOCK: ReadonlySet<string> = new Set([
  "legal_name",
  "registered_address",
  "register_vat",
]);

// biome-ignore lint/complexity/noExcessiveCognitiveComplexity: the act driver is one machine-shaped surface; splitting it further would scatter the event wiring
export function CompanyAct({
  state,
  dispatch,
  profile,
  persist,
  adoptedRead = null,
}: CompanyActProps) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  // The gate greets by name, and uses the whole display_name rather than a
  // first token: name order is not universal, so slicing one off would greet
  // some people by their family name.
  const me = useMe();
  const configuredModel = useConfiguredModel();

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
  // A run the machine already owns at mount was persisted when it started
  // (that is how restore found it), so its wizard-state join is already in
  // place; a fresh session joins when its own read starts.
  const [proposalJoin, setProposalJoin] = useState<
    "pending" | "ready" | "failed"
  >(() => (state.activeReadId !== null ? "ready" : "pending"));
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

  const proposalRef = useRef<Proposal | undefined>(undefined);
  // The read's own candidates, live for the legal-entity fill below — kept
  // current a few lines down, once `read` itself is computed.
  const legalEntitiesRef = useRef<readonly LegalEntity[]>([]);
  const clarify = useClarifyAnswers({
    locale,
    proposalRef,
    draftRef,
    legalEntitiesRef,
    // The rail takes no free text, so there is no chat transcript to send —
    // every clarify authorization is a fresh request on its own terms.
    history: () => [],
    applyChanges,
    // A whole-entity pick, unlike applyChanges: its provenance and its
    // never-overwrite-an-edit guard are draftWithLegalEntity's job, not a
    // loop over field/value pairs.
    applyLegalEntity: (entity) =>
      setDraft((current) => draftWithLegalEntity(current, entity)),
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
    adoptedRead,
  });
  proposalRef.current = proposal.data;

  const handleAnswer = useCallback(
    (questionId: string, value: string) => {
      dispatch({ type: "QUESTION_ANSWERED", questionId, value });
      clarify.answerClarify(questionId, value);
      // One answer per fact: picking a legal entity fills the whole legal
      // block, so any sibling clarify about that block (the multi-address
      // conflict) is already decided — it resolves as keep-current instead
      // of asking the human the same thing in different words.
      const questions = proposalRef.current?.open_questions ?? [];
      const answered = questions.find((question) => question.id === questionId);
      if (answered !== undefined && LEGAL_BLOCK.has(answered.field)) {
        for (const question of questions) {
          if (question.id !== questionId && LEGAL_BLOCK.has(question.field)) {
            clarify.dismissClarify(question.id);
          }
        }
      }
    },
    [dispatch, clarify.answerClarify, clarify.dismissClarify],
  );

  // Humans outrank the reader: dismissing a clarify resolves it locally —
  // the machine's pending question clears through the ordinary answer path
  // and the recorded dismissal stops it counting as an open decision.
  const handleDismiss = useCallback(
    (questionId: string) => {
      dispatch({
        type: "QUESTION_ANSWERED",
        questionId,
        value: "",
        dismissed: true,
      });
      clarify.dismissClarify(questionId);
    },
    [dispatch, clarify.dismissClarify],
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

  // The gate's own field is the ONE place a website address is typed — the
  // rail takes no free text, so there is no second entry point to keep in
  // step with this one. The gate hands back a bare host; canonicalising it
  // here keeps normalizeUrl the single spelling of "what a website address
  // is".
  const startFromGate = useCallback(
    (host: string) => {
      const norm = normalizeUrl(host);
      if (!norm.ok || startRead.isPending) {
        return;
      }
      setDraft((current) => changeDraftField(current, "website", norm.full));
      dispatch({ type: "URL_SUBMITTED", url: norm.full });
      startRead.mutate(norm.full);
    },
    [dispatch, setDraft, startRead],
  );

  const read = siteRead.data ?? startRead.data ?? null;
  legalEntitiesRef.current = read?.legal_entities ?? [];
  const missing = missingRequiredFields(draft.values);
  const readBroken = startRead.isError || siteRead.isError;

  const gateNotice = gateNoticeFor({
    state,
    read,
    startError: startRead.isError ? startRead.error.message : null,
    translate: t,
    failedWithDetail: (detail) => t("ob.gate.startFailed", { detail }),
    pausedWithDetail: (detail) => t("ob.gate.readPaused", { detail }),
  });

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

  // The rail's to-do list is the review board's own outstanding rows, read
  // through the exact same `rowFor`/`isWork` pair confirm-card.tsx's section
  // nav counts with — never a second idea of "still needs you" computed from
  // `missing` alone. `byName` mirrors confirm-card's own construction so a
  // field's state can never differ between the two surfaces.
  const attentionRows = useMemo<readonly ReviewRow[]>(() => {
    if (reviewProposal === null) {
      return [];
    }
    const byName = new Map(
      evidencedFields(reviewProposal.fields)
        .filter((field) => isCompanyField(field.field, draft.values))
        .map((field) => [field.field, field]),
    );
    return reviewFields()
      .map((field) => rowFor(field, draft, byName, t))
      .filter((row) => isWork(row.state));
  }, [reviewProposal, draft, t]);
  // The ONLY rows that stop the human continuing: `confirmCompanySiteRead`
  // 422s exactly when one of REQUIRED_FIELDS is still empty — checked here
  // against `isRequired` itself, the same source the server enforces
  // against, never assumed from `row.state === "required"` alone. Every
  // other outstanding row (optional-empty, weak-confidence) is advisory:
  // worth a look, never an obstacle.
  const blocking = attentionRows.filter(blocksConfirm);
  const advisory = attentionRows.filter((row) => !blocksConfirm(row));

  // The runtime bar keeps the live read total unless a clarify authorization
  // saw more calls — answering a decision is a real model round trip too,
  // and its own reply is a point-in-time copy, never a freeze.
  const readRuntime = read?.ai_runtime;
  const clarifyRuntime = clarify.runtime;
  const runtime =
    clarifyRuntime &&
    (!readRuntime || clarifyRuntime.call_attempts >= readRuntime.call_attempts)
      ? clarifyRuntime
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

  // The pinned next-step line: open decisions first — the pending thread
  // question plus the proposal's still-unanswered ones — then the ready
  // review. The bar renders only while a decision affordance is actually
  // on the page (the live question card, or the review's clarify list), so
  // it can always scroll to what it names.
  const pendingId = state.pendingQuestion?.id ?? null;
  // The review card's own "still open" list, recomputed here from the exact
  // same inputs (the proposal's open questions, the pending id, the recorded
  // answers) it derives its own count from — not a second tally the rail
  // could drift from once an answer lands.
  const openReviewQuestions = (reviewProposal?.open_questions ?? []).filter(
    (question) =>
      question.id !== pendingId &&
      !clarify.answers.some((answer) => answer.clarifyId === question.id),
  );
  const decisionCount =
    (pendingId !== null ? 1 : 0) + openReviewQuestions.length;
  // The rail's own two counts: `blockingCount` is what the narration leads
  // with — the still-open decisions count here too, because the live
  // DecisionScene replaces the review outright while one is pending, the
  // same gate the required trio sits behind. `advisoryCount` never gets
  // billed as an obstacle: the guide says so explicitly rather than folding
  // it into one undifferentiated total (the old bug: a flat count that
  // could not tell the human which items actually stopped them).
  const blockingCount = blocking.length + openReviewQuestions.length;
  const advisoryCount = advisory.length;
  const blockingFindingIds = [
    ...blocking.map((row) => row.field),
    ...openReviewQuestions.map((question) => question.field),
  ];
  const advisoryFindingIds = advisory.map((row) => row.field);
  let nextStep: { label: string; selector: string } | null = null;
  if (
    decisionCount > 0 &&
    (pendingId !== null || state.phase === "co.review")
  ) {
    nextStep = {
      label:
        decisionCount === 1
          ? t("ob.conv.next.decisionOne")
          : t("ob.conv.next.decisionMany", { count: decisionCount }),
      // The live decision is the scene on the surface, no longer a card in
      // the thread; the remaining open ones live in the review card.
      selector: pendingId !== null ? ".ob-decision" : ".ob-conv-confirm",
    };
  } else if (state.phase === "co.review" && reviewProposal !== null) {
    nextStep = {
      label: t("ob.conv.next.review"),
      selector: ".ob-conv-confirm",
    };
  }

  const presence = presenceFor(state, { read, readBroken });

  // The gate and the read theatre are the company act's first face. It is
  // full-screen and deliberately has no thread, no panel and no composer:
  // before there is anything sourced to review, a two-column workbench would be
  // showing the reader an empty dossier and asking them to trust it.
  //
  // ONE return for both faces, because they are one column — the read replaces
  // the question below a Core and a headline that never move. Two returns would
  // put two component types at the same position and remount everything between
  // them; OnboardingGate's GateColumn documents what that costs.
  //
  // The condition is the whole span before there is anything to review, and
  // NOTHING else: an in-flight POST or an unarrived first snapshot are both just
  // "still waiting", so they keep the screen the reader is already on. Deriving
  // it from whether the manual escape is offered — which is suppressed while a
  // start is in flight — is what used to drop the reader onto an empty workbench
  // for the length of one request.
  const beforeReview =
    state.phase === "co.intro" || state.phase === "co.reading";
  const scanning =
    state.phase === "co.reading" && state.activeReadId !== null && read
      ? { read, host: normalizeUrl(read.root_url).host, locale }
      : undefined;
  // A run the machine owns whose first snapshot has not arrived: the Core keeps
  // working and the question stays put rather than the column changing shape
  // twice in half a second.
  const awaitingRead =
    state.phase === "co.reading" && state.activeReadId !== null && !read;

  if (beforeReview) {
    return (
      <OnboardingGate
        name={me.data?.user.display_name}
        running={startRead.isPending || awaitingRead}
        notice={gateNotice}
        configuredModel={configuredModel}
        scan={scanning}
        onSubmit={startFromGate}
        onManual={() => dispatch({ type: "MANUAL_CHOSEN" })}
      />
    );
  }

  // The scene eyebrow: where the journey stands, in the rail's own counting.
  // Both the decision and the review live on the CONFIRM stop.
  const stops = railStops(state.memberPath);
  const stepEyebrow = t("ob.conv.scene.step", {
    n: stops.findIndex((stop) => stop.key === "confirm") + 1,
    m: stops.length,
    label: t("ob.rail.confirm"),
  });

  // ONE scene on the surface at a time — the prototype's rule. A pending
  // decision owns the whole surface; the review owns it after; the thread
  // beside them stays what a rail can carry: narration and history. Every
  // unresolved "question" entry is FILTERED from the rail, not only the one
  // matching the machine's current pendingQuestion — the reducer re-asks a
  // clarify by APPENDING a fresh entry rather than retiring the old one (a
  // background poll can re-issue the same clarify under a new id), so an
  // exact-id filter alone still lets a superseded, never-answered re-ask
  // render as a second, disabled copy of the same candidate list. A
  // resolved entry (answered or dismissed) is history and stays; the moment
  // one settles it returns to the transcript.
  const decision =
    state.phase === "co.clarify" && state.pendingQuestion !== null ? (
      <DecisionScene
        question={state.pendingQuestion}
        onAnswer={handleAnswer}
        onDismiss={handleDismiss}
        // The entity candidates carry their address, registry number and
        // imprint quote on the read; matching by the option's value (the
        // entity name) attaches each card's detail. Any other clarify has
        // none to attach and its cards render as name-only.
        factsOf={(value) => {
          const entity = (read?.legal_entities ?? []).find(
            (candidate) => candidate.name === value,
          );
          return entity === undefined
            ? null
            : {
                meta: entity.registered_address,
                mono: entity.register_number,
                snippet: entity.evidence_snippet,
                source: entity.source_url,
              };
        }}
      />
    ) : null;
  const reviewScene =
    state.phase === "co.review" && reviewProposal ? (
      <div className="ob-scene">
        <p className="ob-scene-eyebrow">{stepEyebrow}</p>
        <CompanyConfirmCard
          proposal={reviewProposal}
          draft={draft}
          answers={clarify.answers}
          read={read}
          selectedFactKeys={selectedFactKeys}
          setSelectedFactKeys={setSelectedFactKeys}
          missingRequired={missing}
          setField={(field, value) =>
            setDraft((current) => changeDraftField(current, field, value))
          }
          onAcceptAll={() => confirm.mutate()}
          pending={confirm.isPending}
          authorizing={clarify.authorizing}
          error={confirm.isError ? confirm.error.message : null}
        />
      </div>
    ) : null;
  const threadEntries = state.thread.filter(
    (entry, index) =>
      entry.kind !== "question" || selectionFor(state.thread, index) !== null,
  );

  return (
    <ConversationWorkbench
      core={presence.core}
      progress={presence.progress}
      railState={state}
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
          review={decision ?? reviewScene}
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
            // The profile can never be confirmed while a question the
            // server still considers open is stranded: `useCompanyRead`
            // re-promotes the next one to the decision surface the moment
            // review has none live, which already swaps this scene back to
            // DecisionScene — this is the belt on top of that suspender, for
            // the one render between a question settling and its successor
            // landing. `openReviewQuestions` is the rail's own attention
            // list, so the reason is never a bare disabled button.
            openReviewQuestions.length > 0 ||
            !(state.phase === "co.review" || state.phase === "co.manual")
          }
          saveError={confirm.isError ? confirm.error.message : null}
        />
      }
    >
      <div className="mw-thread">
        <ConversationThread
          entries={threadEntries}
          pendingQuestionId={state.pendingQuestion?.id ?? null}
          onAnswer={handleAnswer}
          onDismiss={handleDismiss}
          lead={
            // The act's greeting. It belongs to the transcript, so it scrolls
            // with it — the machine simply does not own it, because it is the
            // same line whatever state the reader resumes into. The URL ask
            // that used to sit beside it now lives on the gate, which is the
            // only place it can still be answered.
            <NarrationBubble
              entry={{
                kind: "narration",
                id: "greeting",
                i18nKey: state.memberPath
                  ? "ob.conv.welcomeMember"
                  : "ob.conv.welcome",
              }}
            />
          }
        >
          {/* What I need from you next, said in the chat and linked to the
              surface: the machine only speaks when something is ready or
              needs the human, and then it points. */}
          {decision !== null && state.pendingQuestion !== null && (
            <NarrationBubble
              entry={{
                kind: "narration",
                id: "guide:decision",
                i18nKey: "ob.conv.guide.decision",
                // The guide names WHAT is being decided, not just that a
                // decision exists: the question rides along verbatim.
                params: {
                  question: t(
                    state.pendingQuestion.i18nKey,
                    state.pendingQuestion.params,
                  ),
                },
              }}
            />
          )}
          {/* One line, said only once something is done, ready, or needs the
              human — never the running commentary a free-text chat would
              have produced. `blockingCount` leads because it is the number
              that actually stops confirm; `advisoryCount` is named only when
              nothing blocks, and always as a look, never as an obstacle — a
              clean review says so once BOTH are zero, never a bare empty
              list. */}
          {reviewScene !== null && (
            <NarrationBubble
              entry={
                blockingCount > 0
                  ? {
                      kind: "narration",
                      id: "guide:review-blocked",
                      // German pluralises differently from English (and by a
                      // different rule than "add an s"), so the count picks a
                      // whole key, never a suffix glued onto one string.
                      i18nKey:
                        blockingCount === 1
                          ? "ob.conv.guide.reviewBlocked.one"
                          : "ob.conv.guide.reviewBlocked.other",
                      params: { count: blockingCount },
                      findingIds: blockingFindingIds,
                    }
                  : advisoryCount > 0
                    ? {
                        kind: "narration",
                        id: "guide:review-advisory",
                        i18nKey:
                          advisoryCount === 1
                            ? "ob.conv.guide.reviewAdvisory.one"
                            : "ob.conv.guide.reviewAdvisory.other",
                        params: { count: advisoryCount },
                        findingIds: advisoryFindingIds,
                      }
                    : {
                        kind: "narration",
                        id: "guide:review-clean",
                        i18nKey: "ob.conv.guide.reviewClean",
                      }
              }
            />
          )}
          {reviewScene !== null && (
            <ReviewAttention
              blocking={blocking}
              decisions={openReviewQuestions}
              advisory={advisory}
              t={t}
            />
          )}
          {/* The clarify authorization is a real model round trip; this is
              its "thinking" beat now that no free-text send can produce one. */}
          {clarify.authorizing && (
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
        </ConversationThread>
      </div>
      {nextStep !== null && (
        <NextStepBar
          label={nextStep.label}
          targetSelector={nextStep.selector}
          revision={state.seq}
        />
      )}
    </ConversationWorkbench>
  );
}

// The one predicate for "does this row stop the human continuing" — the
// server's `confirmCompanySiteRead` 422s exactly when one of REQUIRED_FIELDS
// (`isRequired`) is still empty, so that is the whole test. Never widened to
// `row.state === "required"` on its own: `rowFor` happens to assign that
// state for exactly this case today, but the check goes through `isRequired`
// so a future change to that naming cannot silently stop matching what the
// server actually enforces.
function blocksConfirm(row: ReviewRow): boolean {
  return isRequired(row.field) && row.value.trim() === "";
}

type AttentionKind = "blocks" | "decision" | "empty" | "check";

type AttentionItem = Readonly<{
  key: string;
  field: string;
  kind: AttentionKind;
  statusKey: MessageKey;
}>;

function blockingItems(rows: readonly ReviewRow[]): readonly AttentionItem[] {
  return rows.map((row) => ({
    key: `field:${row.field}`,
    field: row.field,
    kind: "blocks",
    statusKey: "ob.conv.guide.attentionStatus.blocks",
  }));
}

function decisionItems(
  decisions: readonly { id: string; field: string }[],
): readonly AttentionItem[] {
  return decisions.map((question) => ({
    key: question.id,
    field: question.field,
    kind: "decision",
    statusKey: "ob.conv.guide.attentionStatus.decision",
  }));
}

// Optional-empty and weak-confidence both recede next to the blocking tier,
// so they share one visible group — but a screen reader still gets the
// finer distinction through each button's own (visually hidden) status word.
function advisoryItems(rows: readonly ReviewRow[]): readonly AttentionItem[] {
  return rows.map((row) => ({
    key: `field:${row.field}`,
    field: row.field,
    kind: row.state === "empty" ? "empty" : "check",
    statusKey:
      row.state === "empty"
        ? "ob.conv.guide.attentionStatus.empty"
        : "ob.conv.guide.attentionStatus.check",
  }));
}

// The review's own outstanding points, as a to-do panel, not a flat list of
// identical rows: a heading announces it as work to do, then one labelled
// group per tier that genuinely differs — what stops confirm, what stops it
// because a decision is pending, and what merely wants a look. `blocking`
// and `advisory` are the SAME two arrays the driver already used to derive
// the narration's counts, so the rail can never claim a field is
// outstanding once the surface has settled it, or call something an
// obstacle it is not. The blocking group is the only one that ever renders
// red, and it simply does not render at all once nothing blocks. Each entry
// reuses the one jump the thread already offers narration — no second
// highlight mechanism.
function ReviewAttention({
  blocking,
  decisions,
  advisory,
  t,
}: Readonly<{
  blocking: readonly ReviewRow[];
  decisions: readonly { id: string; field: string }[];
  advisory: readonly ReviewRow[];
  t: ReturnType<typeof useT>;
}>) {
  if (
    blocking.length === 0 &&
    decisions.length === 0 &&
    advisory.length === 0
  ) {
    return null;
  }
  return (
    <div className="ob-conv-attention">
      <h3 className="ob-conv-attention-heading">
        {t("ob.conv.guide.attentionHeading")}
      </h3>
      {blocking.length > 0 && (
        <AttentionGroup
          groupKey="blocking"
          label={t("ob.conv.guide.attentionGroup.blocking")}
          items={blockingItems(blocking)}
          t={t}
        />
      )}
      {decisions.length > 0 && (
        <AttentionGroup
          groupKey="decisions"
          label={t("ob.conv.guide.attentionGroup.decisions")}
          items={decisionItems(decisions)}
          t={t}
        />
      )}
      {advisory.length > 0 && (
        <AttentionGroup
          groupKey="advisory"
          label={t("ob.conv.guide.attentionGroup.advisory")}
          items={advisoryItems(advisory)}
          t={t}
        />
      )}
    </div>
  );
}

// One tier: a short label naming what the rows below it have in common —
// legible before any single row is read — then the rows themselves, each
// just the field name. The label carries the state, so a row never repeats
// it: the old flat list said "still empty" once per row; this says it once
// per group.
function AttentionGroup({
  groupKey,
  label,
  items,
  t,
}: Readonly<{
  groupKey: string;
  label: string;
  items: readonly AttentionItem[];
  t: ReturnType<typeof useT>;
}>) {
  const labelId = `ob-conv-attention-${groupKey}`;
  return (
    <div className="ob-conv-attention-group">
      <p className="ob-conv-attention-group-label" id={labelId}>
        {label}
      </p>
      <ul className="ob-conv-attention-list" aria-labelledby={labelId}>
        {items.map((item) => (
          <li key={item.key}>
            <AttentionButton
              kind={item.kind}
              field={item.field}
              statusKey={item.statusKey}
              t={t}
            />
          </li>
        ))}
      </ul>
    </div>
  );
}

// One to-do row: the field's own label is the only VISIBLE text — the
// group's own heading already said what tier it is in, so repeating that
// once per row would be the same noise the flat list used to read as. The
// status word still rides along for a screen reader jumping straight to a
// button without passing the group heading first.
function AttentionButton({
  kind,
  field,
  statusKey,
  t,
}: Readonly<{
  kind: AttentionKind;
  field: string;
  statusKey: MessageKey;
  t: ReturnType<typeof useT>;
}>) {
  return (
    <button
      type="button"
      data-kind={kind}
      onClick={() => jumpToFindings([field])}
    >
      <span className="ob-conv-attention-field">
        {coldFieldLabel(field, t)}
      </span>
      <span className="sr-only">{t(statusKey)}</span>
    </button>
  );
}
