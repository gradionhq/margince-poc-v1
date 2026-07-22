import type { Dispatch } from "react";
import { navigate } from "../../app/router";
import { Button } from "../../design-system/atoms";
import type { MarginceCoreState } from "../../design-system/margince-core";
import { useT } from "../../i18n";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { exitToClassicOnboarding } from "./flag";
import { ConversationThread } from "./thread";
import { ConversationWorkbench } from "./workbench";

// Honest placeholders for the acts after company: the machine already knows
// them, but their full conversational UI lands in later phases. Until then
// each act says so plainly and offers the classic step, instead of
// pretending a flow that does not exist yet.

type ActStubsProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
}>;

function stubPresence(state: ConversationState): MarginceCoreState {
  return state.act === "done" ? "success" : "listening";
}

export function ActStubs({ state, dispatch }: ActStubsProps) {
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
        />
        <StubControls state={state} dispatch={dispatch} />
      </div>
    </ConversationWorkbench>
  );
}

function StubControls({ state, dispatch }: ActStubsProps) {
  const t = useT();
  switch (state.phase) {
    case "vo.invite":
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
    case "vo.collecting":
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
          <div className="ob-conv-chips">
            <Button
              small
              onClick={() => exitToClassicOnboarding("#/onboarding/connect")}
            >
              {t("ob.conv.connect.openClassic")}
            </Button>
            <Button
              small
              variant="ghost"
              onClick={() => dispatch({ type: "CONNECT_DONE" })}
            >
              {t("ob.conv.connect.finish")}
            </Button>
          </div>
        </>
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
