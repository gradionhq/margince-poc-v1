// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { EntityKind } from "../app/entity";
import { entityTimelineKeys } from "../screens/activitykeys";
import { throwProblem, useSorMode } from "../screens/common";

/**
 * useRecordTimeline reads the activities linked to ONE record, for the
 * timeline zone of that record's page.
 *
 * It lives here rather than on a screen because four screens render a
 * timeline and each one that reached into a neighbour's file for it made that
 * neighbour a de-facto library — the way `SurfaceState` spent months
 * importable only from `company360.tsx`.
 *
 * The kind is `EntityKind`, not a hand-written union of the three that
 * happened to render a timeline first. `activity_link` has carried a lead arm
 * since migration 0038 and the contract's `entity_type` admits every kind the
 * link table does (ADR-0118/A169), so narrowing here would refuse a read the
 * server answers.
 */
export function useRecordTimeline(entityType: EntityKind, id: string) {
  // The timeline is an entity-scoped activity read, a dial the overlay mirror
  // refuses (422) — skip the fetch in overlay; the record page renders the
  // honest unavailable state in the timeline slot instead.
  const overlay = useSorMode() === "overlay";
  return useQuery({
    // The same key the write path invalidates. Spelling it a second time here
    // is how a logged note lands in the database and never appears on screen.
    queryKey: entityTimelineKeys(entityType, id)[0],
    enabled: !overlay,
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: {
          query: { entity_type: entityType, entity_id: id, limit: 20 },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}
