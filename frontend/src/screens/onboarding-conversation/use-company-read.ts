import { useMutation, useQuery } from "@tanstack/react-query";
import type { Dispatch, SetStateAction } from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { useLocale } from "../../i18n";
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
  /** Fired once per started read, before the first poll concludes anything —
   * the shell persists wizard state here so the proposal join can resolve. */
  onReadStarted: (read: CompanySiteRead) => void;
  /** Whether the wizard-state write joining the CURRENT read landed: the
   * proposal is fetched only when "ready" (a stale join would serve the
   * previous read's proposal) and falls back to the snapshot on "failed". */
  proposalJoin: "pending" | "ready" | "failed";
  /** The restored snapshot of the machine's already-active run: primed as
   * the diff baseline (its findings recap upstream instead of replaying)
   * and, when already terminal, concluded straight into the review path. */
  adoptedRead?: CompanySiteRead | null;
}>;

export function useCompanyRead({
  dispatch,
  machine,
  setDraft,
  setSelectedFactKeys,
  answers,
  onReadStarted,
  proposalJoin,
  adoptedRead = null,
}: UseCompanyReadArgs) {
  // A run the machine already owns at mount (a restore, or this act
  // remounting mid-read) is adopted: polling resumes for the machine's
  // active run instead of stranding it with no poller.
  const [readId, setReadId] = useState<string | null>(
    machine.current.activeReadId,
  );
  // Mirrors the readId state for callbacks: the poll effect must ignore
  // snapshots of a run this hook no longer intends (a superseded URL).
  const readIdRef = useRef<string | null>(machine.current.activeReadId);
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
      // A snapshot from a run this hook no longer wants (an in-flight poll
      // of a URL the user replaced) must not narrate, prefill, or — worst —
      // re-arm the superseded run via the resume path below.
      if (prevSnapshot.current === next || next.id !== readIdRef.current) {
        return;
      }
      const events = diffSiteRead(prevSnapshot.current, next);
      const freshTerminal = events.some((event) => event.kind === "flush");
      // A retired run the server moved on its own re-arms before its fresh
      // events land: a deferred read that resumed (queued/reading again) or
      // that jumped straight to a NEW terminal between the slow polls —
      // without READ_STARTED the machine would drop that outcome as stale.
      if (
        machine.current.activeReadId !== next.id &&
        (next.status === "queued" || next.status === "reading" || freshTerminal)
      ) {
        dispatch({ type: "READ_STARTED", readId: next.id });
      }
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

  // Reload adoption: the restored snapshot becomes the diff baseline (the
  // recap upstream already summarized it — a reload summarizes, never
  // replays), the draft prefills from it, and a snapshot that is already
  // terminal concludes through the SAME pending-terminal path a live poll
  // takes: proposal first, clarify question if any, then the terminal
  // outcome and review.
  const adopted = useRef(false);
  useEffect(() => {
    if (adopted.current || adoptedRead === null) {
      return;
    }
    adopted.current = true;
    prevSnapshot.current = adoptedRead;
    appliedReadVersion.current = adoptedRead.draft_version;
    setDraft((current) => prefill(current, adoptedRead.profile_fields));
    setSelectedFactKeys(
      [...new Set(adoptedRead.facts.map((fact) => fact.value_key))].slice(
        0,
        MAX_SELECTED_FACTS,
      ),
    );
    if (adoptedRead.status === "ready" || adoptedRead.status === "partial") {
      concludeFreshTerminal(adoptedRead);
    }
  }, [adoptedRead, concludeFreshTerminal, setDraft, setSelectedFactKeys]);

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
    // The moment a replacement URL is submitted, the old run is dead to this
    // hook: its poll stops and any in-flight snapshot is ignored, so a stale
    // terminal can never conclude the conversation for the wrong site.
    onMutate: () => {
      readIdRef.current = null;
      setReadId(null);
      pendingTerminal.current = null;
      setProposalArmed(false);
    },
    onSuccess: (data) => {
      onReadStarted(data);
      readIdRef.current = data.id;
      setReadId(data.id);
      // draft_version counts within ONE dossier; a new read starts over.
      appliedReadVersion.current = 0;
      prevSnapshot.current = null;
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

  // A persistently failing poll must not strand the act in co.reading:
  // isError flips only after react-query exhausted its retries (a transient
  // error that recovers never lands here), and only a still-active,
  // not-yet-concluding run is concluded as failed — the machine's failed
  // path then offers the manual/paste fallback.
  useEffect(() => {
    if (!siteRead.isError) {
      return;
    }
    const activeReadId = machine.current.activeReadId;
    if (activeReadId === null || pendingTerminal.current !== null) {
      return;
    }
    dispatch({
      type: "NARRATION",
      readId: activeReadId,
      entry: {
        kind: "narration",
        id: `${activeReadId}:poll-failed`,
        i18nKey: "ob.conv.read.pollFailed",
      },
    });
    dispatch({
      type: "READ_TERMINAL",
      readId: activeReadId,
      status: "failed",
      findings: prevSnapshot.current?.profile_fields.length ?? 0,
    });
  }, [siteRead.isError, dispatch, machine]);

  const { locale } = useLocale();
  const proposal = useQuery({
    queryKey: ["onboarding-company-proposal", readId, locale],
    enabled: proposalArmed && proposalJoin === "ready",
    queryFn: async (): Promise<Proposal> => {
      // The open questions' copy speaks the user's language; option values
      // stay locale-invariant server-side.
      const { data, error } = await api.GET("/onboarding/company/proposal", {
        params: { query: { locale } },
      });
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
  // with no questions left the review opens straight away. A proposal
  // failure must never stall the act: the outcome still lands and the
  // review builds from the site-read snapshot, after one honest turn.
  //
  // The ordering contract with the machine: CLARIFY and READ_TERMINAL are
  // run-correlated and must precede the retirement READ_TERMINAL performs;
  // REVIEW_READY deliberately carries NO run id — its guard is the recorded
  // readCompleted flag, so review stays reachable after the run retires.
  // Reordering these dispatches, or correlating REVIEW_READY, would strand
  // a completed read one event short of its review.
  useEffect(() => {
    const terminal = pendingTerminal.current;
    if (!terminal) {
      return;
    }
    // A failed wizard-state join means the proposal can only answer for a
    // PREVIOUS read; the snapshot fallback is the honest source then.
    if (proposal.isError || proposalJoin === "failed") {
      pendingTerminal.current = null;
      dispatch({
        type: "NARRATION",
        readId: terminal.readId,
        entry: {
          kind: "narration",
          id: `${terminal.readId}:proposal-fallback`,
          i18nKey: "ob.conv.review.proposalFallback",
        },
      });
      dispatch({ type: "READ_TERMINAL", ...terminal });
      dispatch({ type: "REVIEW_READY" });
      return;
    }
    const data = proposal.data;
    if (!data) {
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
  }, [proposal.data, proposal.isError, proposalJoin, answers, dispatch]);

  return { startRead, siteRead, proposal, prevSnapshot };
}
