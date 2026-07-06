import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, SectionHeader } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// Tasks (B-EP09.12d): open tasks grouped overdue / today / upcoming / undated
// by due_at, with complete and snooze (+1 day) actions. Grouping compares
// UTC instants; the rendering localizes per user zone.

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

// One open task, with its complete / snooze actions. Extracted so the grouped
// render tree above stays legible instead of nesting these handlers deeply.
function TaskRow({
  task,
  overdue,
  onComplete,
  onSnooze,
}: Readonly<{
  task: Activity;
  overdue: boolean;
  onComplete: (id: string) => void;
  onSnooze: (task: Activity) => void;
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
  const query = useQuery({
    queryKey: ["tasks"],
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
      body: { is_done?: boolean; due_at?: string };
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

  return (
    <div className="wrap narrow">
      <SectionHeader title={t("nav.tasks")} />
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
