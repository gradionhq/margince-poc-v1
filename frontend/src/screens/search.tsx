// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ENTITY, ENTITY_KINDS, type EntityKind } from "../app/entity";
import { navigate } from "../app/router";
import { Badge, Card, EmptyState, SearchField } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { QueryGate, type QueryLike, throwProblem } from "./common";
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
        throwProblem(error);
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
      <form onSubmit={submit} className="search-bar">
        <SearchField
          value={draft}
          placeholder={t("search.placeholder")}
          aria-label={t("search.placeholder")}
          onChange={(event) => setDraft(event.target.value)}
        />
      </form>
      {/* No term, no read — and therefore no gate. A query held back by `enabled`
          reports `pending` in react-query v5, so handing it to QueryGate drew
          three loading bars under this box forever, with nothing in flight: a
          bare `#/search` is reachable from the address bar, and the page it
          showed said "working on it" about a request that had not been made.
          The screen asks for a term instead. */}
      {q.trim() === "" ? (
        <EmptyState>{t("search.prompt")}</EmptyState>
      ) : (
        <QueryGate query={query as QueryLike<SearchResponse>}>
          {(data) =>
            data.data.length === 0 ? (
              <EmptyState>{t("search.empty", { q })}</EmptyState>
            ) : (
              <SearchGroups results={data.data} />
            )
          }
        </QueryGate>
      )}
    </div>
  );
}

function SearchGroups({ results }: Readonly<{ results: SearchResult[] }>) {
  const t = useT();
  return (
    <div className="search-groups arrive-stack">
      {GROUP_ORDER.filter((type) => results.some((r) => r.type === type)).map(
        (type) => (
          <Card key={type} className="search-group">
            <h2 className="t-label">{t(GROUP_KEY[type])}</h2>
            <ul className="search-hits">
              {results
                .filter((r) => r.type === type)
                .map((hit) => (
                  <SearchHit key={`${hit.type}:${hit.id}`} hit={hit} />
                ))}
            </ul>
          </Card>
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
        {/* Only a tier the reader would not otherwise assume. In native mode
            every stored record is `authoritative` (contract: external and
            unverified are reserved for overlay/connector rows), so badging it
            put the same green pill on every hit on the page — a mark that
            never varies marks nothing, and it crowded out the one that does.
            `unverified` is the opposite case and keeps its badge: it is rare by
            the same contract, and a tier the record CARRIES while the page draws
            nothing reads as a record with nothing to declare.

            Neither badge names the SYSTEM a row came from. `external` covers
            every overlay- and connector-sourced row and the hit carries no
            provider field, so one vendor's name would be stamped on rows
            mirrored from any other. */}
        {hit.trust_tier === "external" && (
          <Badge tone="accent">{t("search.tier.mirrored")}</Badge>
        )}
        {hit.trust_tier === "unverified" && (
          <Badge tone="warn">{t("search.tier.unverified")}</Badge>
        )}
      </div>
      {/* `hit.score` is deliberately not drawn. The contract bounds it to
          nothing (schema: "Relevance score"), so the retriever's raw figure
          reached the page as "relevance 280%" — a percentage of nothing, which
          a reader can neither act on nor disbelieve. It still does its job in
          the ordering the results arrive in. */}
      {hit.snippet && <p className="search-hit-snippet">“{hit.snippet}”</p>}
    </li>
  );
}
