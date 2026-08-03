import { Check } from "lucide-react";
import type { Dispatch } from "react";
import { useState } from "react";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import type { MessageKey } from "../../i18n/en";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { presenceFor } from "./presence";
import { ConversationThread } from "./thread";
import { useSaveLinkedInAccount } from "./use-linkedin-account";
import { ConversationWorkbench } from "./workbench";

// The LinkedIn act, and it comes BEFORE the inbox on purpose.
//
// Mail says who you have already spoken to. A network says who you could
// reach — and on day one, when the CRM knows nobody, that is the only thing
// that separates a cold account from a warm one. Asking for it after the
// inbox would frame it as an extra; asking for it first says what it is.
//
// PLACEHOLDER PROVIDER, but a real answer. The Member Data Portability API
// needs an approved LinkedIn developer app, which we have not been granted yet,
// so `authorize()` stands in for the OAuth redirect. Everything else is live:
// the consent copy and the scopes are what the integration will ask for, and
// the profile and the consent are SAVED — a member can see and correct them in
// Settings afterwards, and a reload does not lose them. The pending app is
// marked in the UI rather than hidden, because a member who thinks their
// network is syncing when it is not would wait forever for contacts that never
// arrive.

// What the live integration will request, named one by one. A member handing
// over their professional network deserves the list before they click, not a
// summary afterwards.
const scopes: { lead: MessageKey; rest: MessageKey }[] = [
  { lead: "ob.conv.linkedin.scope1Lead", rest: "ob.conv.linkedin.scope1Rest" },
  { lead: "ob.conv.linkedin.scope2Lead", rest: "ob.conv.linkedin.scope2Rest" },
  { lead: "ob.conv.linkedin.scope3Lead", rest: "ob.conv.linkedin.scope3Rest" },
  { lead: "ob.conv.linkedin.scope4Lead", rest: "ob.conv.linkedin.scope4Rest" },
];

type LinkedInActProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
}>;

export function LinkedInAct({ state, dispatch }: LinkedInActProps) {
  const t = useT();
  const [profile, setProfile] = useState("");
  const save = useSaveLinkedInAccount();

  // The one field the member supplies. The connection is owned by whoever
  // authorizes it, so their profile is what the imported connections get
  // attributed to — "Anna knows them", never "the company knows them".
  const trimmed = profile.trim();
  const ready = trimmed.length > 0;

  // The act advances only once the answer is STORED. Advancing first and
  // saving in the background would let a member finish onboarding believing
  // they had connected, with nothing recorded and nothing to correct.
  const authorize = () => {
    save.mutate(
      { profileUrl: trimmed, connected: true },
      {
        onSuccess: () =>
          dispatch({ type: "LINKEDIN_CONNECTED", profile: trimmed }),
      },
    );
  };

  return (
    <ConversationWorkbench
      core={presenceFor(state).core}
      status={t("ob.ai.ready")}
      artifact={
        <div className="mw-review ob-conv-artifact">
          <div className="mw-review-heading">
            <span>{t("ob.ai.liveArtifact")}</span>
            <h2>{t("ob.conv.linkedin.artifactTitle")}</h2>
            <p>{t("ob.conv.linkedin.artifactSub")}</p>
          </div>
          <div className="ob-conv-scopes">
            {scopes.map((scope) => (
              <p key={scope.lead}>
                <Check aria-hidden /> <b>{t(scope.lead)}</b> {t(scope.rest)}
              </p>
            ))}
          </div>
          <p className="co-muted">{t("ob.conv.linkedin.neverContacts")}</p>
          <label className="ob-conv-field" htmlFor="linkedin-profile">
            {t("ob.conv.linkedin.profileLabel")}
            <input
              id="linkedin-profile"
              type="url"
              inputMode="url"
              data-testid="linkedin-profile"
              placeholder={t("ob.conv.linkedin.profilePlaceholder")}
              value={profile}
              onChange={(e) => setProfile(e.target.value)}
            />
          </label>
          <p className="co-muted">{t("ob.conv.linkedin.profileWhy")}</p>
          <div className="ob-conv-chips">
            <Button
              variant="primary"
              disabled={!ready || save.isPending}
              onClick={authorize}
            >
              {t("ob.conv.linkedin.authorize")}
            </Button>
          </div>
          {save.isError && (
            <p
              role="alert"
              className="co-error"
              data-testid="linkedin-save-error"
            >
              {save.error.message}
            </p>
          )}
          <p className="co-muted" data-testid="linkedin-pending">
            {t("ob.conv.linkedin.appPending")}
          </p>
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
          {state.phase === "ln.why" && (
            <>
              <NarrationBubble
                entry={{
                  kind: "narration",
                  id: "linkedin:why",
                  i18nKey: "ob.conv.linkedin.why",
                }}
              />
              <NarrationBubble
                entry={{
                  kind: "narration",
                  id: "linkedin:ask",
                  i18nKey: "ob.conv.linkedin.ask",
                }}
              />
              <div className="ob-conv-chips">
                <Button
                  small
                  variant="ghost"
                  onClick={() => dispatch({ type: "LINKEDIN_SKIPPED" })}
                >
                  {t("ob.conv.linkedin.skip")}
                </Button>
              </div>
            </>
          )}
        </ConversationThread>
      </div>
    </ConversationWorkbench>
  );
}
