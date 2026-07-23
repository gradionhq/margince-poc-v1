import type { Dispatch } from "react";
import { useState } from "react";
import { navigate } from "../../app/router";
import { Button } from "../../design-system/atoms";
import type { MarginceCoreState } from "../../design-system/margince-core";
import { useT } from "../../i18n";
import { EMPTY_DRAFT } from "../onboarding";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { exitToClassicOnboarding } from "./flag";
import { ConversationThread } from "./thread";
import type { WizardPersistInput } from "./use-wizard-state";
import { ConversationWorkbench } from "./workbench";

// Honest placeholders for the acts after company: the machine already knows
// them, but their full conversational UI lands in later phases. Until then
// each act says so plainly and offers the classic step, instead of
// pretending a flow that does not exist yet. The controls render INSIDE the
// conversation log so a screen reader hears them with the transcript.

type ActStubsProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  persist: (input: WizardPersistInput) => Promise<boolean>;
}>;

function stubPresence(state: ConversationState): MarginceCoreState {
  return state.act === "done" ? "success" : "listening";
}

export function ActStubs({ state, dispatch, persist }: ActStubsProps) {
  const t = useT();
  return (
    <ConversationWorkbench core={stubPresence(state)} status={t("ob.ai.ready")}>
      <div className="mw-thread">
        <ConversationThread
          entries={state.thread}
          pendingQuestionId={state.pendingQuestion?.id ?? null}
          onAnswer={(questionId, value) =>
            dispatch({ type: "QUESTION_ANSWERED", questionId, value })
          }
        >
          <StubControls state={state} dispatch={dispatch} persist={persist} />
        </ConversationThread>
      </div>
    </ConversationWorkbench>
  );
}

function VoiceInvite({ dispatch }: Pick<ActStubsProps, "dispatch">) {
  const t = useT();
  return (
    <>
      <NarrationBubble
        entry={{
          kind: "narration",
          id: "voice:invite",
          i18nKey: "ob.conv.voice.invite",
        }}
      />
      <div className="ob-conv-chips">
        <Button
          small
          variant="primary"
          onClick={() => dispatch({ type: "VOICE_OPT_IN" })}
        >
          {t("ob.conv.voice.optIn")}
        </Button>
        <Button
          small
          variant="ghost"
          onClick={() => dispatch({ type: "VOICE_SKIPPED" })}
        >
          {t("ob.conv.voice.skipped")}
        </Button>
      </div>
    </>
  );
}

function VoiceCollecting({ dispatch }: Pick<ActStubsProps, "dispatch">) {
  const t = useT();
  return (
    <>
      <NarrationBubble
        entry={{
          kind: "narration",
          id: "voice:stub",
          i18nKey: "ob.conv.voice.stubBody",
        }}
      />
      <div className="ob-conv-chips">
        <Button small onClick={() => exitToClassicOnboarding()}>
          {t("ob.conv.voice.openClassic")}
        </Button>
        <Button
          small
          variant="ghost"
          onClick={() => dispatch({ type: "VOICE_SKIPPED" })}
        >
          {t("ob.conv.voice.skipped")}
        </Button>
      </div>
    </>
  );
}

// Finishing is a server fact before it is a UI fact: the completion
// checkpoint (connect skipped, step complete) must land before the machine
// celebrates; a failed write is said out loud and retryable.
function ConnectConsent({ state, dispatch, persist }: ActStubsProps) {
  const t = useT();
  const [finishing, setFinishing] = useState(false);
  const [finishFailed, setFinishFailed] = useState(false);
  const finish = async () => {
    setFinishing(true);
    setFinishFailed(false);
    const persisted = await persist({
      nextStep: 4,
      values: EMPTY_DRAFT.values,
      connectSkipped: true,
      voiceSkipped: state.lastBuildStatus !== "succeeded",
    });
    setFinishing(false);
    if (persisted) {
      dispatch({ type: "CONNECT_DONE" });
      return;
    }
    setFinishFailed(true);
  };
  return (
    <>
      <NarrationBubble
        entry={{
          kind: "narration",
          id: "connect:consent",
          i18nKey: "ob.conv.consent",
        }}
      />
      <NarrationBubble
        entry={{
          kind: "narration",
          id: "connect:stub",
          i18nKey: "ob.conv.connect.stubBody",
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
      <div className="ob-conv-chips">
        <Button
          small
          onClick={() => exitToClassicOnboarding("#/onboarding/connect")}
        >
          {t("ob.conv.connect.openClassic")}
        </Button>
        <Button small variant="ghost" disabled={finishing} onClick={finish}>
          {t("ob.conv.connect.finish")}
        </Button>
      </div>
    </>
  );
}

function StubControls({ state, dispatch, persist }: ActStubsProps) {
  const t = useT();
  switch (state.phase) {
    case "vo.invite":
      return <VoiceInvite dispatch={dispatch} />;
    case "vo.collecting":
      return <VoiceCollecting dispatch={dispatch} />;
    case "vo.skipped":
    case "vo.result":
    case "re.recap":
      return (
        <div className="ob-conv-chips">
          <Button
            small
            variant="primary"
            onClick={() => dispatch({ type: "RESULTS_CONTINUE" })}
          >
            {t("ob.conv.results.continue")}
          </Button>
        </div>
      );
    case "cn.consent":
      return (
        <ConnectConsent state={state} dispatch={dispatch} persist={persist} />
      );
    case "cn.done":
      return (
        <div className="ob-conv-chips">
          <Button
            small
            variant="primary"
            onClick={() => navigate({ screen: "home" })}
          >
            {t("ob.s4.enterCrm")}
          </Button>
        </div>
      );
    default:
      return null;
  }
}
