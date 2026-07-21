import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import {
  OverlayUnavailable,
  problemMessage,
  QueryGate,
  useSorMode,
} from "./common";
import { CreateRecordModal, NewRecordButton } from "./create";

// Tasks (B-EP09.12d): open tasks grouped overdue / today / upcoming / undated
// by due_at, with complete and snooze (+1 day) actions. Grouping compares
// UTC instants; the rendering localizes per user zone. B-E16.1 adds the
// reminder: remind_at rendered on the row, settable and clearable inline,
// plus the New-task create modal.

type Activity = components["schemas"]["Activity"];

export type TaskGroup = "overdue" | "today" | "upcoming" | "undated";

export function groupTask(task: Activity, now: Date): TaskGroup {
  if (!task.due_at) {
    return "undated";
  }
  const due = new Date(task.due_at);
  if (due.getTime() < now.getTime()) {
    return "overdue";
  }
  const sameDay =
    due.toISOString().slice(0, 10) === now.toISOString().slice(0, 10);
  return sameDay ? "today" : "upcoming";
}

const GROUP_ORDER: TaskGroup[] = ["overdue", "today", "upcoming", "undated"];

// The date picker yields a local calendar day; the task stays due until that
// day ends, so the wire instant is the local end of day (an instant at
// midnight would file a task picked "today" as overdue at breakfast).
function dueInstant(day: string): string {
  return new Date(`${day}T23:59:59`).toISOString();
}

// datetime-local yields zoneless local wall time; the wire wants UTC.
function reminderInstant(local: string): string {
  return new Date(local).toISOString();
}

// The inline reminder control: a bell + time (clearable) when remind_at is
// set, otherwise a bell button that unfolds a datetime picker. Extracted so
// TaskRow's flex line stays a readable list of affordances.
function ReminderControl({
  task,
  onSet,
}: Readonly<{
  task: Activity;
  onSet: (id: string, remindAt: string | null) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const [picking, setPicking] = useState(false);
  const [draft, setDraft] = useState("");

  if (task.remind_at) {
    return (
      <>
        <span
          className="t-caption"
          title={t("tasks.reminder")}
          style={{ display: "inline-flex", alignItems: "center", gap: 4 }}
        >
          <Bell aria-hidden style={{ width: 12, height: 12 }} />
          {formatDateTime(task.remind_at, locale, "Europe/Berlin")}
        </span>
        <Button small onClick={() => onSet(task.id, null)}>
          {t("tasks.clearReminder")}
        </Button>
      </>
    );
  }
  if (!picking) {
    return (
      <Button small onClick={() => setPicking(true)}>
        <Bell aria-hidden style={{ width: 12, height: 12 }} />{" "}
        {t("tasks.remind")}
      </Button>
    );
  }
  return (
    <>
      <TextInput
        type="datetime-local"
        aria-label={t("tasks.remindAt")}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        style={{ maxWidth: 200 }}
      />
      <Button
        small
        variant="primary"
        disabled={!draft}
        onClick={() => {
          onSet(task.id, reminderInstant(draft));
          setPicking(false);
          setDraft("");
        }}
      >
        {t("tasks.setReminder")}
      </Button>
    </>
  );
}

// One open task, with its complete / snooze / reminder actions. Extracted so
// the grouped render tree above stays legible instead of nesting these
// handlers deeply.
function TaskRow({
  task,
  overdue,
  onComplete,
  onSnooze,
  onRemind,
}: Readonly<{
  task: Activity;
  overdue: boolean;
  onComplete: (id: string) => void;
  onSnooze: (task: Activity) => void;
  onRemind: (id: string, remindAt: string | null) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <div className="card" style={{ marginBottom: 8 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
        <span style={{ flex: 1 }}>
          <strong>{task.subject ?? ""}</strong>
          {task.due_at && (
            <span className="t-caption">
              {" "}
              · {formatDateTime(task.due_at, locale, "Europe/Berlin")}
            </span>
          )}
        </span>
        {overdue && <Badge tone="danger">{t("tasks.overdue")}</Badge>}
        <ReminderControl task={task} onSet={onRemind} />
        <Button small variant="primary" onClick={() => onComplete(task.id)}>
          {t("tasks.complete")}
        </Button>
        {task.due_at && (
          <Button small onClick={() => onSnooze(task)}>
            {t("tasks.snooze")}
          </Button>
        )}
      </div>
    </div>
  );
}

export function TasksScreen() {
  const t = useT();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  // Tasks are activities filtered by kind=task — a defining filter the overlay
  // mirror cannot honor (422), and creating one would write to an incumbent
  // that owns the record. So overlay mode shows the honest unavailable state
  // (below) and skips the doomed fetch, rather than mislabeling every activity.
  const overlay = useSorMode() === "overlay";
  const query = useQuery({
    queryKey: ["tasks"],
    enabled: !overlay,
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: { query: { kind: "task", limit: 100 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const update = useMutation({
    mutationFn: async (input: {
      id: string;
      body: { is_done?: boolean; due_at?: string; remind_at?: string | null };
    }) => {
      const { error } = await api.PATCH("/activities/{id}", {
        params: { path: { id: input.id } },
        body: input.body,
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["tasks"] }),
  });

  // A task is created in place — there is no task 360 to land on, so the
  // shared CreateAction choreography (which navigates) does not fit; the
  // modal + refreshed list is the whole story.
  const create = useMutation({
    mutationFn: async (values: Record<string, string>) => {
      const { error } = await api.POST("/activities", {
        body: {
          kind: "task",
          subject: values.subject.trim(),
          due_at: values.due_date ? dueInstant(values.due_date) : null,
          remind_at: values.remind_at
            ? reminderInstant(values.remind_at)
            : null,
          source: "manual",
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["tasks"] });
      setCreating(false);
    },
  });

  const groupLabel: Record<TaskGroup, string> = {
    overdue: t("tasks.overdue"),
    today: t("tasks.today"),
    upcoming: t("tasks.upcoming"),
    undated: t("tasks.undated"),
  };

  const completeTask = (id: string) =>
    update.mutate({ id, body: { is_done: true } });

  const snoozeTask = (task: Activity) => {
    if (!task.due_at) {
      return;
    }
    const nextDue = new Date(
      new Date(task.due_at).getTime() + 86_400_000,
    ).toISOString();
    update.mutate({ id: task.id, body: { due_at: nextDue } });
  };

  const remindTask = (id: string, remindAt: string | null) =>
    update.mutate({ id, body: { remind_at: remindAt } });

  if (overlay) {
    return (
      <div className="wrap narrow">
        <div className="list-head">
          <SectionHeader title={t("nav.tasks")} />
        </div>
        <OverlayUnavailable />
      </div>
    );
  }

  return (
    <div className="wrap narrow">
      <div className="list-head">
        <SectionHeader title={t("nav.tasks")} />
        <NewRecordButton
          label={t("tasks.new")}
          onClick={() => setCreating(true)}
        />
      </div>
      <CreateRecordModal
        open={creating}
        onClose={() => setCreating(false)}
        title={t("tasks.new")}
        fields={[
          { key: "subject", label: "tasks.subject", required: true },
          { key: "due_date", label: "tasks.dueDate", type: "date" },
          { key: "remind_at", label: "tasks.remindAt", type: "datetime-local" },
        ]}
        pending={create.isPending}
        error={create.isError ? create.error.message : null}
        onSubmit={(values) => create.mutate(values)}
      />
      <QueryGate
        query={query}
        empty={(page) => page.data.filter((task) => !task.is_done).length === 0}
      >
        {(page) => {
          const now = new Date();
          const open = page.data.filter((task) => !task.is_done);
          return (
            <div>
              {GROUP_ORDER.map((group) => {
                const tasks = open.filter(
                  (task) => groupTask(task, now) === group,
                );
                if (tasks.length === 0) {
                  return null;
                }
                return (
                  <section key={group} aria-label={groupLabel[group]}>
                    <SectionHeader title={groupLabel[group]} />
                    {tasks.map((task) => (
                      <TaskRow
                        key={task.id}
                        task={task}
                        overdue={group === "overdue"}
                        onComplete={completeTask}
                        onSnooze={snoozeTask}
                        onRemind={remindTask}
                      />
                    ))}
                  </section>
                );
              })}
            </div>
          );
        }}
      </QueryGate>
    </div>
  );
}
