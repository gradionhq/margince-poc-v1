import { useEffect, useReducer } from "react";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import { useCompany } from "../onboarding";
import { ActStubs } from "./acts-stub";
import { CompanyAct } from "./company-act";
import {
  conversationReducer,
  initialConversationState,
} from "./conversation-machine";
import { useWizardStatePersist } from "./use-wizard-state";

// The conversational onboarding shell: one pure machine owns where the
// conversation is, and each act renders inside the shared Margince
// workbench. The classic stepper stays the default; this screen mounts only
// behind the flag (see flag.ts).

export { conversationFlagEnabled } from "./flag";

export function OnboardingConversationScreen() {
  const t = useT();
  const [state, dispatch] = useReducer(
    conversationReducer,
    initialConversationState,
  );
  const { persist } = useWizardStatePersist();
  // GET /company 404s until a human saved one — that 404 IS the creator
  // signal; an existing profile means this user joins as a member. Only a
  // SETTLED lookup may route: a transient error must not send an existing
  // member down the creator flow.
  const existing = useCompany(true);
  const memberPath = Boolean(existing.data);

  useEffect(() => {
    if (state.act === "welcome" && existing.isSuccess) {
      dispatch({ type: "START", memberPath });
    }
  }, [state.act, existing.isSuccess, memberPath]);

  if (state.act === "welcome") {
    return (
      <div className="ob-page ob-conv-page">
        {existing.isError ? (
          <div className="readfail warn" role="alert">
            <p>{t("ob.conv.loadFailed")}</p>
            <Button small onClick={() => existing.refetch()}>
              {t("ob.conv.retry")}
            </Button>
          </div>
        ) : (
          <div className="ob-state-loading" role="status">
            <span className="ob-spinner" /> {t("ob.restoring")}
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="ob-page ob-conv-page">
      {state.act === "company" ? (
        <CompanyAct
          state={state}
          dispatch={dispatch}
          profile={existing.data ?? null}
          persist={persist}
        />
      ) : (
        <ActStubs state={state} dispatch={dispatch} persist={persist} />
      )}
    </div>
  );
}
