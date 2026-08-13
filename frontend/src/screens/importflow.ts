// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import { throwProblem } from "./common";
import type {
  ImportObject,
  ImportProfile,
  ImportReport,
  ImportRun,
} from "./importtypes";
import { DONT_IMPORT } from "./importtypes";

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

  const [object, setObject] = useState<ImportObject>("lead");
  const [profile, setProfile] = useState<ImportProfile | null>(null);
  const [mapping, setMapping] = useState<Record<string, string>>({});
  const [run, setRun] = useState<ImportRun | null>(null);
  const [report, setReport] = useState<ImportReport | null>(null);

  // Every step clears what the steps after it said. A profile from one file
  // beside a report from another is the one way this screen could lie about
  // what is being imported.
  const restart = () => {
    setProfile(null);
    setMapping({});
    setRun(null);
    setReport(null);
  };

  const upload = useMutation({
    mutationFn: async (file: File): Promise<ImportProfile> => {
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
      return payload as ImportProfile;
    },
    onSuccess: (next) => {
      setProfile(next);
      setMapping(next.suggested_mapping);
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
      const created = data as ImportRun;
      const { data: fetched, error: reportError } = await api.GET(
        "/imports/{id}/report",
        { params: { path: { id: created.id } } },
      );
      if (reportError) {
        throwProblem(reportError);
      }
      return { run: created, report: fetched as ImportReport };
    },
    onSuccess: ({ run: created, report: dryRun }) => {
      setRun(created);
      setReport(dryRun);
    },
  });

  const commit = useMutation({
    mutationFn: async (approving: ImportRun) => {
      const { data, error } = await api.POST("/imports/{id}/approve", {
        params: { path: { id: approving.id } },
      });
      if (error) {
        throwProblem(error);
      }
      const committed = data as ImportRun;
      const { data: fetched, error: reportError } = await api.GET(
        "/imports/{id}/report",
        { params: { path: { id: committed.id } } },
      );
      if (reportError) {
        throwProblem(reportError);
      }
      return { run: committed, report: fetched as ImportReport };
    },
    onSuccess: ({ run: committed, report: outcome }) => {
      setRun(committed);
      setReport(outcome);
      // The import wrote leads, organizations and their events. Every cached
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
    upload,
    validate,
    commit,
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
