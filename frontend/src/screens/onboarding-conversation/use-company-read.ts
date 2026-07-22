import { useMutation, useQuery } from "@tanstack/react-query";
import type { Dispatch, SetStateAction } from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { problemMessage } from "../common";
import type { CompanyDraft } from "../onboarding";
import { MAX_SELECTED_FACTS, prefill } from "../onboarding";
import type { ClarifyAnswer } from "./company-proposal";
import { toMachineQuestion } from "./company-proposal";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { diffSiteRead, useNarrationQueue } from "./narration";

// The read lifecycle of the company act as one hook: start the read, poll
// it, narrate poll deltas through the paced queue, prefill the draft per
// dossier version, and conclude — clarify question first (while the run is
// still active), then the terminal outcome, then review when nothing is
// open. Everything the conversation shows goes through machine events.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];

type ReadTerminal = Readonly<{
  readId: string;
  status: "ready" | "partial";
  findings: number;
}>;

type UseCompanyReadArgs = Readonly<{
  dispatch: Dispatch<ConversationEvent>;
  /** Live view of the machine, for the deferred-resume re-arm. */
  machine: Readonly<{ current: ConversationState }>;
  setDraft: (update: SetStateAction<CompanyDraft>) => void;
  setSelectedFactKeys: (keys: string[]) => void;
  answers: readonly ClarifyAnswer[];
}>;

export function useCompanyRead({
  dispatch,
  machine,
  setDraft,
  setSelectedFactKeys,
  answers,
}: UseCompanyReadArgs) {
  const [readId, setReadId] = useState<string | null>(null);
  const [proposalArmed, setProposalArmed] = useState(false);
  const prevSnapshot = useRef<CompanySiteRead | null>(null);
  const appliedReadVersion = useRef(0);
  const pendingTerminal = useRef<ReadTerminal | null>(null);
  const askedClarifies = useRef<Set<string>>(new Set());

  const queue = useNarrationQueue({
    onEmit: (event) => {
      // diffSiteRead scopes every event id to its run (`<readId>:...`), so
      // the machine's correlation guard drops a superseded run's leftovers
      // even when they emit after a new read began.
      const { kind: _kind, id, ...say } = event;
      const [runId] = id.split(":");
      dispatch({
        type: "NARRATION",
        readId: runId,
        entry: { kind: "narration", id, ...say },
      });
    },
  });

  // A fresh terminal either concludes immediately (failed, deferred) or
  // waits for the proposal so a clarify question can precede the outcome.
  const concludeFreshTerminal = useCallback(
    (next: CompanySiteRead) => {
      const findings = next.profile_fields.length;
      if (next.status === "ready" || next.status === "partial") {
        pendingTerminal.current = {
          readId: next.id,
          status: next.status,
          findings,
        };
        setProposalArmed(true);
        return;
      }
      if (next.status === "failed" || next.status === "deferred") {
        dispatch({
          type: "READ_TERMINAL",
          readId: next.id,
          status: next.status,
          findings,
        });
      }
    },
    [dispatch],
  );

  const handleSnapshot = useCallback(
    (next: CompanySiteRead) => {
      if (prevSnapshot.current === next) {
        return;
      }
      // A deferred read the server resumed on its own re-arms the retired
      // run before its fresh progress narrates.
      if (
        (next.status === "queued" || next.status === "reading") &&
        machine.current.activeReadId !== next.id
      ) {
        dispatch({ type: "READ_STARTED", readId: next.id });
      }
      const events = diffSiteRead(prevSnapshot.current, next);
      const freshTerminal = events.some((event) => event.kind === "flush");
      prevSnapshot.current = next;
      if (next.draft_version > appliedReadVersion.current) {
        appliedReadVersion.current = next.draft_version;
        setDraft((current) => prefill(current, next.profile_fields));
        setSelectedFactKeys(
          [...new Set(next.facts.map((fact) => fact.value_key))].slice(
            0,
            MAX_SELECTED_FACTS,
          ),
        );
      }
      // Progress first, outcome second: the flush inside a terminal diff
      // drains every queued bubble before any terminal event is dispatched.
      queue.push(events);
      if (freshTerminal) {
        concludeFreshTerminal(next);
      }
    },
    [
      concludeFreshTerminal,
      dispatch,
      machine,
      queue,
      setDraft,
      setSelectedFactKeys,
    ],
  );

  const startRead = useMutation({
    mutationFn: async (url: string): Promise<CompanySiteRead> => {
      const { data, error } = await api.POST("/company/site-reads", {
        params: { header: { "Idempotency-Key": crypto.randomUUID() } },
        body: { url },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      setReadId(data.id);
      // draft_version counts within ONE dossier; a new read starts over.
      appliedReadVersion.current = 0;
      prevSnapshot.current = null;
      pendingTerminal.current = null;
      setProposalArmed(false);
      dispatch({ type: "READ_STARTED", readId: data.id });
      dispatch({
        type: "NARRATION",
        readId: data.id,
        entry: {
          kind: "narration",
          id: `${data.id}:started`,
          i18nKey: "ob.conv.read.started",
          params: { host: new URL(data.root_url).hostname },
        },
      });
      handleSnapshot(data);
    },
  });

  const siteRead = useQuery({
    queryKey: ["company-site-read", readId],
    enabled: readId !== null,
    queryFn: async (): Promise<CompanySiteRead> => {
      const { data, error } = await api.GET("/company/site-reads/{readId}", {
        params: { path: { readId: readId ?? "" } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      if (status === "queued" || status === "reading") {
        return 800;
      }
      return status === "deferred" ? 60_000 : false;
    },
  });

  useEffect(() => {
    if (siteRead.data) {
      handleSnapshot(siteRead.data);
    }
  }, [siteRead.data, handleSnapshot]);

  const proposal = useQuery({
    queryKey: ["onboarding-company-proposal", readId],
    enabled: proposalArmed,
    queryFn: async (): Promise<Proposal> => {
      const { data, error } = await api.GET("/onboarding/company/proposal");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  // Concluding a successful read waits for the proposal: the first
  // server-detected open question must be asked BEFORE the terminal — the
  // machine retires the run at the terminal, and a post-terminal CLARIFY is
  // stale by its correlation guard. Then the outcome lands (readCompleted
  // records it, so the eventual answer proceeds straight to review), and
  // with no questions left the review opens straight away.
  useEffect(() => {
    const data = proposal.data;
    const terminal = pendingTerminal.current;
    if (!data || !terminal) {
      return;
    }
    pendingTerminal.current = null;
    const open = (data.open_questions ?? []).filter(
      (question) => !answers.some((answer) => answer.clarifyId === question.id),
    );
    const first = open[0];
    if (first && !askedClarifies.current.has(first.id)) {
      askedClarifies.current.add(first.id);
      dispatch({
        type: "CLARIFY",
        readId: terminal.readId,
        question: toMachineQuestion(first),
      });
    }
    dispatch({ type: "READ_TERMINAL", ...terminal });
    if (open.length === 0) {
      dispatch({ type: "REVIEW_READY" });
    }
  }, [proposal.data, answers, dispatch]);

  return { startRead, siteRead, proposal, prevSnapshot };
}
