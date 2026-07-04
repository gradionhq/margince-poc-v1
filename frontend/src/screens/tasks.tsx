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

export function TasksScreen() {
  const t = useT();
  const { locale } = useLocale();
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
                      <div
                        key={task.id}
                        className="card"
                        style={{ marginBottom: 8 }}
                      >
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: 10,
                          }}
                        >
                          <span style={{ flex: 1 }}>
                            <strong>{task.subject ?? ""}</strong>
                            {task.due_at && (
                              <span className="t-caption">
                                {" "}
                                ·{" "}
                                {formatDateTime(
                                  task.due_at,
                                  locale,
                                  "Europe/Berlin",
                                )}
                              </span>
                            )}
                          </span>
                          {group === "overdue" && (
                            <Badge tone="danger">{groupLabel.overdue}</Badge>
                          )}
                          <Button
                            small
                            variant="primary"
                            onClick={() =>
                              update.mutate({
                                id: task.id,
                                body: { is_done: true },
                              })
                            }
                          >
                            {t("tasks.complete")}
                          </Button>
                          {task.due_at && (
                            <Button
                              small
                              onClick={() =>
                                update.mutate({
                                  id: task.id,
                                  body: {
                                    due_at: new Date(
                                      new Date(
                                        task.due_at as string,
                                      ).getTime() + 86_400_000,
                                    ).toISOString(),
                                  },
                                })
                              }
                            >
                              {t("tasks.snooze")}
                            </Button>
                          )}
                        </div>
                      </div>
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
