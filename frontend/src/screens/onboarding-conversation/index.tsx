import { useEffect, useReducer } from "react";
import { useT } from "../../i18n";
import { useCompany } from "../onboarding";
import { ActStubs } from "./acts-stub";
import { CompanyAct } from "./company-act";
import {
  conversationReducer,
  initialConversationState,
} from "./conversation-machine";

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
  // GET /company 404s until a human saved one — that 404 IS the creator
  // signal; an existing profile means this user joins as a member.
  const existing = useCompany(true);
  const memberPath = Boolean(existing.data);

  useEffect(() => {
    if (state.act === "welcome" && !existing.isPending) {
      dispatch({ type: "START", memberPath });
    }
  }, [state.act, existing.isPending, memberPath]);

  return (
    <div className="ob-page ob-conv-page">
      {state.act === "welcome" ? (
        <div className="ob-state-loading" role="status">
          <span className="ob-spinner" /> {t("ob.restoring")}
        </div>
      ) : state.act === "company" ? (
        <CompanyAct state={state} dispatch={dispatch} />
      ) : (
        <ActStubs state={state} dispatch={dispatch} />
      )}
    </div>
  );
}
