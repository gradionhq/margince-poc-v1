// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { Callout } from "../design-system/callout";
import { useT } from "../i18n";
import { throwProblem } from "../screens/common";
import { embedReindexStatusQueryKey } from "../screens/embedreindex";
import { useCan } from "./capability";

// The reindex-needed advisory (v6 B2, rekeyed per ADR-0069 §3a): shown ONLY
// on an identity mismatch (configured_identity ≠ populated_identity) — the
// operator changed the embed binding and has not confirmed the rebuild, the
// one state a human must act on. Identity-matched pending entities (a lost
// embed event) are NOT this banner's business: the worker drift sweep heals
// them automatically, and the settings card (screens/embedreindex.tsx) shows
// the pending detail meanwhile. Keying off reindex_needed here would show an
// admin a banner about drift the system is already fixing. The status read
// this banner needs is itself RBAC-gated (embedding_reindex:read, granted to
// admin and ops alone), so the gate below is not a UX preference layered over
// an open endpoint — it is the same grant the server checks, asked before
// issuing a query that could only 403.
export function EmbedReindexBanner() {
  const t = useT();
  const enabled = useCan("embedding_reindex", "read");
  const query = useQuery({
    queryKey: embedReindexStatusQueryKey,
    enabled,
    staleTime: 5 * 60_000,
    queryFn: async () => {
      const { data, error } = await api.GET("/embeddings/reindex/status");
      if (error) {
        throwProblem(error);
      }
      if (!data) {
        throw new Error("malformed reindex status response");
      }
      return data;
    },
  });
  // Advisory only: a failed status probe must not block the app shell — the
  // settings card (screens/embedreindex.tsx) surfaces the same read's error
  // state to the accountable audience.
  // The mismatch ALONE keys the banner (SEARCH-AC-13) — deliberately not
  // status-qualified: suppressing during status="reembedding" would also
  // hide a rebuild whose job was drift-cancelled and left the marker
  // stuck, exactly the state the settings card's recovery affordance
  // exists for. A redundant banner during a healthy rebuild is the
  // cheaper failure.
  if (
    !enabled ||
    query.isError ||
    !query.data ||
    query.data.configured_identity === query.data.populated_identity
  ) {
    return null;
  }
  return (
    <Callout
      className="appbanner"
      live="status"
      actions={<a href="#/settings/maintenance">{t("reindexbanner.link")}</a>}
    >
      {t("reindexbanner.needed")}
    </Callout>
  );
}
