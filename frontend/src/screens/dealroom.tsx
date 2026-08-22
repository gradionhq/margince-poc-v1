// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The seller's view of a deal's Deal Room, in the deal page's aside: the
// room's state, the documents the buyer can read, and the conversation both
// sides hold about them.
//
// Documents are EDITORIAL: a change reaches the buyer at the next publish,
// exactly like the room's welcome text, so the panel says so rather than
// implying the buyer sees it now. Comments are LIVE. A room that can no longer
// reach a buyer refuses both, and the refusal is a sentence on the control
// rather than a failed request after the click.

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Badge } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { QueryStates, throwProblem } from "./common";
import { DealRoomConversation } from "./dealroomconversation";
import { DealRoomDocuments } from "./dealroomdocuments";

type DealRoomState = components["schemas"]["DealRoomState"];

// The room states in which the room still takes content rather than being a record. It
// mirrors the store's own `publishable` rule; the server refuses regardless, so
// this exists to say WHY before the click rather than to enforce anything.
const FINISHED_STATES: ReadonlySet<DealRoomState> = new Set([
  "closed",
  "expired",
  "archived",
]);

// Each room state's chip label. Keyed by the contract's own closed union rather
// than by string, so a state the contract adds fails the typecheck here — a
// Record<string, …> would compile and render the bare machine word to a rep.
const STATE_LABELS: Record<DealRoomState, MessageKey> = {
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
 * The deal page's Deal Room aside. Renders nothing at all when the deal has no
 * room: an empty card inviting a rep to open one belongs to the publish slice,
 * and a card that only ever says "none" is furniture.
 */
export function DealRoomAside({ dealId }: Readonly<{ dealId: string }>) {
  const t = useT();
  const mayWrite = useCanWrite("deal_room", "update");
  const roomQuery = useDealRoom(dealId);

  const room = roomQuery.data?.data?.[0];
  if (roomQuery.isSuccess && !room) {
    return null;
  }
  return (
    <QueryStates query={roomQuery} pendingLines={4}>
      {room ? (
        <>
          <DealRoomDocuments
            room={room}
            state={<Badge>{t(STATE_LABELS[room.state])}</Badge>}
            refusal={refusalFor(FINISHED_STATES.has(room.state), mayWrite, t)}
          />
          <DealRoomConversation
            room={room}
            refusal={refusalFor(FINISHED_STATES.has(room.state), mayWrite, t)}
          />
        </>
      ) : null}
    </QueryStates>
  );
}

/**
 * Whether this deal has a Deal Room, for a caller deciding whether to give the
 * aside a slot at all.
 *
 * The slot has to be decided OUTSIDE this component, because a React element
 * is truthy whatever it renders: passing `aside={<DealRoomAside …/>}` reserves
 * the aside column and its landmark even on the deals where the component then
 * renders nothing, leaving the page's story narrower for no content. Sharing
 * the query key means the caller's question costs no second request.
 */
export function useDealRoomPresence(dealId: string, enabled = true): boolean {
  const roomQuery = useDealRoom(dealId, enabled);
  return (roomQuery.data?.data?.length ?? 0) > 0;
}

function useDealRoom(dealId: string, enabled = true) {
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
function refusalFor(
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
