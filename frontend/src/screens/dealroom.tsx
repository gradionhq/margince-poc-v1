// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The seller's view of a deal's Deal Room, in the deal page's aside.
//
// It shows the room's state and the shared to-do list — the work outstanding
// between the two sides, each item owed by one of them. A rep adds items and
// either side ticks them off.
//
// TWO KINDS OF CHANGE, FROZEN DIFFERENTLY, and the surface has to make that
// visible or a rep will not believe it. Adding or rewording an item is
// EDITORIAL: it reaches the buyer at the next publish, exactly like the room's
// welcome text, so the panel says so rather than implying the buyer sees it
// now. Ticking an item off is LIVE — no publish, both sides, immediately. A
// room that can no longer reach a buyer refuses both, and the refusal is a
// sentence on the control rather than a failed request after the click.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { useCanWrite } from "../app/capability";
import {
  Badge,
  Button,
  EmptyState,
  Field,
  SegmentedControl,
  TextInput,
} from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { Switch } from "../design-system/switch";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryStates, throwProblem } from "./common";
import { DealRoomConversation } from "./dealroomconversation";
import { DealRoomDocuments } from "./dealroomdocuments";

type DealRoom = components["schemas"]["DealRoom"];
type DealRoomTask = components["schemas"]["DealRoomTask"];
type DealRoomState = components["schemas"]["DealRoomState"];

// The room states in which the list is still work rather than a record. It
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
// The two sides an item can be owed by, as the segmented control reads them.
const SIDES = ["buyer", "seller"] as const;

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
          <DealRoomTasks room={room} />
          <DealRoomDocuments
            room={room}
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

function DealRoomTasks({ room }: Readonly<{ room: DealRoom }>) {
  const t = useT();
  // Two separate reasons a control refuses, folded into one answer because a
  // reader only ever needs the first true one. A finished room refuses
  // everybody; a read-only seat refuses this reader in every room. Without the
  // second, a reader who may not write sees live controls whose every click
  // comes back 403 — the server holds the line, but only after the click.
  const mayWrite = useCanWrite("deal_room", "update");
  const finished = FINISHED_STATES.has(room.state);
  const tasksQuery = useQuery({
    queryKey: ["deal-room-tasks", room.id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deal-rooms/{id}/tasks", {
        params: { path: { id: room.id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  return (
    <Panel
      title={t("room.tasks.title")}
      sub={t("room.tasks.sub")}
      titleAction={<Badge>{t(STATE_LABELS[room.state])}</Badge>}
    >
      <QueryStates query={tasksQuery} pendingLines={3}>
        {tasksQuery.data ? (
          <TaskList
            roomId={room.id}
            tasks={tasksQuery.data.data ?? []}
            refusal={refusalFor(finished, mayWrite, t)}
          />
        ) : null}
      </QueryStates>
      <PanelBody>
        <AddTask roomId={room.id} refusal={refusalFor(finished, mayWrite, t)} />
      </PanelBody>
    </Panel>
  );
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
    return t("room.tasks.finished");
  }
  if (!mayWrite) {
    return t("room.tasks.readOnly");
  }
  return undefined;
}

function TaskList({
  roomId,
  tasks,
  refusal,
}: Readonly<{
  roomId: string;
  tasks: DealRoomTask[];
  refusal: string | undefined;
}>) {
  const t = useT();
  if (tasks.length === 0) {
    return (
      <PanelBody>
        <EmptyState>
          <p className="t-small">{t("room.tasks.empty")}</p>
        </EmptyState>
      </PanelBody>
    );
  }
  return (
    <>
      {tasks.map((task) => (
        <TaskRow key={task.id} roomId={roomId} task={task} refusal={refusal} />
      ))}
    </>
  );
}

function TaskRow({
  roomId,
  task,
  refusal,
}: Readonly<{
  roomId: string;
  task: DealRoomTask;
  refusal: string | undefined;
}>) {
  const t = useT();
  const toggle = useToggleTask(roomId);
  // A row CONTAINS a control rather than being one, so it is not interactive:
  // the switch draws its own hover, and a fill behind it would claim a hit area
  // the row does not have.
  return (
    <PanelRow>
      <Switch
        label={task.title}
        hint={t(
          task.side === "buyer"
            ? "room.tasks.owedByBuyer"
            : "room.tasks.owedByUs",
        )}
        checked={task.done}
        pending={toggle.isPending}
        reason={refusal}
        onChange={(done) =>
          toggle.mutate({ taskId: task.id, done, version: task.version })
        }
        testId={`room-task-${task.id}`}
      />
      {toggle.isError ? (
        <p className="t-small t-danger">
          {problemMessageOf(toggle.error, t, t("room.tasks.toggleFailed"))}
        </p>
      ) : null}
    </PanelRow>
  );
}

function AddTask({
  roomId,
  refusal,
}: Readonly<{ roomId: string; refusal: string | undefined }>) {
  const t = useT();
  const [title, setTitle] = useState("");
  const [side, setSide] = useState<(typeof SIDES)[number]>("buyer");
  const add = useAddTask(roomId);

  if (refusal !== undefined) {
    return <p className="t-small">{refusal}</p>;
  }
  const submit = () => {
    const wording = title.trim();
    if (wording === "") {
      return;
    }
    add.mutate({ title: wording, side }, { onSuccess: () => setTitle("") });
  };
  return (
    <>
      <Field label={t("room.tasks.newLabel")}>
        {(control) => (
          <TextInput
            {...control}
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                submit();
              }
            }}
            data-testid="room-task-new"
          />
        )}
      </Field>
      {/* Both sides stay visible rather than one hiding behind a flip: a
          button reading "the buyer owes this" does not say whether that is the
          current choice or the one a press would make. */}
      <SegmentedControl
        options={SIDES}
        value={side}
        onChange={setSide}
        label={t("room.tasks.sideLabel")}
        labels={{
          buyer: t("room.tasks.owedByBuyer"),
          seller: t("room.tasks.owedByUs"),
        }}
      />
      <div className="card-actions">
        <Button
          small
          onClick={submit}
          disabled={title.trim() === ""}
          pending={add.isPending}
          busyLabel={t("room.tasks.adding")}
        >
          {t("room.tasks.add")}
        </Button>
      </div>
      {/* Said on every add rather than once in the header: a rep who scrolls
          past the sub line still has to know the buyer does not see this yet. */}
      <p className="t-small">{t("room.tasks.editorial")}</p>
      {add.isError ? (
        <p className="t-small t-danger">
          {problemMessageOf(add.error, t, t("room.tasks.addFailed"))}
        </p>
      ) : null}
    </>
  );
}

function useToggleTask(roomId: string) {
  const t = useT();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: ["deal-room-task-toggle"],
    mutationFn: async (input: {
      taskId: string;
      done: boolean;
      version: number | undefined;
    }) => {
      const { data, error } = await api.PATCH(
        "/deal-rooms/{id}/tasks/{taskId}",
        {
          params: {
            path: { id: roomId, taskId: input.taskId },
            ...ifMatch(requireVersion(input.version)),
          },
          body: { done: input.done },
        },
      );
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["deal-room-tasks", roomId] });
    },
  });
}

function useAddTask(roomId: string) {
  const t = useT();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: ["deal-room-task-add"],
    mutationFn: async (input: {
      title: string;
      side: (typeof SIDES)[number];
    }) => {
      const { data, error } = await api.POST("/deal-rooms/{id}/tasks", {
        params: { path: { id: roomId } },
        body: { title: input.title, side: input.side, source: "manual" },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["deal-room-tasks", roomId] });
    },
  });
}
