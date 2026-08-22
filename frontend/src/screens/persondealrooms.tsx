import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DoorOpen, UserX } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { navigate } from "../app/router";
import { Badge, Button } from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelRow } from "../design-system/panel";
import { useT } from "../i18n";
import { problemMessageOf, QueryStates, throwProblem } from "./common";
import { STATE_LABELS } from "./dealroom";
import { participantsKey } from "./dealroomaccess";

// The rooms a contact can still enter, on the contact's own page. An admin
// removing "Max, who left the buyer" knows the person, not the deals — the
// room page's Revoke is reachable only from a deal, so this is the path that
// starts from the name. Each row revokes through the same verb the room page
// uses, so there is one way to end a seat and it lives in one place.

type DealRoom = components["schemas"]["DealRoom"];

// The rooms a contact holds a seat in, keyed on the whole address list so a
// revoke anywhere refreshes it.
export function roomsOfKey(emails: readonly string[]) {
  return ["deal-rooms-of", [...emails].sort().join(",")] as const;
}

// More rooms than any one contact sits in; past this the card says the list
// is cut rather than pretending it is whole.
const ROOMS_LIMIT = 50;

export function PersonDealRooms({
  emails,
}: Readonly<{ emails: readonly string[] }>) {
  const t = useT();
  const rooms = useQuery({
    queryKey: roomsOfKey(emails),
    queryFn: async () => {
      const pages = await Promise.all(
        emails.map(async (email) => {
          const { data, error } = await api.GET("/deal-rooms", {
            params: { query: { participant_email: email, limit: ROOMS_LIMIT } },
          });
          if (error) {
            throwProblem(error);
          }
          return {
            email,
            rooms: data?.data ?? [],
            cut: data?.page.has_more ?? false,
          };
        }),
      );
      // One row per room, even when two of the contact's addresses sit in
      // it; the first address found is the one the revoke uses.
      const seen = new Map<string, { room: DealRoom; email: string }>();
      for (const page of pages) {
        for (const room of page.rooms) {
          if (!seen.has(room.id)) {
            seen.set(room.id, { room, email: page.email });
          }
        }
      }
      return { seats: [...seen.values()], cut: pages.some((p) => p.cut) };
    },
  });
  const seats = rooms.data?.seats ?? [];
  if (rooms.isSuccess && seats.length === 0) {
    return null;
  }
  return (
    <Panel title={t("persondealrooms.title")} sub={t("persondealrooms.sub")}>
      <QueryStates query={rooms} pendingLines={2}>
        {seats.map(({ room, email }) => (
          <RoomRow key={room.id} room={room} email={email} emails={emails} />
        ))}
        {rooms.data?.cut ? (
          <p className="t-small">{t("persondealrooms.cut")}</p>
        ) : null}
      </QueryStates>
    </Panel>
  );
}

function RoomRow({
  room,
  email,
  emails,
}: Readonly<{ room: DealRoom; email: string; emails: readonly string[] }>) {
  const t = useT();
  const mayManage = useCanWrite("deal_room", "update");
  const [confirming, setConfirming] = useState(false);
  return (
    <PanelRow className="person-room-row">
      <div>
        <p>{room.title}</p>
        <p className="t-small">{t(STATE_LABELS[room.state])}</p>
      </div>
      <div className="card-actions">
        <Button
          small
          variant="ghost"
          onClick={() =>
            navigate({ screen: "deals", id: room.deal_id, id2: "room" })
          }
        >
          <DoorOpen aria-hidden />
          {t("persondealrooms.open")}
        </Button>
        {mayManage ? (
          <Button small variant="ghost" onClick={() => setConfirming(true)}>
            <UserX aria-hidden />
            {t("access.revoke")}
          </Button>
        ) : null}
      </div>
      <RevokeSeat
        room={room}
        email={email}
        emails={emails}
        open={confirming}
        onClose={() => setConfirming(false)}
      />
    </PanelRow>
  );
}

// Revoking from the person's side: find the seat by address in the room's
// roster, then the same revoke the room page performs.
function RevokeSeat({
  room,
  email,
  emails,
  open,
  onClose,
}: Readonly<{
  room: DealRoom;
  email: string;
  emails: readonly string[];
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const revoke = useMutation({
    mutationFn: async (input: { roomId: string; email: string }) => {
      const roster = await api.GET("/deal-rooms/{id}/participants", {
        params: { path: { id: input.roomId } },
      });
      if (roster.error) {
        throwProblem(roster.error, t);
      }
      const seat = (roster.data?.data ?? []).find(
        (p) =>
          p.email.toLowerCase() === input.email.toLowerCase() && !p.revoked_at,
      );
      if (!seat) {
        throw new Error(t("persondealrooms.seatGone"));
      }
      const { error } = await api.POST(
        "/deal-rooms/{id}/participants/{participantId}/revoke",
        { params: { path: { id: input.roomId, participantId: seat.id } } },
      );
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: roomsOfKey(emails) });
      queryClient.invalidateQueries({ queryKey: participantsKey(room.id) });
      onClose();
    },
  });
  return (
    <ConfirmModal
      open={open}
      onClose={onClose}
      title={t("persondealrooms.revokeTitle", { room: room.title })}
      confirmLabel={t("access.revoke")}
      confirmVariant="danger"
      pending={revoke.isPending}
      error={revoke.isError ? problemMessageOf(revoke.error, t) : null}
      onConfirm={() => revoke.mutate({ roomId: room.id, email })}
    >
      <p>
        {email} <Badge>{t(STATE_LABELS[room.state])}</Badge>
      </p>
      <p className="t-small">{t("access.revokeBody")}</p>
    </ConfirmModal>
  );
}
