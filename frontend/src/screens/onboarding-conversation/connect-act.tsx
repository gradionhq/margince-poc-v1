import type { Dispatch } from "react";
import { useEffect, useState } from "react";
import { navigate } from "../../app/router";
import { useT } from "../../i18n";
import { BackfillPanel } from "../backfill";
import { EMPTY_DRAFT } from "../onboarding";
import { BuildScene } from "../onboarding-build-scene";
import {
  clearOAuthAttempt,
  ImapConnectPanel,
  OAuthConnectPanel,
  type OAuthProvider,
  OAuthReturnPanel,
  peekOAuthAttempt,
} from "../onboarding-connect-panels";
import type { MailProvider } from "./connect-scene";
import { ConnectScene } from "./connect-scene";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { presenceFor } from "./presence";
import { railStops } from "./rail";
import { ConversationThread } from "./thread";
import { useSaveLinkedInAccount } from "./use-linkedin-account";
import type { WizardPersistInput } from "./use-wizard-state";
import { ConversationWorkbench } from "./workbench";

// The connect act: per-purpose consent as a conversation turn, provider
// cards that open their OWN dialog on the artifact surface (never an inline
// panel growing under the card, never a chip in the rail), and the finish
// gate. Finishing is a server fact before it is a UI fact: the completion
// checkpoint (step complete, connect skipped or not) must land before any
// navigation; a failed write is said out loud and retryable.
//
// The four step-level consent guarantees, and each provider's own
// disclosure, live entirely on `ConnectScene` and inside its dialogs now —
// the rail keeps only the two lines that are genuinely its own: what this
// step is for, and that connecting is optional per-provider even though a
// mailbox is required.

type ConnectActProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  persist: (input: WizardPersistInput) => Promise<boolean>;
  /** The OAuth consent return's outcome from the deep-link route. */
  outcome?: string;
  /** The provider that consent returned for, from the same route. */
  returningProvider?: string;
}>;

// `outcome` and `returningProvider` are ROUTE segments — a stale bookmark or
// a reload of an old return URL replays them with no live attempt behind
// them. `peekOAuthAttempt` is the one thing that DOES prove this tab started
// the trip (see onboarding-connect-panels.tsx), so the reader only lands
// back inside the dialog it left from when the mark actually matches; a
// mismatch or an unmarked outcome falls back to the plain inline result
// instead of implying a return this tab never made.
function attemptedProvider(
  outcome: string | undefined,
  returningProvider: string | undefined,
): MailProvider | null {
  if (outcome === undefined) {
    return null;
  }
  const marked = peekOAuthAttempt();
  if (marked === null) {
    return null;
  }
  if (returningProvider !== undefined && returningProvider !== marked) {
    return null;
  }
  const byMarked: Record<OAuthProvider, MailProvider> = {
    gmail: "google",
    graph: "microsoft",
  };
  return byMarked[marked];
}

export function ConnectAct({
  state,
  dispatch,
  persist,
  outcome,
  returningProvider,
}: ConnectActProps) {
  const t = useT();
  // Which dialog is open. On an ordinary visit this starts null; on the real
  // page load a redirect lands on, it opens straight to the provider this
  // tab's own attempt marks — the reader lands back inside the same chrome
  // they left, rather than a bare surface with the result buried inline.
  const [provider, setProvider] = useState<MailProvider | null>(() =>
    attemptedProvider(outcome, returningProvider),
  );
  // Whether the OPEN dialog is showing that reopened result rather than a
  // fresh ask — cleared the moment the reader closes it or picks a
  // different provider, so a retry after a denied/failed return is a real
  // ask again, not a replay of the same result forever.
  const [resultFor, setResultFor] = useState<MailProvider | null>(() =>
    attemptedProvider(outcome, returningProvider),
  );
  const [finishing, setFinishing] = useState(false);
  const [finishFailed, setFinishFailed] = useState(false);
  const [entering, setEntering] = useState(false);
  const linkedin = useSaveLinkedInAccount();

  // Spends the mark once this mount has read it, so reloading the same
  // return URL finds nothing and correctly falls back to the plain inline
  // result instead of reopening the dialog a second time.
  useEffect(() => {
    if (outcome !== undefined) {
      clearOAuthAttempt();
    }
  }, [outcome]);

  const openAsk = (key: MailProvider) => {
    setResultFor(null);
    setProvider(key);
  };
  const closeDialog = () => {
    setProvider(null);
    setResultFor(null);
  };

  // The act advances LinkedIn only once the answer is STORED. Dispatching
  // first and saving in the background would let a member finish onboarding
  // believing they had connected, with nothing recorded and nothing to
  // correct. Unlike mail, resolving LinkedIn never touches `finish` — it is
  // a card on the same screen, not a gate on it.
  const connectLinkedin = (profileUrl: string) => {
    linkedin.mutate(
      { profileUrl, connected: true },
      {
        onSuccess: () =>
          dispatch({ type: "LINKEDIN_CONNECTED", profile: profileUrl }),
      },
    );
  };

  const finish = async (skipped: boolean) => {
    setFinishing(true);
    setFinishFailed(false);
    // Step "complete" (classic STEPS index 4). Voice flags are NOT sent:
    // the merge keeps whatever the voice act (or an earlier session)
    // recorded, so finishing can never overwrite a built voice as skipped.
    const persisted = await persist({
      nextStep: 4,
      values: EMPTY_DRAFT.values,
      connectSkipped: skipped,
    });
    setFinishing(false);
    if (!persisted) {
      setFinishFailed(true);
      return;
    }
    dispatch({ type: "CONNECT_DONE" });
    // Completion is recorded, so the handoff can take its beat: the build scene
    // navigates when it is done. It resolves immediately under reduced motion,
    // so nobody is held behind an animation they asked not to see.
    setEntering(true);
  };

  if (entering) {
    return <BuildScene onDone={() => navigate({ screen: "home" })} />;
  }

  // Where the journey stands, in the rail's own counting.
  const stops = railStops(state.memberPath);
  const eyebrow = t("ob.conv.scene.step", {
    n: stops.findIndex((stop) => stop.key === "connect") + 1,
    m: stops.length,
    label: t("ob.rail.connect"),
  });
  // The connector the backfill window applies to. Only the two the deep link
  // can return for are named; anything else leaves the window unoffered
  // rather than guessing which mailbox was connected.
  const backfillProvider =
    returningProvider === "gmail" || returningProvider === "graph"
      ? returningProvider
      : null;

  return (
    <ConversationWorkbench
      core={presenceFor(state).core}
      railState={state}
      status={t("ob.ai.ready")}
      artifact={
        <ConnectScene
          eyebrow={eyebrow}
          provider={provider}
          onPick={openAsk}
          onDialogClose={closeDialog}
          // True only for the ONE dialog this mount reopened from a proven
          // attempt (see `attemptedProvider`): its content is the settled
          // result, not a fresh ask, so the dialog's own chrome must say so.
          dialogShowsResult={provider !== null && provider === resultFor}
          onSkip={() => void finish(true)}
          skipDisabled={finishing}
          // Once consent has returned, "skip connecting" is no longer a true
          // option — a mailbox is connected (or its confirmation failed and a
          // provider card is the retry), and recording the step as skipped
          // would persist a fact that is not so.
          showSkip={outcome !== "ok"}
          linkedinStatus={state.linkedinStatus}
          onLinkedinConnect={connectLinkedin}
          onLinkedinSkip={() => dispatch({ type: "LINKEDIN_SKIPPED" })}
          linkedinPending={linkedin.isPending}
          linkedinError={linkedin.isError ? linkedin.error.message : null}
          onEnter={
            state.phase === "cn.done" ? () => setEntering(true) : undefined
          }
          // The ask, still open: rendered INSIDE the dialog ConnectScene
          // wraps around `provider`. A real OAuth "allow" leaves the page
          // entirely (`location.assign`), so the dialog does not try to
          // stay open across that redirect — it simply closes on `onDismiss`
          // if the reader backs out first.
          dialogPanel={
            <>
              {provider === "google" && (
                <OAuthConnectPanel provider="gmail" onDismiss={closeDialog} />
              )}
              {provider === "microsoft" && (
                <OAuthConnectPanel provider="graph" onDismiss={closeDialog} />
              )}
              {provider === "imap" && (
                <ImapConnectPanel onComplete={finish} onDismiss={closeDialog} />
              )}
            </>
          }
          // The ask is settled: a redirect already returned. This is a
          // finding plus one more real decision (the backfill window), not a
          // fresh consent round — `ConnectScene` shows it in the dialog the
          // reader left from when a proven attempt says so, and inline on
          // the surface otherwise (a stale or bookmarked return link, which
          // is real information but not a return this tab can vouch for).
          returnPanel={
            outcome !== undefined ? (
              <>
                <OAuthReturnPanel
                  outcome={outcome}
                  provider={returningProvider}
                  onComplete={finish}
                />
                {/* How far back the first import reaches, on the real
                    contract (3m/6m/12m) rather than a decorative dial —
                    the same panel the connectors screen uses. */}
                {outcome === "ok" && backfillProvider !== null && (
                  <BackfillPanel provider={backfillProvider} />
                )}
              </>
            ) : null
          }
        />
      }
    >
      <div className="mw-thread">
        <ConversationThread
          entries={state.thread}
          pendingQuestionId={state.pendingQuestion?.id ?? null}
          onAnswer={(questionId, value) =>
            dispatch({ type: "QUESTION_ANSWERED", questionId, value })
          }
        >
          {state.phase === "cn.consent" && (
            <>
              <NarrationBubble
                entry={{
                  kind: "narration",
                  id: "connect:consent",
                  i18nKey: "ob.conv.consent",
                }}
              />
              {/* The substance of what connecting means lives on the
                  artifact surface now (the guarantees grid, each provider's
                  own disclosure inside its dialog) — the rail keeps only
                  this one honest sentence about the whole step. */}
              <NarrationBubble
                entry={{
                  kind: "narration",
                  id: "connect:promise",
                  i18nKey: "ob.conv.connect.railPromise",
                }}
              />
              <NarrationBubble
                entry={{
                  kind: "narration",
                  id: "connect:pick",
                  i18nKey: "ob.conv.connect.pick",
                }}
              />
              {finishFailed && (
                <div role="alert">
                  <NarrationBubble
                    entry={{
                      kind: "narration",
                      id: "connect:persist-failed",
                      i18nKey: "ob.conv.connect.persistFailed",
                    }}
                  />
                </div>
              )}
            </>
          )}
        </ConversationThread>
      </div>
    </ConversationWorkbench>
  );
}
