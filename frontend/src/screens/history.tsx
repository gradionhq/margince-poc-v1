import {
  type UseInfiniteQueryResult,
  useInfiniteQuery,
} from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import type { EntityKind } from "../app/entity";
import {
  Button,
  EmptyState,
  SectionHeader,
  Skeleton,
} from "../design-system/atoms";
import { type Provenance, ProvenanceTag } from "../design-system/trust";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage } from "./common";
import "./history.css";

// The per-record plain-language change list (B-EP09.x): every audit_log row
// for one record, rendered as a `summary` line the endpoint already wrote in
// prose — this panel never re-derives wording from before/after diffs, it
// just attributes and paginates what the contract hands back.

type AuditHistoryEntry = components["schemas"]["AuditHistoryEntry"];
type AuditHistoryListResponse =
  components["schemas"]["AuditHistoryListResponse"];

export function useRecordHistory(
  kind: EntityKind,
  id: string,
): UseInfiniteQueryResult<AuditHistoryListResponse> {
  return useInfiniteQuery({
    queryKey: ["record-history", kind, id],
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET(
        "/records/{entity_type}/{id}/history",
        {
          params: {
            path: { entity_type: kind, id },
            query: { limit: 20, ...(pageParam ? { cursor: pageParam } : {}) },
          },
        },
      );
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });
}

// captured_by-style strings aren't on this projection — the endpoint already
// splits actor_type/actor_id, so the Provenance maps straight off actor_type
// (system/connector read as "agent": neither is a human typing).
function provenanceOfEntry(entry: AuditHistoryEntry): Provenance {
  return entry.actor_type === "human"
    ? { kind: "human" }
    : { kind: "agent", agent: entry.actor_id };
}

function HistoryEntryRow({
  entry,
  locale,
}: Readonly<{
  entry: AuditHistoryEntry;
  locale: ReturnType<typeof useLocale>["locale"];
}>) {
  const t = useT();
  return (
    <li>
      <span className="tl-body">
        <span className="tl-title">
          {entry.summary}
          {entry.on_behalf_of_name && (
            <span className="history-onbehalf">
              {" "}
              {t("history.onBehalfOf", { name: entry.on_behalf_of_name })}
            </span>
          )}
        </span>
        <span className="tl-meta">
          <span>
            {formatDateTime(entry.occurred_at, locale, "Europe/Berlin")}
          </span>
          <ProvenanceTag provenance={provenanceOfEntry(entry)} />
        </span>
      </span>
    </li>
  );
}

export function RecordHistory({
  kind,
  id,
}: Readonly<{ kind: EntityKind; id: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const query = useRecordHistory(kind, id);
  const entries = query.data?.pages.flatMap((page) => page.data) ?? [];

  // Honest state matrix (§3a): loading, error, empty, then the list — kept
  // as sequential branches rather than a nested ternary in the JSX below.
  let body: ReactNode;
  if (query.isPending) {
    body = (
      <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        <Skeleton width="60%" />
        <Skeleton width="90%" />
        <Skeleton width="75%" />
      </div>
    );
  } else if (query.isError) {
    body = (
      <EmptyState>
        <p>{t("common.error")}</p>
        <p className="t-mono" style={{ marginTop: 6 }}>
          {query.error instanceof Error ? query.error.message : null}
        </p>
        <Button small onClick={() => query.refetch()} style={{ marginTop: 10 }}>
          {t("common.retry")}
        </Button>
      </EmptyState>
    );
  } else if (entries.length === 0) {
    body = <EmptyState>{t("history.empty")}</EmptyState>;
  } else {
    body = (
      <>
        <ul className="timeline">
          {entries.map((entry) => (
            <HistoryEntryRow key={entry.id} entry={entry} locale={locale} />
          ))}
        </ul>
        {query.hasNextPage && (
          <Button
            small
            disabled={query.isFetchingNextPage}
            onClick={() => query.fetchNextPage()}
            style={{ marginTop: 10 }}
          >
            {t("settings.loadMore")}
          </Button>
        )}
      </>
    );
  }

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <SectionHeader title={t("history.title")} />
      {body}
    </section>
  );
}
