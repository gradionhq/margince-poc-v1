import type { Dispatch } from "react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import { ResultsStep } from "../onboarding-results";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { ConversationThread } from "./thread";
import { ConversationWorkbench } from "./workbench";

// The results act: an honest recap of what the funnel actually did. The
// in-thread turns and the artifact recap card both derive from the same two
// server facts (a saved company profile, a built voice version) — a skipped
// voice step is named a starter voice, an unconfirmed profile is named
// unsaved, never claimed as captured.

type CompanyProfile = components["schemas"]["CompanyProfile"];

type ResultsActProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  profile: CompanyProfile | null;
  voiceBuilt: boolean;
}>;

export function ResultsAct({
  state,
  dispatch,
  profile,
  voiceBuilt,
}: ResultsActProps) {
  const t = useT();
  return (
    <ConversationWorkbench
      core="success"
      status={t("ob.ai.ready")}
      artifact={
        <div className="mw-review ob-conv-artifact">
          <div className="mw-review-heading">
            <span>{t("ob.ai.liveArtifact")}</span>
            <h2>{t("ob.conv.results.artifactTitle")}</h2>
            <p>{t("ob.conv.results.artifactBody")}</p>
          </div>
          <ResultsStep
            voiceBuilt={voiceBuilt}
            profileSaved={profile !== null}
            profile={profile ?? undefined}
          />
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
          <NarrationBubble
            entry={
              profile !== null
                ? {
                    kind: "narration",
                    id: "results:company",
                    i18nKey: "ob.conv.results.company",
                    params: { name: profile.display_name },
                  }
                : {
                    kind: "narration",
                    id: "results:company-unsaved",
                    i18nKey: "ob.conv.results.companyUnsaved",
                  }
            }
          />
          <NarrationBubble
            entry={{
              kind: "narration",
              id: "results:voice",
              i18nKey: voiceBuilt
                ? "ob.conv.results.voiceBuilt"
                : "ob.conv.results.voiceSkipped",
            }}
          />
          <div className="ob-conv-chips">
            <Button
              small
              variant="primary"
              onClick={() => dispatch({ type: "RESULTS_CONTINUE" })}
            >
              {t("ob.conv.results.continue")}
            </Button>
          </div>
        </ConversationThread>
      </div>
    </ConversationWorkbench>
  );
}
