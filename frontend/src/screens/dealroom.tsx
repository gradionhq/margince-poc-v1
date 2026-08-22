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
import {
  Badge,
  Button,
  EmptyState,
  Field,
  TextInput,
} from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { Switch } from "../design-system/switch";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryStates, throwProblem } from "./common";

type DealRoom = components["schemas"]["DealRoom"];
type DealRoomTask = components["schemas"]["DealRoomTask"];

// The room states in which the list is still work rather than a record. It
// mirrors the store's own `publishable` rule; the server refuses regardless, so
// this exists to say WHY before the click rather than to enforce anything.
const FINISHED_STATES = new Set(["closed", "expired", "archived"]);

// Each room state's chip label, named as message keys so a state the contract
// adds fails the build here rather than rendering a bare machine word to a rep.
const STATE_LABELS: Record<string, MessageKey> = {
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
  const roomQuery = useQuery({
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

  const room = roomQuery.data?.data?.[0];
  if (roomQuery.isSuccess && !room) {
    return null;
  }
  return (
    <QueryStates query={roomQuery} pendingLines={4}>
      {room ? <DealRoomTasks room={room} /> : null}
    </QueryStates>
  );
}

function DealRoomTasks({ room }: Readonly<{ room: DealRoom }>) {
  const t = useT();
  const finished = FINISHED_STATES.has(String(room.state));
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

  const stateLabel = STATE_LABELS[String(room.state)];
  return (
    <Panel
      title={t("room.tasks.title")}
      sub={t("room.tasks.sub")}
      titleAction={
        stateLabel ? (
          <Badge>{t(stateLabel)}</Badge>
        ) : (
          <Badge>{room.state}</Badge>
        )
      }
    >
      <QueryStates query={tasksQuery} pendingLines={3}>
        {tasksQuery.data ? (
          <TaskList
            roomId={room.id}
            tasks={tasksQuery.data.data ?? []}
            finished={finished}
          />
        ) : null}
      </QueryStates>
      <PanelBody>
        <AddTask roomId={room.id} finished={finished} />
      </PanelBody>
    </Panel>
  );
}

function TaskList({
  roomId,
  tasks,
  finished,
}: Readonly<{ roomId: string; tasks: DealRoomTask[]; finished: boolean }>) {
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
        <TaskRow
          key={task.id}
          roomId={roomId}
          task={task}
          finished={finished}
        />
      ))}
    </>
  );
}

function TaskRow({
  roomId,
  task,
  finished,
}: Readonly<{ roomId: string; task: DealRoomTask; finished: boolean }>) {
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
        reason={finished ? t("room.tasks.finished") : undefined}
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
  finished,
}: Readonly<{ roomId: string; finished: boolean }>) {
  const t = useT();
  const [title, setTitle] = useState("");
  const [side, setSide] = useState<"seller" | "buyer">("buyer");
  const add = useAddTask(roomId);

  if (finished) {
    return <p className="t-small">{t("room.tasks.finished")}</p>;
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
      <div className="card-actions">
        <Button
          variant="ghost"
          small
          onClick={() => setSide(side === "buyer" ? "seller" : "buyer")}
        >
          {t(
            side === "buyer" ? "room.tasks.owedByBuyer" : "room.tasks.owedByUs",
          )}
        </Button>
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
    mutationFn: async (input: { title: string; side: "seller" | "buyer" }) => {
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
