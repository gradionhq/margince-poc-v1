// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The deal page's Deal Room card, and the door to the room: one card saying
// the room's state, who is in it, whether the buyer has seen the latest
// changes and what came back from their side — with the page one click away.
// A deal without a room gets the one control that opens one.
//
// The room itself lives on its own page (dealroompage.tsx). The card never
// edits anything: a rep who wants to change the room goes where the whole of
// it is visible, rather than half-editing it from the deal's margin.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DoorOpen } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { navigate } from "../app/router";
import { Badge, Button, Field, TextInput } from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryStates, throwProblem } from "./common";
import { useParticipants } from "./dealroomaccess";
import { useRoomChanges } from "./dealroompage";

type DealRoom = components["schemas"]["DealRoom"];
type DealRoomState = components["schemas"]["DealRoomState"];

// The room states in which the room still takes content rather than being a
// record. It mirrors the store's own `publishable` rule; the server refuses
// regardless, so this exists to say WHY before the click rather than to
// enforce anything.
export const FINISHED_STATES: ReadonlySet<DealRoomState> = new Set([
  "closed",
  "expired",
  "archived",
]);

// Each room state's chip label. Keyed by the contract's own closed union rather
// than by string, so a state the contract adds fails the typecheck here — a
// Record<string, …> would compile and render the bare machine word to a rep.
export const STATE_LABELS: Record<DealRoomState, MessageKey> = {
  draft: "room.state.draft",
  building: "room.state.building",
  ready: "room.state.ready",
  publishing: "room.state.publishing",
  live: "room.state.live",
  paused: "room.state.paused",
  closed: "room.state.closed",
  expired: "room.state.expired",
  archived: "room.state.archived",
};

/**
 * The deal page's Deal Room card. A deal with no room gets the one control
 * that opens one; a deal with a room gets its summary and the way in.
 */
export function DealRoomAside({
  dealId,
  dealName,
}: Readonly<{ dealId: string; dealName: string }>) {
  const roomQuery = useDealRoom(dealId);
  const room = roomQuery.data?.data?.[0];
  return (
    <QueryStates query={roomQuery} pendingLines={3}>
      {room ? (
        <RoomCard room={room} />
      ) : roomQuery.isSuccess ? (
        <OpenRoomCard dealId={dealId} dealName={dealName} />
      ) : null}
    </QueryStates>
  );
}

function RoomCard({ room }: Readonly<{ room: DealRoom }>) {
  const t = useT();
  const participants = useParticipants(room.id);
  const changes = useRoomChanges(room.id, !FINISHED_STATES.has(room.state));
  const rows = participants.data?.data ?? [];
  const invited = rows.filter((p) => !p.revoked_at).length;
  const active = rows.filter((p) => !p.revoked_at && p.has_signed_in).length;
  const lastSeen = rows
    .map((p) => p.last_seen_at)
    .filter((v): v is string => typeof v === "string")
    .sort()
    .at(-1);
  return (
    <Panel
      title={t("room.card.title")}
      titleAction={<Badge>{t(STATE_LABELS[room.state])}</Badge>}
    >
      <PanelBody>
        <p>{room.title}</p>
        <p className="t-small">
          {t("room.card.people", {
            invited: String(invited),
            active: String(active),
          })}
          {changes.data?.has_changes ? ` · ${t("room.card.unpublished")}` : ""}
        </p>
        {lastSeen ? (
          <p className="t-small">
            {t("room.card.lastSeen", { when: lastSeen.slice(0, 10) })}
          </p>
        ) : null}
        <div className="card-actions">
          <Button
            small
            onClick={() =>
              navigate({ screen: "deals", id: room.deal_id, id2: "room" })
            }
          >
            <DoorOpen aria-hidden />
            {t("room.card.open")}
          </Button>
        </div>
      </PanelBody>
    </Panel>
  );
}

function OpenRoomCard({
  dealId,
  dealName,
}: Readonly<{ dealId: string; dealName: string }>) {
  const t = useT();
  const mayCreate = useCanWrite("deal_room", "create");
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState(
    t("room.create.defaultTitle", { deal: dealName }),
  );
  const create = useMutation({
    mutationFn: async (roomTitle: string) => {
      const { data, error } = await api.POST("/deal-rooms", {
        body: { deal_id: dealId, title: roomTitle, source: "ui" },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["deal-rooms", dealId] });
      setCreating(false);
      navigate({ screen: "deals", id: dealId, id2: "room" });
    },
  });
  if (!mayCreate) {
    return null;
  }
  return (
    <Panel title={t("room.card.title")} sub={t("room.create.sub")}>
      <PanelBody>
        <div className="card-actions">
          <Button small onClick={() => setCreating(true)}>
            <DoorOpen aria-hidden />
            {t("room.create.open")}
          </Button>
        </div>
      </PanelBody>
      <ConfirmModal
        open={creating}
        onClose={() => setCreating(false)}
        title={t("room.create.open")}
        confirmLabel={t("room.create.confirm")}
        confirmDisabled={title.trim() === ""}
        pending={create.isPending}
        error={create.isError ? problemMessageOf(create.error, t) : null}
        onConfirm={() => create.mutate(title.trim())}
      >
        <Field
          label={t("room.create.titleLabel")}
          hint={t("room.create.titleHint")}
        >
          {(control) => (
            <TextInput
              {...control}
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
          )}
        </Field>
      </ConfirmModal>
    </Panel>
  );
}

/**
 * Whether this deal has a Deal Room, for a caller deciding whether to give the
 * aside a slot at all. Sharing the query key means the caller's question costs
 * no second request.
 */
export function useDealRoomPresence(dealId: string, enabled = true): boolean {
  const roomQuery = useDealRoom(dealId, enabled);
  return (roomQuery.data?.data?.length ?? 0) > 0;
}

export function useDealRoom(dealId: string, enabled = true) {
  return useQuery({
    // Off in overlay mode, where the deal is a mirror from another system of
    // record and its sub-resources answer 422.
    enabled,
    queryKey: ["deal-rooms", dealId],
    queryFn: async () => {
      const { data, error } = await api.GET("/deal-rooms", {
        params: { query: { deal_id: dealId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

// The sentence a control states instead of accepting a change, or undefined
// when this reader may make it. One function so the row and the form cannot
// disagree about whether a change is possible.
export function refusalFor(
  finished: boolean,
  mayWrite: boolean,
  t: ReturnType<typeof useT>,
): string | undefined {
  if (finished) {
    return t("room.finished");
  }
  if (!mayWrite) {
    return t("room.readOnly");
  }
  return undefined;
}
