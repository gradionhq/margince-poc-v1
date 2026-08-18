// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ENTITY_KINDS, type EntityKind } from "../app/entity";
import { Card, EmptyState } from "../design-system/atoms";
import { EvidenceChip, toEvidence } from "../design-system/trust";
import { useT } from "../i18n";
import {
  OverlayUnavailable,
  QueryGate,
  type QueryLike,
  throwProblem,
  useSorMode,
} from "./common";
import { EntityRef } from "./entityref";
import "./context.css";

// RS-3: the "Related evidence" context panel on a record 360 — the assembled
// context walk (anchor -> neighborhood) rendered as named sections of items,
// each carrying its provenance chip when the server attached one. Only the
// four routable record kinds render as an EntityRef link; `activity` (a valid
// ContextEntityRef.type but not a 360) renders as plain text, same convention
// as SearchHit in search.tsx.
type ContextResponse = components["schemas"]["ContextResponse"];
const LINKABLE = new Set<EntityKind>(ENTITY_KINDS);

// The walk starts at the anchor, so the anchor comes back inside it — and a row
// linking a reader to the page they are standing on carries nothing. Dropping it
// can empty a section, and an empty section is a heading over nothing, so the
// section goes with its last item.
function neighbourhood(
  sections: ReadonlyArray<ContextResponse["sections"][number]>,
  anchor: string,
): ReadonlyArray<ContextResponse["sections"][number]> {
  return sections
    .map((section) => ({
      ...section,
      items: section.items.filter(
        (item) => `${item.ref.type}:${item.ref.id}` !== anchor,
      ),
    }))
    .filter((section) => section.items.length > 0);
}

export function RecordContextPanel({
  entityType,
  id,
}: Readonly<{ entityType: EntityKind; id: string }>) {
  const t = useT();
  // The context walk is assembled from the context graph / embeddings, which
  // branch 1 builds no nodes for over mirror content (the endpoint 404s in
  // overlay) — an honest unavailable state, not an error, and no doomed fetch.
  const overlay = useSorMode() === "overlay";
  const query = useQuery({
    queryKey: ["record-context", entityType, id],
    enabled: !overlay,
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/records/{entity_type}/{id}/context",
        {
          params: {
            path: { entity_type: entityType, id },
            query: { max_items: 5 },
          },
        },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  if (overlay) {
    return (
      <Card className="record-context" title={t("context.title")}>
        <OverlayUnavailable />
      </Card>
    );
  }

  return (
    <Card className="record-context" title={t("context.title")}>
      <QueryGate query={query as QueryLike<ContextResponse>}>
        {(data) => {
          const sections = neighbourhood(
            data.sections ?? [],
            `${entityType}:${id}`,
          );
          return sections.length === 0 ? (
            <EmptyState>{t("context.empty")}</EmptyState>
          ) : (
            <div className="context-sections">
              {sections.map((section) => (
                <div key={section.name} className="context-section">
                  <h3 className="t-label">{section.name}</h3>
                  <ul className="context-items">
                    {section.items.map((item) => {
                      const self = `${item.ref.type}:${item.ref.id}`;
                      const evidenceList = (item.evidence ?? [])
                        .map((entry) => toEvidence(entry))
                        .filter((entry) => entry != null)
                        // A record is not evidence for itself. The walk cites
                        // every item by its own ref, which the model needs — an
                        // item with no citation trips its no-guess gate — and a
                        // reader does not: a chip proving "Anna Weber" with
                        // "Anna Weber" is the same claim twice.
                        .filter((entry) => entry.source !== self);
                      return (
                        <li
                          key={`${item.ref.type}:${item.ref.id}`}
                          className="context-item"
                        >
                          {LINKABLE.has(item.ref.type as EntityKind) ? (
                            <>
                              <EntityRef
                                kind={item.ref.type as EntityKind}
                                id={item.ref.id}
                              />
                              {item.summary && (
                                <span className="t-caption">
                                  {item.summary}
                                </span>
                              )}
                            </>
                          ) : (
                            // A kind with no record page of its own has
                            // nothing to link to, and its id is not a reading:
                            // it names the kind, which is what a reader can
                            // actually do something with. The ref stays on
                            // `title` for whoever is debugging a walk.
                            <span title={`${item.ref.type}:${item.ref.id}`}>
                              {item.summary ?? item.ref.type}
                            </span>
                          )}
                          {evidenceList.map((evidence) => (
                            <EvidenceChip
                              key={`${evidence.source}:${evidence.snippet}`}
                              evidence={evidence}
                            />
                          ))}
                        </li>
                      );
                    })}
                  </ul>
                </div>
              ))}
            </div>
          );
        }}
      </QueryGate>
    </Card>
  );
}
