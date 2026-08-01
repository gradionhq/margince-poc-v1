// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { useT } from "../i18n";
import {
  canConfigureAutomations,
  problemMessage,
  useMe,
} from "../screens/common";
import { embedReindexStatusQueryKey } from "../screens/embedreindex";

// The reindex-needed advisory (v6 B2, rekeyed per ADR-0069 §3a): shown ONLY
// on an identity mismatch (configured_identity ≠ populated_identity) — the
// operator changed the embed binding and has not confirmed the rebuild, the
// one state a human must act on. Identity-matched pending entities (a lost
// embed event) are NOT this banner's business: the worker drift sweep heals
// them automatically, and the settings card (screens/embedreindex.tsx) shows
// the pending detail meanwhile. Keying off reindex_needed here would show an
// admin a banner about drift the system is already fixing. Gated to
// ops/admin, same as EconomyBanner: only the settings card's confirm/rebuild
// actions are admin/ops-restricted server-side, but the banner itself is an
// ops/admin surface — a rep or read_only user has nothing actionable to do
// with it.
export function EmbedReindexBanner() {
  const t = useT();
  const me = useMe();
  const enabled = canConfigureAutomations(me.data?.roles);
  const query = useQuery({
    queryKey: embedReindexStatusQueryKey,
    enabled,
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
  if (
    !enabled ||
    query.isError ||
    !query.data ||
    query.data.configured_identity === query.data.populated_identity
  ) {
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
