import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { Dispatch, SetStateAction } from "react";
import { useCallback, useMemo, useRef, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { useLocale, useT } from "../../i18n";
import type { MessageKey } from "../../i18n/en";
import {
  coldFieldLabel,
  problemCodeOf,
  problemMessageOf,
  throwProblem,
  useMe,
} from "../common";
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
  legalEntityForOption,
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
import { presenceFor } from "./presence";
import { railStops } from "./rail";
import { ConversationThread, selectionFor } from "./thread";
import { useClarifyAnswers } from "./use-clarify-answers";
import { safeStartError, useCompanyRead } from "./use-company-read";
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

// Which server state a confirm 409 named — see the state declaration in the
// driver for what each one means to the reader.
type ConfirmNotice = "skew" | "notReady" | "checkFailed";

// What a notice offers the reader instead of the Continue button: the one
// look that can turn the state it describes into a different one.
type NoticeRetry = Readonly<{ run: () => void; busy: boolean }>;

// Whether the version pair the next confirm would quote is the one this read
// carries — the whole question a readiness re-check has to answer about the
// proposal half, since the pair is all the confirm takes from it.
//
// The proposal is that pair's source whenever it has one: `ready` is the
// server's own word for "this read has a draft to confirm", and the pair has
// to be THIS read's, not one from before it moved. A proposal endpoint that
// has never answered is the one case that needs no comparison — the confirm
// then quotes the refreshed read's own pair (see the mutation body's
// `proposalFromRead` fallback) — and it is distinguishable from a stale pair
// precisely because no snapshot was ever kept to go stale.
function quotesReadVersion(
  quoted: Proposal | undefined,
  refreshed: CompanySiteRead,
): boolean {
  if (quoted === undefined) {
    return true;
  }
  return (
    quoted.ready &&
    quoted.draft_version === refreshed.draft_version &&
    quoted.proposal_hash === refreshed.proposal_hash
  );
}

// The sentence a notice reads as. A "skew" has two, because the reader's
// next step changes once its refresh has run and the block is still there.
function confirmNoticeKey(
  notice: ConfirmNotice,
  skewStuck: boolean,
): MessageKey {
  if (notice === "notReady") {
    return "ob.conv.review.confirmNotReady";
  }
  if (notice === "checkFailed") {
    return "ob.conv.review.confirmCheckFailed";
  }
  return skewStuck
    ? "ob.conv.review.confirmVersionSkewStuck"
    : "ob.conv.review.confirmVersionSkew";
}

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
  // A confirm 409 that this driver has already resolved into a next step,
  // rather than a bare failure. The server gives each of its three refusals
  // its own code (crm.yaml, confirmCompanySiteRead), so each notice states
  // what the server itself said: "skew" is `version_skew`, the draft changed
  // under the human; "notReady" is `not_confirmable`, this read has no draft
  // to confirm; and "checkFailed" is the one thing left unresolved after an
  // `already_confirmed` — the company that confirmation created could not be
  // loaded, an honest "I could not load it", never a diagnosis of the read
  // stated on the strength of a request that never landed. None is the
  // generic confirmFailed banner — see confirmBannerMessage below — because
  // none is "fix your input and try the same thing again". This is the
  // banner's OWN category and outlives the refetch below: it clears only at
  // the next confirm attempt (onMutate) or at the reader's own check that
  // ends the state it describes, never the moment Continue happens to
  // re-arm, so the reassurance stays on screen until the reader has had a
  // chance to read it.
  const [confirmNotice, setConfirmNotice] = useState<ConfirmNotice | null>(
    null,
  );
  // Whether a "skew" 409 blocks the NEXT press of Continue, settled in one
  // of three outcomes once its refetch (or the reader's own manual retry of
  // one) resolves — (1) a genuinely NEW proposal hash landed: the block
  // lifts, because the next press would send something the server has not
  // seen yet; (2) the refetch succeeded but the hash is UNCHANGED:
  // re-arming would earn the identical 409, so the block stays and
  // `skewStuck` switches the banner to naming that and offering a manual
  // retry; (3) the refetch itself FAILED: same outcome as (2), since there
  // is nothing new to resubmit either way.
  const [skewBlocked, setSkewBlocked] = useState(false);
  const [skewStuck, setSkewStuck] = useState(false);
  // True for as long as this driver's own refetch (automatic or a manual
  // retry of a "skewStuck" notice) is in flight — disables the retry action
  // itself, so a second press cannot start a second refetch on top of one
  // already running.
  const [awaitingProposalRefresh, setAwaitingProposalRefresh] = useState(false);
  // Whether a `not_confirmable` 409 blocks the NEXT press of Continue. It
  // always does when it lands: the server refused this read as having no
  // draft to confirm, and the identical submission earns the identical
  // refusal until the read itself moves. Only a re-check that finds the read
  // confirmable again lifts it — see recheckReadiness.
  const [notReadyBlocked, setNotReadyBlocked] = useState(false);
  // The same as awaitingProposalRefresh, for this driver's two other looks —
  // the readiness re-check and the confirmed-company load. While one is in
  // flight its own retry is disabled, so a second press cannot start a
  // second look on top of the one already running.
  const [awaitingReadinessCheck, setAwaitingReadinessCheck] = useState(false);
  const [awaitingCompanyLoad, setAwaitingCompanyLoad] = useState(false);
  // The proposal hash THIS confirm attempt submitted, captured inside the
  // mutation so the version-skew refresh below can tell "the refetch moved
  // past the rejected draft" from "the refetch landed the exact same one" —
  // the only distinction that decides whether the block lifts.
  const submittedProposalHashRef = useRef<string | undefined>(undefined);
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
    // never-overwrite-an-edit guard belong to the draft helper, not to a
    // loop over field/value pairs. The same helper the dossier's own entity
    // cards call, because it is the same gesture — see draftWithLegalEntity.
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

  // The one refresh a version-skew 409 ever runs, whether it fires
  // automatically (the mutation's own onError) or the reader asks for
  // another look at a "skewStuck" notice — one place, so the three-outcome
  // decision below can never drift between the two callers. The proposal is
  // cached separately from the read snapshot: refetching only the read
  // leaves the stale proposal in place, so both are awaited together.
  const refreshAfterSkew = useCallback(() => {
    setAwaitingProposalRefresh(true);
    const submittedHash = submittedProposalHashRef.current;
    void Promise.allSettled([siteRead.refetch(), proposal.refetch()]).then(
      ([, proposalOutcome]) => {
        setAwaitingProposalRefresh(false);
        const refreshedHash =
          proposalOutcome.status === "fulfilled"
            ? proposalOutcome.value.data?.proposal_hash
            : undefined;
        // Three outcomes, and only the first lifts the block: (1) the
        // refetch succeeded and came back with a DIFFERENT hash — the next
        // press provably sends a draft the server has not rejected; (2) the
        // refetch succeeded but the hash is UNCHANGED — resubmitting would
        // earn the identical 409, so the block stays and `skewStuck` tells
        // the reader so, with a route to ask again; (3) the refetch itself
        // FAILED — same outcome as (2), since there is nothing new to
        // resubmit. The banner's own text (confirmNotice) is untouched
        // either way: it already says "have a look, then press Continue
        // again", which stays true whether that press is available now or
        // needs one more look first.
        const landedNewDraft =
          refreshedHash !== undefined && refreshedHash !== submittedHash;
        setSkewBlocked(!landedNewDraft);
        setSkewStuck(!landedNewDraft);
      },
    );
  }, [siteRead.refetch, proposal.refetch]);

  // The one route off the review, whether this attempt confirmed it or an
  // earlier one already did (the recovered "already confirmed" 409 below
  // reaches this too) — one place, so the checkpoint and the machine
  // transition can never drift between the two callers.
  const finishConfirm = useCallback(
    (profileData: CompanyProfile) => {
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
    [dispatch, persist, prevSnapshot, queryClient, selectedFactKeys],
  );

  // The one route an `already_confirmed` 409 takes, whether it fires from the
  // mutation's own onError or the reader asks to look again — one place, so
  // the outcomes below cannot drift between the two callers.
  //
  // The server has already named THIS read as confirmed, so there is nothing
  // left to diagnose: the only thing standing between the reader and the
  // review's exit is the company that confirmation created. Loading it is
  // therefore the whole recovery — no refetch of the read to re-derive which
  // 409 this was, and no reading of GET /company as proof of anything (the
  // member path can carry a company from before the attempt began; here the
  // server itself already answered that question).
  //
  // The load can fail, and a failed load is its own outcome rather than a
  // fall-through: leaving it silent would put the reader back on a Continue
  // button with nothing said, which is the loop this branch exists to close.
  const loadConfirmedCompany = useCallback(() => {
    setAwaitingCompanyLoad(true);
    const load = async () => {
      const { data } = await api.GET("/company");
      if (data === undefined) {
        setConfirmNotice("checkFailed");
        return;
      }
      finishConfirm(data);
    };
    void load()
      .catch((loadError) => {
        // Whatever actually broke belongs in the console, never in the
        // sentence the human reads — and never nowhere, which is what left
        // Continue re-armed onto the same rejection.
        console.error("confirmed-company load failed", loadError);
        setConfirmNotice("checkFailed");
      })
      .finally(() => setAwaitingCompanyLoad(false));
  }, [finishConfirm]);

  // The one look a `not_confirmable` 409 ever takes, and the ONLY thing that
  // lifts the block it raised. The server refused this read as having no
  // draft to confirm, so nothing about the same submission can end
  // differently until the read itself moves; only a snapshot the server now
  // reports as confirmable proves it has. A refetch that FAILED proves
  // nothing — react-query keeps the last good snapshot, which is the very
  // one the server just refused — so the block stands, exactly as it does
  // for a snapshot that comes back unconfirmable again.
  //
  // Both halves are checked, on their own terms, because the confirm sends
  // both: a read the server calls confirmable AND the version pair that press
  // would quote. A refetch that merely SETTLED proves nothing about the pair —
  // react-query serves its last good proposal through a failure, and a
  // successful one can still answer for a draft the read has since moved past
  // — so the block lifts only once that pair IS the refreshed read's own. Any
  // other release re-arms Continue onto a version_skew earned on the next
  // press rather than avoided.
  const recheckReadiness = useCallback(() => {
    setAwaitingReadinessCheck(true);
    void Promise.allSettled([siteRead.refetch(), proposal.refetch()]).then(
      ([readOutcome, proposalOutcome]) => {
        setAwaitingReadinessCheck(false);
        const refreshed =
          readOutcome.status === "fulfilled" && !readOutcome.value.isError
            ? readOutcome.value.data
            : undefined;
        const released =
          refreshed !== undefined &&
          (refreshed.status === "ready" || refreshed.status === "partial") &&
          proposalOutcome.status === "fulfilled" &&
          quotesReadVersion(proposalOutcome.value.data, refreshed);
        setNotReadyBlocked(!released);
        if (released) {
          // The reader ran this check themselves and it released the block,
          // so the sentence naming that block has stopped being true —
          // leaving it up would be a false statement, not reassurance.
          setConfirmNotice(null);
        }
      },
    );
  }, [proposal.refetch, siteRead.refetch]);

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
      submittedProposalHashRef.current = proposalData?.proposal_hash;
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
        throwProblem(error);
      }
      return data;
    },
    // Every fresh attempt starts clean: a notice from a PREVIOUS 409 must
    // not survive to describe THIS one, whether this click succeeds, fails
    // the same way again, or fails differently. Reachable only once a prior
    // block has actually lifted (Continue is disabled while one stands), so
    // resetting the blocks here is the belt on top of that suspender, not
    // the thing keeping a stale draft from resubmitting.
    onMutate: () => {
      setConfirmNotice(null);
      setSkewBlocked(false);
      setSkewStuck(false);
      setNotReadyBlocked(false);
    },
    onSuccess: finishConfirm,
    // A 409 always leaves the reader with something to do — never a dead
    // Continue button and a raw server string — and the server's own code
    // says which of its three refusals this is, so nothing here has to be
    // re-derived from a second request. `version_skew`: the draft the human
    // reviewed is stale, so the read is re-fetched right away
    // (`useCompanyRead`'s own poll already stopped once the read went
    // terminal, so nothing else will) and the NEXT press of Continue sends
    // whatever that fetch turns up. `already_confirmed`: an earlier
    // confirmation already created the company, so all that is left is to
    // load it. `not_confirmable`: this read has no draft to confirm, which
    // the identical submission cannot change — hence the block, and the
    // re-check that is the only thing able to lift it.
    onError: (error) => {
      const code = problemCodeOf(error);
      if (code === "version_skew") {
        // See refreshAfterSkew for the three outcomes this can settle into.
        setConfirmNotice("skew");
        setSkewBlocked(true);
        refreshAfterSkew();
        return;
      }
      if (code === "already_confirmed") {
        loadConfirmedCompany();
        return;
      }
      if (code === "not_confirmable") {
        setConfirmNotice("notReady");
        setNotReadyBlocked(true);
      }
    },
  });

  // Continue must not resubmit what the server has already refused in a way
  // an identical press cannot change: the proposal it rejected for version
  // skew (see refreshAfterSkew for the three outcomes that decide when
  // `skewBlocked` clears) or a read it says has no draft to confirm (see
  // recheckReadiness). Nor while the `already_confirmed` recovery is still
  // loading the company that confirmation created: the server has settled
  // this read, so a press there earns the same 409 and starts a second
  // lookup racing the first. A SETTLED "checkFailed" deliberately leaves
  // Continue armed: that load may simply have been unlucky, and pressing
  // again is a fair thing to do.
  const confirmBlocked = skewBlocked || notReadyBlocked || awaitingCompanyLoad;

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
    startError: startRead.isError ? safeStartError(startRead.error, t) : null,
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

  // Entries already present at mount are transcript, not news. Freezing their
  // ids on the first render is what stops a leftover crawl line — which names
  // fields, and is very often the last thing said before the review appears —
  // from pulsing and scrolling the board the reader has only just arrived at.
  const mountedEntryIds = useRef<ReadonlySet<string> | null>(null);
  if (mountedEntryIds.current === null) {
    mountedEntryIds.current = new Set(state.thread.map((entry) => entry.id));
  }
  const preRendered = mountedEntryIds.current;
  const lastEntry = state.thread.at(-1);
  // A highlight belongs to the dossier: the review and decision scenes replace
  // that body outright, so one aimed at a scene no longer on screen would land
  // on whatever the new one happens to render.
  const dossierShowing =
    state.phase !== "co.review" && state.phase !== "co.clarify";
  const highlight = useMemo<FindingHighlight | null>(() => {
    if (
      dossierShowing &&
      lastEntry?.kind === "narration" &&
      !preRendered.has(lastEntry.id) &&
      lastEntry.findingIds !== undefined &&
      lastEntry.findingIds.length > 0
    ) {
      return { key: lastEntry.id, ids: lastEntry.findingIds };
    }
    return null;
  }, [lastEntry, dossierShowing, preRendered]);

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
        // imprint quote on the read; resolving the option's value back to its
        // candidate attaches each card's detail. Through the same matcher the
        // pick itself uses, so a card can never show details for a candidate
        // the pick would then fail to find — or show none for one it would.
        // Any other clarify has no candidate to attach and its cards render
        // as name-only.
        factsOf={(value) => {
          const entity = legalEntityForOption(
            read?.legal_entities ?? [],
            value,
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
  // The generic "I could not save that: {detail}" banner, but ONLY for a
  // confirm failure this driver has not already turned into something more
  // useful: `confirmNotice` covers version_skew (a fresh fetch is already in
  // flight; the thread's own alert says so) and the other two documented
  // 409s, so this stays null for those rather than doubling the message.
  // `already_confirmed` is the one refusal that carries no notice while it
  // resolves — its whole recovery is the company lookup — and that lookup
  // ends either in the review's exit or in the "checkFailed" notice, so a
  // failure banner over it would report a save that in fact went through.
  const confirmBannerMessage =
    confirm.isError && confirmNotice === null && !awaitingCompanyLoad
      ? problemMessageOf(confirm.error, t)
      : null;
  // The action each resolved-409 notice offers, or none. A "skew" notice
  // earns one only once its automatic refresh has already run and left the
  // block standing (`skewStuck`) — before that there is nothing left to ask
  // for, and Continue itself is the next step. The other two always do: for
  // "notReady" the re-check is the reader's ONLY route, because Continue is
  // blocked, and for "checkFailed" the load is the only thing that can turn
  // "I could not load it" into the company itself.
  const noticeRetries: Readonly<Record<ConfirmNotice, NoticeRetry | null>> = {
    skew: skewStuck
      ? { run: refreshAfterSkew, busy: awaitingProposalRefresh }
      : null,
    notReady: { run: recheckReadiness, busy: awaitingReadinessCheck },
    checkFailed: { run: loadConfirmedCompany, busy: awaitingCompanyLoad },
  };
  const noticeRetry =
    confirmNotice === null ? null : noticeRetries[confirmNotice];
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
          authorizing={clarify.authorizing || confirmBlocked}
          error={confirmBannerMessage}
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
            !(state.phase === "co.review" || state.phase === "co.manual") ||
            // A submission the server has already refused in a way pressing
            // again cannot change — see confirmBlocked.
            confirmBlocked
          }
          saveError={confirmBannerMessage}
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
              {t("ob.gate.startFailed", {
                detail: safeStartError(startRead.error, t),
              })}
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
          {/* A confirm 409 this driver already turned into a next step
              rather than a bare failure — see the `confirm` mutation's
              onError, refreshAfterSkew, recheckReadiness and
              loadConfirmedCompany for which server state each one names, and
              why none is "fix your input and press the same button again".
              Each carries the look that could end it (see noticeRetry), and
              where Continue is blocked that look is the route forward. */}
          {confirmNotice !== null && (
            <div role="alert">
              <NarrationBubble
                entry={{
                  kind: "narration",
                  id: `confirm:${confirmNotice}${skewStuck ? ":stuck" : ""}`,
                  i18nKey: confirmNoticeKey(confirmNotice, skewStuck),
                }}
              />
              {noticeRetry !== null && (
                <Button
                  small
                  variant="ghost"
                  onClick={noticeRetry.run}
                  disabled={noticeRetry.busy}
                >
                  {t("common.retry")}
                </Button>
              )}
            </div>
          )}
        </ConversationThread>
      </div>
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
