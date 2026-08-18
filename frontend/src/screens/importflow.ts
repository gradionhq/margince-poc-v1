// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import {
  skipToken,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useRef, useState } from "react";
import { api } from "../api/client";
import { throwProblem } from "./common";
import type {
  ImportObject,
  ImportProfile,
  ImportReport,
  ImportRun,
} from "./importtypes";
import { DONT_IMPORT } from "./importtypes";

// A mutation's answer, stamped with the flow generation it was asked in.
type Generational<T> = Readonly<{ at: number; value: T }>;

// What one run and its report look like together — the pair every step of this
// screen renders from.
type RunAndReport = Readonly<{ run: ImportRun; report: ImportReport }>;

// asProfile checks the ONE payload nothing else types. The upload is a
// hand-rolled multipart fetch — the generated client cannot send a file part —
// so its answer arrives as parsed JSON with no shape behind it. A cast here
// would be a promise the code has no way to keep, so every member the screen
// reads is checked and the value is rebuilt from the checked parts.
function asProfile(payload: unknown): ImportProfile {
  if (typeof payload !== "object" || payload === null) {
    throw new Error(unreadableUpload);
  }
  // Read by name rather than destructured through a cast: the payload is
  // genuinely unknown here, and asserting a shape onto it is the promise this
  // function exists to avoid making.
  const read = (name: string): unknown =>
    name in payload ? Reflect.get(payload, name) : undefined;
  const source_ref = read("source_ref");
  const object = read("object");
  const rows_profiled = read("rows_profiled");
  const columns = read("columns");
  const targets = read("targets");
  const suggested_mapping = read("suggested_mapping");
  if (
    typeof source_ref !== "string" ||
    typeof rows_profiled !== "number" ||
    !Array.isArray(columns) ||
    !Array.isArray(targets) ||
    !isImportObject(object) ||
    !isMapping(suggested_mapping)
  ) {
    throw new Error(unreadableUpload);
  }
  return {
    source_ref,
    object,
    rows_profiled,
    columns,
    targets,
    suggested_mapping,
  };
}

const unreadableUpload =
  "the upload answered something this screen cannot read";

function isImportObject(value: unknown): value is ImportObject {
  return value === "lead" || value === "organization";
}

function isMapping(value: unknown): value is Record<string, string> {
  return (
    typeof value === "object" &&
    value !== null &&
    Object.values(value).every((entry) => typeof entry === "string")
  );
}

// emptyReport stands in when a run's report could not be read back. It states
// the run's own status and nothing it does not know, rather than inventing
// counts: the screen shows the run happened and what state it is in.
function emptyReport(run: ImportRun): ImportReport {
  return {
    run_id: run.id,
    status: run.status,
    rows_read: 0,
    disposition: { created: 0, updated: 0, unchanged: 0, skipped: 0 },
    issues: [],
    source_key_used: "",
  };
}

// Where the reader got to, kept outside React so a remount can find it again.
//
// localStorage rather than the URL, and the flow this exists for is why: "edit
// the one row you don't want reversed, then undo the rest" leaves this screen
// through the nav to the Leads list and comes BACK through the nav, so there is
// no history entry to return to and no URL to carry. A run is also nobody
// else's to open — it belongs to the seat that started it, and the server
// answers 404 to anyone else — so a shareable link would promise something it
// cannot deliver. The cost of the choice is that the reference is per browser,
// not per tab: two tabs on this screen remember the same run.
const REMEMBERED_RUN_KEY = "margince.import.run";

// Storage is unavailable in some embedded contexts; a run this screen cannot
// remember is an affordance the reader has to reach another way, never an error.
function readRememberedRun(): string | null {
  try {
    return window.localStorage.getItem(REMEMBERED_RUN_KEY);
  } catch {
    return null;
  }
}

function rememberRun(id: string): void {
  try {
    window.localStorage.setItem(REMEMBERED_RUN_KEY, id);
  } catch {
    // A browser refusing storage must not break the import itself.
  }
}

function forgetRememberedRun(): void {
  try {
    window.localStorage.removeItem(REMEMBERED_RUN_KEY);
  } catch {
    // Nothing to forget is the state this was trying to reach anyway.
  }
}

// Which run states still have something for a reader to DO: `complete` can be
// undone, `failed` resumed, `undoing` continued. Everything else is either
// still moving under the server's own hand or already spent, and putting one of
// those back on screen would offer a button whose press answers 409.
function unfinished(status: ImportRun["status"]): boolean {
  return status === "complete" || status === "failed" || status === "undoing";
}

// What re-reading a remembered run answered.
//
// Three outcomes rather than a pair, because "the server will not answer for
// this id" and "the server could not be reached" must not be treated alike: the
// first is a reference to forget, the second is one to keep and ask about again.
type Recovered =
  | Readonly<{ kind: "run"; value: RunAndReport }>
  | Readonly<{ kind: "spent" }>
  | Readonly<{ kind: "unreachable" }>;

// A refusal that settles the question of whether this reader may see this run:
// gone, never theirs (another organization's or another seat's — existence is
// hidden as a 404), or a grant they no longer hold.
function refused(status: number): boolean {
  return status === 403 || status === 404 || status === 410;
}

// recoverRun re-reads the run a previous visit left behind. The report is read
// beside it and stood in for when that second read fails: the RUN is what the
// affordances are derived from, and losing the whole recovery because a report
// read failed would hide an undo that is still perfectly available.
async function recoverRun(id: string): Promise<Recovered> {
  const { data: run, response } = await api.GET("/imports/{id}", {
    params: { path: { id } },
  });
  if (!run) {
    return refused(response.status)
      ? { kind: "spent" }
      : { kind: "unreachable" };
  }
  if (!unfinished(run.status)) {
    return { kind: "spent" };
  }
  const { data: report } = await api.GET("/imports/{id}/report", {
    params: { path: { id } },
  });
  return { kind: "run", value: { run, report: report ?? emptyReport(run) } };
}

// readRun reads a run and its report back from the server. Answers null when
// even that fails: the caller is already handling one failure and must not lose
// it behind a second.
async function readRun(id: string): Promise<RunAndReport | null> {
  const { data: run } = await api.GET("/imports/{id}", {
    params: { path: { id } },
  });
  const { data: report } = await api.GET("/imports/{id}/report", {
    params: { path: { id } },
  });
  if (!run || !report) {
    return null;
  }
  return { run, report };
}

// What one validation needs, handed to the mutation rather than closed over.
type ValidateInput = Readonly<{
  object: ImportObject;
  profile: ImportProfile;
  mapping: Record<string, string>;
}>;

// The import's state machine, kept out of the card so the card is markup.
//
// Three steps, each of which invalidates the ones after it: a profile from one
// file beside a report from another is the one way this screen could lie about
// what is being imported.
export function useImportFlow() {
  const queryClient = useQueryClient();
  // Which flow a response belongs to. A dry run over a whole file is a real
  // multi-second window, and the human can switch object or upload again inside
  // it — so every mutation carries the generation it started in, and a reply
  // from a generation that has been cleared is DROPPED. Without it, a validate
  // that resolves after a restart puts its run and report back on a screen that
  // now says something else, and the commit button would write the old file.
  const generation = useRef(0);
  const current = () => generation.current;

  const [object, setObject] = useState<ImportObject>("lead");
  const [profile, setProfile] = useState<ImportProfile | null>(null);
  const [mapping, setMapping] = useState<Record<string, string>>({});
  const [run, setRun] = useState<ImportRun | null>(null);
  const [report, setReport] = useState<ImportReport | null>(null);
  // Whether the run on screen was recovered rather than produced here. The card
  // says so: an outcome a reader did not just cause, presented as if they had,
  // reads as an import that ran by itself.
  const [resumed, setResumed] = useState(false);

  // The id left behind by an earlier visit, read ONCE at mount — this exists to
  // recover a run the screen has lost, and a session already holding one has
  // nothing to recover. From here it only ever goes to null, when the server
  // will not answer for it.
  const [remembered, setRemembered] = useState<string | null>(
    readRememberedRun,
  );

  // noteRun is the screen's only memory of where the reader got to, so the
  // stored reference and the affordance it stands for cannot disagree: a run
  // with something left to do is remembered, and a spent one is forgotten in
  // the same breath — undo must never be offered twice for the same run.
  //
  // It also ends the "picked up from earlier" notice: whatever the run was when
  // the card found it, what is on screen now is the answer to a press the reader
  // just made.
  const noteRun = (next: ImportRun) => {
    setResumed(false);
    if (unfinished(next.status)) {
      rememberRun(next.id);
      return;
    }
    forgetRememberedRun();
  };

  // Asked once, at mount, and never refetched: this is a recovery of state the
  // screen lost, not a live view of the run. `skipToken` rather than `enabled`
  // so the id the request uses is the one the query is keyed on.
  const recovery = useQuery({
    queryKey: ["importRunRecovery", remembered],
    queryFn: remembered === null ? skipToken : () => recoverRun(remembered),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });

  // The recovery is considered EXACTLY once, whatever it turns out to say.
  //
  // Once, because a reader who has already chosen a file by the time it lands
  // is doing something newer than what it carries, and a recovery that stayed
  // pending would put that stale run back the next time the card went quiet.
  const [considered, setConsidered] = useState(false);
  if (!considered && recovery.data) {
    setConsidered(true);
    if (recovery.data.kind === "spent") {
      // Gone, never this reader's, or already undone. Asking again on every
      // mount would be asking a question that has been answered.
      setRemembered(null);
      forgetRememberedRun();
    }
    if (recovery.data.kind === "run" && run === null && profile === null) {
      setRun(recovery.data.value.run);
      setReport(recovery.data.value.report);
      setResumed(true);
    }
  }

  // Every step clears what the steps after it said. A profile from one file
  // beside a report from another is the one way this screen could lie about
  // what is being imported.
  // clearAnswers drops everything the previous file answered and moves the
  // generation on, so a reply still in flight cannot put any of it back.
  const clearAnswers = () => {
    generation.current += 1;
    validate.reset();
    commit.reset();
    undo.reset();
    setProfile(null);
    setMapping({});
    setRun(null);
    setReport(null);
    setResumed(false);
    // The reader has moved on to another file, so the run they left behind is
    // no longer what this card is about. The run itself is untouched server-side
    // — this drops the screen's pointer to it, which is what "start again" means
    // here and what keeps the next mount from reopening a finished import.
    forgetRememberedRun();
  };

  // restart is the human's own "start again": it clears the answers AND the
  // upload's error, which clearAnswers deliberately leaves alone because the
  // upload path calls it while its own mutation is starting.
  const restart = () => {
    clearAnswers();
    upload.reset();
  };

  const upload = useMutation({
    // Cleared as the upload STARTS, not when it succeeds: an upload that fails
    // would otherwise leave the previous file's report and its armed commit
    // button on screen beside the new file's error, and nothing on this card
    // names which file a report belongs to.
    onMutate: () => {
      clearAnswers();
    },
    mutationFn: async (file: File): Promise<Generational<ImportProfile>> => {
      const at = current();
      // Multipart by hand: the generated client serializes JSON bodies, and
      // this operation takes a file part.
      const body = new FormData();
      body.append("object", object);
      body.append("file", file);
      const response = await fetch("/v1/imports/sources", {
        method: "POST",
        body,
        credentials: "include",
      });
      const payload = await response.json().catch(() => undefined);
      if (!response.ok) {
        throwProblem(payload);
      }
      return { at, value: asProfile(payload) };
    },
    onSuccess: ({ at, value }) => {
      if (at !== current()) {
        return;
      }
      setProfile(value);
      setMapping(value.suggested_mapping);
      setRun(null);
      setReport(null);
    },
  });

  // The profile and the mapping arrive as the mutation's VARIABLE rather than
  // through the closure: a mutationFn that reads state it closed over runs
  // against whatever that state was when the function was made, which for a
  // screen the human is still editing is the wrong file's mapping.
  const validate = useMutation({
    mutationFn: async (input: ValidateInput) => {
      const at = current();
      const { data, error } = await api.POST("/imports", {
        body: {
          connector: "csv",
          object: input.object,
          source_ref: input.profile.source_ref,
          mapping: mappedOnly(input.mapping),
        },
      });
      if (error) {
        throwProblem(error);
      }
      const created = data;
      const { data: fetched, error: reportError } = await api.GET(
        "/imports/{id}/report",
        { params: { path: { id: created.id } } },
      );
      if (reportError) {
        throwProblem(reportError);
      }
      return { at, value: { run: created, report: fetched } };
    },
    onSuccess: ({ at, value }) => {
      if (at !== current()) {
        return;
      }
      setRun(value.run);
      setReport(value.report);
    },
  });

  const commit = useMutation({
    mutationFn: async (approving: ImportRun) => {
      const at = current();
      const { data, error } = await api.POST("/imports/{id}/approve", {
        params: { path: { id: approving.id } },
      });
      if (error) {
        // A commit that stops part-way answers with a problem, not with the
        // run — but the RUN is where the truth is: the server recorded the
        // failure and its checkpoint before answering. Reading it back is what
        // turns "something went wrong" into the resumable state the contract
        // promises (IEM-WIRE-6). Anything else is re-raised unchanged.
        const stopped = await readRun(approving.id);
        if (stopped && stopped.run.status === "failed") {
          return { at, value: stopped };
        }
        throwProblem(error);
      }
      // The estate is ALREADY written by the time approve answers. So the run
      // is what this returns, with or without its report: losing the whole
      // outcome because a follow-up read failed would leave the screen offering
      // an import that has in fact already happened, and the second press would
      // answer 409 with no explanation.
      const committed = data;
      const { data: fetched } = await api.GET("/imports/{id}/report", {
        params: { path: { id: committed.id } },
      });
      return {
        at,
        value: {
          run: committed,
          report: fetched ?? emptyReport(committed),
        },
      };
    },
    onSuccess: ({ at, value }) => {
      if (at !== current()) {
        return;
      }
      setRun(value.run);
      setReport(value.report);
      // A committed run is the one a reader most needs to find again: undoing it
      // is the documented way back, and the row they want to keep is edited on
      // another screen. So the reference outlives this mount.
      noteRun(value.run);
      // The import wrote leads, organizations and their events. Every cached
      // list is stale, not only the ones this card could name.
      queryClient.invalidateQueries();
    },
  });

  const undo = useMutation({
    mutationFn: async (committed: ImportRun) => {
      const at = current();
      const { data, error } = await api.POST("/imports/{id}/undo", {
        params: { path: { id: committed.id } },
      });
      if (error) {
        // A reversal interrupted part-way is resumable on the same run
        // (the same shape the commit's own checkpoint gives IEM-WIRE-6) —
        // read it back rather than losing the run behind a bare error.
        const stopped = await readRun(committed.id);
        if (stopped && stopped.run.status === "undoing") {
          return { at, value: stopped };
        }
        throwProblem(error);
      }
      const undone = data;
      const { data: fetched } = await api.GET("/imports/{id}/report", {
        params: { path: { id: undone.id } },
      });
      return {
        at,
        value: { run: undone, report: fetched ?? emptyReport(undone) },
      };
    },
    onSuccess: ({ at, value }) => {
      if (at !== current()) {
        return;
      }
      setRun(value.run);
      setReport(value.report);
      // A reversal that finished spends the reference; one that stopped part-way
      // keeps it, because continuing it is the whole point of `undoing`.
      noteRun(value.run);
      // Every reversed row is a lead or organization archived. Every cached
      // list is stale, not only the ones this card could name.
      queryClient.invalidateQueries();
    },
  });

  return {
    object,
    chooseObject: (next: ImportObject) => {
      setObject(next);
      restart();
    },
    profile,
    mapping,
    setTarget: (column: string, target: string) =>
      setMapping((current) => ({ ...current, [column]: target })),
    run,
    report,
    resumed,
    upload,
    validate,
    commit,
    undo,
    restart,
  };
}

// mappedOnly drops the columns the human left on "don't import": the wire
// carries the mapping they chose, not every column the file happened to have.
function mappedOnly(mapping: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [column, target] of Object.entries(mapping)) {
    if (target && target !== DONT_IMPORT) {
      out[column] = target;
    }
  }
  return out;
}
