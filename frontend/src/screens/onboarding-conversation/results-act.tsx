import { useQuery } from "@tanstack/react-query";
import type { Dispatch } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { useNow } from "../../format/now";
import { useLocale, useT } from "../../i18n";
import { problemMessage } from "../common";
import type { PayoffCounts } from "../onboarding-payoff";
import { PayoffMessage } from "../onboarding-payoff";
import { ResultsStep } from "../onboarding-results";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { loadWizardState } from "./index";
import { presenceFor } from "./presence";
import { ConversationThread } from "./thread";
import { ConversationWorkbench } from "./workbench";

// The results act: an honest recap of what the funnel actually did. The
// in-thread turns and the artifact recap card both derive from the same two
// server facts (a saved company profile, a built voice version) — a skipped
// voice step is named a starter voice, an unconfirmed profile is named
// unsaved, never claimed as captured.

type CompanyProfile = components["schemas"]["CompanyProfile"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];

type ResultsActProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  profile: CompanyProfile | null;
  voiceBuilt: boolean;
  /** Server corpus words; null when the voice step was skipped or never built. */
  corpusWords: number | null;
}>;

// Everything the payoff needs from the server: the counts, and when this setup
// began. The start instant decides which lead the payoff has earned — setup is
// resumable, so "minutes ago" is a claim that has to be checked rather than
// assumed.
type PayoffFacts = Readonly<{
  counts: PayoffCounts;
  startedAt: string | null;
}>;

// What the setup actually produced, counted from the server's own records.
//
// The confirmed profile is the authority for what was kept — its `fields` and
// `facts` are what a later screen will show — while the site read is the only
// record of the work that produced them (pages crawled, people proposed). Any
// count whose source is missing stays null, and the grid omits that cell rather
// than printing a zero the reader did not earn.
function usePayoffFacts(
  profile: CompanyProfile | null,
  corpusWords: number | null,
): PayoffFacts {
  const wizard = useQuery({
    queryKey: ["onboarding-conv-state"],
    queryFn: loadWizardState,
  });
  const readId = wizard.data?.site_read_id ?? null;
  const read = useQuery({
    queryKey: ["onboarding-conv-read", readId],
    enabled: readId !== null,
    queryFn: async (): Promise<CompanySiteRead | null> => {
      const { data, error, response } = await api.GET(
        "/company/site-reads/{readId}",
        { params: { path: { readId: readId ?? "" } } },
      );
      if (error) {
        // A read the server no longer serves costs the reader two cells of a
        // recap, not the recap: the counts go absent, nothing throws.
        if (response.status === 404) {
          return null;
        }
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const dossier = read.data ?? null;
  return {
    counts: {
      factsRead: dossier?.facts.length ?? null,
      factsConfirmed:
        profile?.facts?.length ??
        wizard.data?.selected_fact_keys.length ??
        null,
      peopleFound: dossier?.people.length ?? null,
      profileFields: profile?.fields?.length ?? null,
      pagesRead: dossier?.pages_read ?? null,
      voiceWords: corpusWords,
    },
    startedAt: wizard.data?.created_at ?? null,
  };
}

export function ResultsAct({
  state,
  dispatch,
  profile,
  voiceBuilt,
  corpusWords,
}: ResultsActProps) {
  const t = useT();
  const { locale } = useLocale();
  const { counts, startedAt } = usePayoffFacts(profile, corpusWords);
  // Pinned at mount (a zero interval takes no ticks): the lead is one sentence
  // the reader is in the middle of, and a sentence that rewrites itself while
  // they read it is worse than one that ages by a minute.
  const nowMs = useNow(0);
  return (
    <ConversationWorkbench
      core={presenceFor(state).core}
      railState={state}
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
          {/* The payoff carries its own primary action, so the bare Continue
              chip would be a second button for the same step. */}
          <PayoffMessage
            counts={counts}
            locale={locale}
            startedAt={startedAt}
            nowMs={nowMs}
            onContinue={() => dispatch({ type: "RESULTS_CONTINUE" })}
          />
        </ConversationThread>
      </div>
    </ConversationWorkbench>
  );
}
