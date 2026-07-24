import { Check } from "lucide-react";
import type { Dispatch } from "react";
import { useState } from "react";
import { navigate } from "../../app/router";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import type { MessageKey } from "../../i18n/en";
import { EMPTY_DRAFT } from "../onboarding";
import {
  ImapConnectPanel,
  OAuthConnectPanel,
  OAuthReturnPanel,
} from "../onboarding-connect-panels";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { presenceFor } from "./presence";
import { ConversationThread } from "./thread";
import type { WizardPersistInput } from "./use-wizard-state";
import { ConversationWorkbench } from "./workbench";

// The connect act: per-purpose consent as a conversation turn, provider
// chips that open the EXISTING connect panels in the artifact panel, and the
// finish gate. Finishing is a server fact before it is a UI fact: the
// completion checkpoint (step complete, connect skipped or not) must land
// before any navigation; a failed write is said out loud and retryable.

type Provider = "google" | "microsoft" | "imap";

const providerLabels: Record<Provider, MessageKey> = {
  google: "ob.s4.provGoogle",
  microsoft: "ob.s4.provMicrosoft",
  imap: "ob.s4.provImap",
};

const scopes: { lead: MessageKey; rest: MessageKey }[] = [
  { lead: "ob.s4.scope1Lead", rest: "ob.s4.scope1Rest" },
  { lead: "ob.s4.scope2Lead", rest: "ob.s4.scope2Rest" },
  { lead: "ob.s4.scope3Lead", rest: "ob.s4.scope3Rest" },
  { lead: "ob.s4.scope4Lead", rest: "ob.s4.scope4Rest" },
];

type ConnectActProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  persist: (input: WizardPersistInput) => Promise<boolean>;
  /** The OAuth consent return's outcome from the deep-link route. */
  outcome?: string;
}>;

export function ConnectAct({
  state,
  dispatch,
  persist,
  outcome,
}: ConnectActProps) {
  const t = useT();
  // The OAuth return view no longer depends on which chip was open when the
  // consent redirect left this screen — it reads the connector roster fresh,
  // so `provider` only tracks which pre-consent panel is open right now.
  const [provider, setProvider] = useState<Provider | null>(null);
  const [finishing, setFinishing] = useState(false);
  const [finishFailed, setFinishFailed] = useState(false);

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
    navigate({ screen: "home" });
  };

  return (
    <ConversationWorkbench
      core={presenceFor(state).core}
      status={t("ob.ai.ready")}
      artifact={
        <div className="mw-review ob-conv-artifact">
          <div className="mw-review-heading">
            <span>{t("ob.ai.liveArtifact")}</span>
            <h2>{t("ob.conv.connect.artifactTitle")}</h2>
            <p>{t("ob.s4.sub")}</p>
          </div>
          {outcome !== undefined ? (
            <OAuthReturnPanel outcome={outcome} onComplete={finish} />
          ) : (
            <>
              {provider === null && (
                <p className="ob-conv-artifact-empty">
                  {t("ob.conv.connect.artifactEmpty")}
                </p>
              )}
              {provider === "google" && (
                <OAuthConnectPanel provider="gmail" onComplete={finish} />
              )}
              {provider === "microsoft" && (
                <OAuthConnectPanel provider="graph" onComplete={finish} />
              )}
              {provider === "imap" && <ImapConnectPanel onComplete={finish} />}
            </>
          )}
        </div>
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
              <div className="ob-conv-scopes">
                {scopes.map((scope) => (
                  <p key={scope.lead}>
                    <Check aria-hidden /> <b>{t(scope.lead)}</b> {t(scope.rest)}
                  </p>
                ))}
              </div>
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
              <div className="ob-conv-chips">
                {(Object.keys(providerLabels) as Provider[]).map((key) => (
                  <Button
                    key={key}
                    small
                    variant={provider === key ? "primary" : undefined}
                    onClick={() => setProvider(key)}
                  >
                    {t(providerLabels[key])}
                  </Button>
                ))}
                <Button
                  small
                  variant="ghost"
                  disabled={finishing}
                  onClick={() => void finish(true)}
                >
                  {t("ob.conv.connect.skip")}
                </Button>
              </div>
            </>
          )}
          {state.phase === "cn.done" && (
            <div className="ob-conv-chips">
              <Button
                small
                variant="primary"
                onClick={() => navigate({ screen: "home" })}
              >
                {t("ob.s4.enterCrm")}
              </Button>
            </div>
          )}
        </ConversationThread>
      </div>
    </ConversationWorkbench>
  );
}
