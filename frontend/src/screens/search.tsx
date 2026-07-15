// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ENTITY, ENTITY_KINDS, type EntityKind } from "../app/entity";
import { navigate } from "../app/router";
import {
  Badge,
  EmptyState,
  SearchField,
  SectionHeader,
} from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage, QueryGate, type QueryLike } from "./common";
import "./search.css";

type SearchResult = components["schemas"]["SearchResult"];
type SearchResponse = components["schemas"]["SearchResponse"];

// RS-1/RS-2: the cross-object search results screen. Hits are grouped by
// record type (fixed display order below) so a caller scanning "acme" sees
// people, companies, deals, activities, and leads as separate sections
// rather than one undifferentiated ranked list.
const GROUP_ORDER = [
  "person",
  "organization",
  "deal",
  "activity",
  "lead",
] as const;
const GROUP_KEY: Record<string, MessageKey> = {
  person: "search.group.person",
  organization: "search.group.organization",
  deal: "search.group.deal",
  activity: "search.group.activity",
  lead: "search.group.lead",
};
// Only these hit types have a 360 to route to (the app-wide ENTITY registry).
// `activity` is a valid SearchResult type but has no record route, so it
// renders as plain text instead of an EntityRef link.
const LINKABLE_KINDS = new Set<EntityKind>(ENTITY_KINDS);

export function SearchScreen({ q }: Readonly<{ q: string }>) {
  const t = useT();
  const [draft, setDraft] = useState(q);
  const query = useQuery({
    queryKey: ["search", q],
    enabled: q.trim().length > 0,
    queryFn: async () => {
      const { data, error } = await api.GET("/search", {
        params: { query: { q, limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const next = draft.trim();
    if (next) {
      navigate({ screen: "search", id: encodeURIComponent(next) });
    }
  };

  return (
    <div className="wrap">
      <SectionHeader title={t("search.title")} />
      <form onSubmit={submit} className="search-bar">
        <SearchField
          value={draft}
          placeholder={t("search.placeholder")}
          aria-label={t("search.placeholder")}
          onChange={(event) => setDraft(event.target.value)}
        />
      </form>
      <QueryGate query={query as QueryLike<SearchResponse>}>
        {(data) =>
          data.data.length === 0 ? (
            <EmptyState>{t("search.empty", { q })}</EmptyState>
          ) : (
            <SearchGroups results={data.data} />
          )
        }
      </QueryGate>
    </div>
  );
}

function SearchGroups({ results }: Readonly<{ results: SearchResult[] }>) {
  const t = useT();
  return (
    <div className="search-groups">
      {GROUP_ORDER.filter((type) => results.some((r) => r.type === type)).map(
        (type) => (
          <section key={type} className="card search-group">
            <h2 className="t-label">{t(GROUP_KEY[type])}</h2>
            <ul className="search-hits">
              {results
                .filter((r) => r.type === type)
                .map((hit) => (
                  <SearchHit key={`${hit.type}:${hit.id}`} hit={hit} />
                ))}
            </ul>
          </section>
        ),
      )}
    </div>
  );
}

function SearchHit({ hit }: Readonly<{ hit: SearchResult }>) {
  const t = useT();
  const isLinkable = LINKABLE_KINDS.has(hit.type as EntityKind);
  return (
    <li className="search-hit">
      <div className="search-hit-title">
        {isLinkable ? (
          // The search API already returns the hit's display name as
          // `title` — routing through EntityRef here would re-fetch the
          // same record per hit (an N+1 GET per result) just to re-derive
          // a name we already have.
          <button
            type="button"
            className="entity-link"
            onClick={() =>
              navigate(ENTITY[hit.type as EntityKind].route(hit.id))
            }
          >
            {hit.title ?? hit.id}
          </button>
        ) : (
          <span>{hit.title ?? hit.id}</span>
        )}
        {hit.trust_tier === "authoritative" && (
          <Badge tone="success">{t("search.tier.authoritative")}</Badge>
        )}
      </div>
      {hit.snippet && <p className="search-hit-snippet">“{hit.snippet}”</p>}
      {typeof hit.score === "number" && (
        <span className="search-hit-score t-caption" title={t("search.why")}>
          {t("search.relevance", { pct: Math.round(hit.score * 100) })}
        </span>
      )}
    </li>
  );
}
