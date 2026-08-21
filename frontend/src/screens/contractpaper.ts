// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import type { SectionState } from "../design-system/surfacestate";
import { ProblemError, throwProblem } from "./common";

// The paper filed against ONE agreement, read the same way by the two surfaces
// that show it: the contract form's signed-document field and the contract row
// on the account page.
//
// THE ENDPOINT PAGINATES, AND A PAGE IS NOT A LIST. `/organizations/{id}/
// documents` answers 50 rows by default and 200 at most, with `page.has_more`
// saying whether that was everything. Both surfaces used to keep `data.data`
// and drop the envelope, so an agreement with more filed documents than fit in
// one page showed the first page and reported itself complete — the reader had
// no way to tell "these are the documents" from "these are some of them", which
// is the one distinction a list of legal paper cannot afford to lose.

type Attachment = components["schemas"]["Attachment"];

// One page, at the endpoint's own maximum. A limit is a cap and not a fetch
// size, so asking for 200 costs nothing on the ordinary agreement that has two
// documents, and it puts the truncation notice below where a real library sits
// rather than at the default 50.
const PAPER_PAGE_LIMIT = 200;

// How many further pages the count may walk. The endpoint states no total, so
// "and N more" has to be counted — and counting is bounded, because an
// unbounded walk turns one field on one row into as many round trips as
// somebody has uploaded files.
const PAPER_TAIL_PAGES = 5;

/**
 * FiledPaper is the documents a surface may show, plus what it knows about the
 * ones it is not showing.
 *
 * `remaining` is 0 when the read reached the end of the list — the only state
 * in which a surface may present itself as the whole picture. A positive count
 * is what did not fit. UNDEFINED means there is more and the bounded count
 * never reached the end: the server publishes no total, so a number here would
 * be one this client invented, and the reader is told the list is partial
 * without being told a size nobody measured.
 */
export type FiledPaper = {
  documents: Attachment[];
  remaining?: number;
};

type PaperPage = {
  documents: Attachment[];
  nextCursor: string | undefined;
  hasMore: boolean;
};

async function paperPage(
  orgId: string,
  contractId: string | undefined,
  cursor: string | undefined,
): Promise<PaperPage> {
  const { data, error } = await api.GET("/organizations/{id}/documents", {
    params: {
      path: { id: orgId },
      query: { contract_id: contractId, limit: PAPER_PAGE_LIMIT, cursor },
    },
  });
  if (error) {
    throwProblem(error);
  }
  return {
    documents: data?.data ?? [],
    // A body carrying no page envelope claims no continuation, which is the
    // same answer as `has_more: false` — the honest reading, because the only
    // alternative is to call a list partial on the strength of a field that
    // was never there.
    nextCursor: data?.page?.next_cursor ?? undefined,
    hasMore: data?.page?.has_more === true,
  };
}

/**
 * fetchFiledPaper reads the first page of an agreement's documents and, when
 * the server says that was not all of them, counts the rest.
 *
 * The count walks pages WITHOUT keeping their rows: what the surfaces need is
 * one honest sentence about the remainder, not another two hundred links. It
 * stops at `PAPER_TAIL_PAGES` and returns no count at all rather than a guess,
 * and it stops the moment a page says there is more but hands back no cursor —
 * there is no way onward from there, and looping would be the same page over.
 */
export async function fetchFiledPaper(
  orgId: string,
  contractId: string | undefined,
): Promise<FiledPaper> {
  const first = await paperPage(orgId, contractId, undefined);
  if (!first.hasMore) {
    return { documents: first.documents, remaining: 0 };
  }
  let counted = 0;
  let cursor = first.nextCursor;
  for (let page = 0; page < PAPER_TAIL_PAGES; page += 1) {
    if (!cursor) {
      return { documents: first.documents };
    }
    const tail = await paperPage(orgId, contractId, cursor);
    counted += tail.documents.length;
    if (!tail.hasMore) {
      return { documents: first.documents, remaining: counted };
    }
    cursor = tail.nextCursor;
  }
  return { documents: first.documents };
}

/**
 * useContractPaper is the ONE read both surfaces share, under one query key so
 * the form and the row show the same paper and one upload invalidates both.
 *
 * An agreement being CREATED has no id and therefore no paper — asking would be
 * a request for the documents of a record that does not exist yet.
 */
export function useContractPaper(
  orgId: string,
  contractId: string | undefined,
) {
  return useQuery({
    queryKey: ["contractPaper", orgId, contractId],
    enabled: Boolean(contractId),
    queryFn: () => fetchFiledPaper(orgId, contractId),
  });
}

/**
 * paperState is what a surface KNOWS about an agreement's documents.
 *
 * The not-ready cases are kept apart because each is a different sentence to
 * the reader, and collapsing them into an empty list makes a surface claim "no
 * paper on file" several times over when it has no idea. A contract being
 * created is the one honest empty: it cannot have documents yet.
 *
 * A 403 is WITHHELD, not failed. Reading documents carries its own grant, so a
 * reader without it must be told the answer is being kept from them rather than
 * offered a retry that will refuse again exactly the same way.
 *
 * PARTIAL is the read that landed and did not reach the end. It is not `ready`
 * with a footnote: `ready` is the surface saying "this is the list", and a
 * truncated page saying that is the defect this classification exists to stop.
 */
export function paperState(
  hasContract: boolean,
  query: { isPending: boolean; isError: boolean; error: unknown },
  paper: FiledPaper | undefined,
): SectionState {
  if (!hasContract) {
    return "empty";
  }
  if (query.isPending) {
    return "loading";
  }
  if (query.isError) {
    return problemStatus(query.error) === 403 ? "withheld" : "failed";
  }
  if (paper && paper.remaining !== 0) {
    return "partial";
  }
  return (paper?.documents.length ?? 0) === 0 ? "empty" : "ready";
}

// The HTTP status out of a thrown RFC-7807 body, or 0 when the failure carried
// none (a dropped connection throws no problem document at all).
function problemStatus(err: unknown): number {
  // `typeof null === "object"`, so the null check is not redundant: without it
  // a ProblemError carrying a null body throws while deciding how to report a
  // failure, turning a handled error into an unhandled one.
  if (
    !(err instanceof ProblemError) ||
    typeof err.problem !== "object" ||
    err.problem === null
  ) {
    return 0;
  }
  if (!("status" in err.problem)) {
    return 0;
  }
  const { status } = err.problem;
  return typeof status === "number" ? status : 0;
}
