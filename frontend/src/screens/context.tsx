// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import type { EntityKind } from "../app/entity";
import { EmptyState, SectionHeader } from "../design-system/atoms";
import { EvidenceChip, toEvidence } from "../design-system/trust";
import { useT } from "../i18n";
import { problemMessage, QueryGate, type QueryLike } from "./common";
import { EntityRef } from "./entityref";
import "./context.css";

// RS-3: the "Related evidence" context panel on a record 360 — the assembled
// context walk (anchor -> neighborhood) rendered as named sections of items,
// each carrying its provenance chip when the server attached one. Only the
// four routable record kinds render as an EntityRef link; `activity` (a valid
// ContextEntityRef.type but not a 360) renders as plain text, same convention
// as SearchHit in search.tsx.
type ContextResponse = components["schemas"]["ContextResponse"];
const LINKABLE = new Set<EntityKind>([
  "person",
  "organization",
  "deal",
  "lead",
]);

export function RecordContextPanel({
  entityType,
  id,
}: Readonly<{ entityType: EntityKind; id: string }>) {
  const t = useT();
  const query = useQuery({
    queryKey: ["record-context", entityType, id],
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
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  return (
    <section className="card record-context">
      <SectionHeader title={t("context.title")} />
      <QueryGate query={query as QueryLike<ContextResponse>}>
        {(data) =>
          data.sections.length === 0 ? (
            <EmptyState>{t("context.empty")}</EmptyState>
          ) : (
            <div className="context-sections">
              {data.sections.map((section) => (
                <div key={section.name} className="context-section">
                  <h3 className="t-label">{section.name}</h3>
                  <ul className="context-items">
                    {section.items.map((item) => {
                      const evidence = toEvidence(
                        item.evidence?.[0] as
                          | { [k: string]: unknown }
                          | undefined,
                      );
                      return (
                        <li
                          key={`${item.ref.type}:${item.ref.id}`}
                          className="context-item"
                        >
                          {LINKABLE.has(item.ref.type as EntityKind) ? (
                            <EntityRef
                              kind={item.ref.type as EntityKind}
                              id={item.ref.id}
                            />
                          ) : (
                            <span>{item.summary ?? item.ref.id}</span>
                          )}
                          {item.summary && (
                            <span className="t-caption">{item.summary}</span>
                          )}
                          {evidence && <EvidenceChip evidence={evidence} />}
                        </li>
                      );
                    })}
                  </ul>
                </div>
              ))}
            </div>
          )
        }
      </QueryGate>
    </section>
  );
}
