// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { useT } from "../i18n";
import { problemMessage } from "../screens/common";
import { embedReindexStatusQueryKey } from "../screens/embedreindex";

// The reindex-needed advisory (v6 B2). Keyed OFF reindex_needed alone, NEVER
// entities_pending: reindex_needed also flips true on identity drift even
// when entities_pending briefly reads 0 (search/binding.go), so a naive
// entities_pending > 0 check would miss that case and a naive "entities_
// pending stayed nonzero" check would fire on stale data that only looks
// wrong. The status read is wide-granted to every role (migration 0114:
// "every role reads it — any user's UI needs to show the reindex-needed
// banner"), so this component carries NO role gate — only the settings
// card's confirm/rebuild actions (screens/embedreindex.tsx) are admin/ops-
// restricted, since those are the ones that would 403 otherwise.
export function EmbedReindexBanner() {
  const t = useT();
  const query = useQuery({
    queryKey: embedReindexStatusQueryKey,
    staleTime: 5 * 60_000,
    queryFn: async () => {
      const { data, error } = await api.GET("/embeddings/reindex/status");
      if (error) {
        throw new Error(problemMessage(error));
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
  if (query.isError || !query.data?.reindex_needed) {
    return null;
  }
  return (
    <div
      role="status"
      className="card card-inset"
      style={{
        borderRadius: 0,
        display: "flex",
        gap: "var(--space-3)",
        alignItems: "center",
      }}
    >
      <span>{t("reindexbanner.needed")}</span>
      <a href="#/settings/data">{t("reindexbanner.link")}</a>
    </div>
  );
}
