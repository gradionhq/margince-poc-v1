// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Exporting what the filter selects (AC-filters-and-views-8).
//
// `/exports` takes the same predicate the preview does, so this sends the tree
// the reader is looking at rather than a saved view's id: what you export is
// what the count above it says, through the one filter engine. Nothing here
// re-derives a slice.
//
// Two things this file does NOT do, both deliberately. It does not page the
// export — the server renders the whole matching slice, and a client-side
// stitch of preview pages would be a second, wrong answer to the same question.
// And it does not ask the reader to confirm: clicking "Export CSV" IS the
// confirmation, and the server writes the audit row that makes the export
// accountable (P7/P12).

import { useMutation } from "@tanstack/react-query";
import { api } from "../api/client";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";
import { downloadBytes, filenameFromDisposition } from "./download";
import type { FilterResource } from "./filterdata";
import { encode, isComplete, type Node } from "./segmentpredicate";

/** The formats the contract offers. Closed — the server enumerates these two. */
const FORMATS = ["csv", "json"] as const;
type Format = (typeof FORMATS)[number];

const MIME: Record<Format, string> = {
  csv: "text/csv",
  json: "application/json",
};

/** A table rather than a ternary, so adding a third format cannot forget one. */
const LABEL: Record<Format, MessageKey> = {
  csv: "filters.exportCsv",
  json: "filters.exportJson",
};

/**
 * "Export" beside the builder, one item per format.
 *
 * Gated on `isComplete` for the same reason Save is: an incomplete tree is one
 * the engine refuses, and an export button that answers 422 has told the reader
 * nothing they could not have been spared.
 */
export function ExportFilterMenu({
  resource,
  tree,
}: Readonly<{ resource: FilterResource; tree: Node }>) {
  const t = useT();
  const run = useMutation({
    // The tree arrives as a variable rather than through a closure: the click
    // belongs to the committed render, so what it hands over cannot be older
    // than the filter the reader is looking at.
    mutationFn: async (input: Readonly<{ format: Format }>) => {
      const { data, error, response } = await api.POST("/exports", {
        body: { object: resource, filter: encode(tree), format: input.format },
        // The body is a rendered file, not a document to parse. Asking for text
        // keeps CSV intact — JSON.parse over a CSV would throw, and over the
        // JSON format it would reparse bytes we are about to write out verbatim.
        parseAs: "text",
      });
      if (error) {
        throwProblem(error);
      }
      downloadBytes(
        data,
        // The server knows which table and format it just rendered, so its name
        // is the true one; the fallback only covers a response that sent none.
        filenameFromDisposition(
          response.headers.get("Content-Disposition"),
          `${resource}-export.${input.format}`,
        ),
        MIME[input.format],
      );
    },
  });

  if (!isComplete(tree)) {
    return null;
  }

  return (
    <>
      {/* Two labelled buttons rather than a menu. There are exactly two formats,
          so a menu would hide a short list behind a click — and the saved-view
          rail beside this already spends the one unlabelled "…" this header can
          afford. Two of those side by side is a header where nothing says which
          is which, which is what the rendered screen showed. */}
      {FORMATS.map((format) => (
        <Button
          key={format}
          small
          disabled={run.isPending}
          onClick={() => run.mutate({ format })}
        >
          {t(LABEL[format])}
        </Button>
      ))}
      {run.isError && (
        // Spoken, and it carries the server's own reason: a bulk read can be
        // refused for reasons a reader can act on, and "request failed" is not
        // one of them.
        <span className="filters-export-error" role="alert">
          {problemMessageOf(run.error, t)}
        </span>
      )}
    </>
  );
}
