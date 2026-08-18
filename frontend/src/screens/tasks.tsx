import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  Card,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import {
  OverlayUnavailable,
  problemMessageOf,
  QueryGate,
  throwProblem,
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

// The calendar day an instant falls on, in a named zone. `en-CA` is the one
// locale whose short date is already ISO-ordered, so two of these compare as
// strings without a second parse.
function calendarDay(at: Date, zone: string): string {
  return at.toLocaleDateString("en-CA", { timeZone: zone });
}

// Which bucket a task belongs in, decided in the READER's zone.
//
// It used to compare UTC calendar days while `dueInstant` below mints the wire
// instant from LOCAL wall time — the two disagreed for any reader west of UTC.
// Pick "today" in UTC-5 and the instant is today 23:59:59 local, which is
// tomorrow in UTC, so the task a reader had just filed for today appeared under
// Upcoming. The zone has to be the same one on both sides of that comparison.
export function groupTask(task: Activity, now: Date, zone: string): TaskGroup {
  if (!task.due_at) {
    return "undated";
  }
  const due = new Date(task.due_at);
  if (due.getTime() < now.getTime()) {
    return "overdue";
  }
  return calendarDay(due, zone) === calendarDay(now, zone)
    ? "today"
    : "upcoming";
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
          {formatDateTime(
            task.remind_at,
            locale,
            Intl.DateTimeFormat().resolvedOptions().timeZone,
          )}
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

// A row does NOT name the record its task is about, and that is a gap rather
// than a choice: subjects are generated ("Follow up with the new lead"), so a
// queue of them is a column of the same sentence. The activity carries `links[]`
// with the record's id but no display name, and there is no batch-by-ids read in
// the contract — so naming them costs one request per row, up to the query's
// hundred. That is the same missing field the contacts list needs for an
// employer column: a list read should carry the linked record's display name,
// which is a contract change first (P3).
//
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
  // A due date is a PERSONAL deadline, so it reads in the viewer's own zone —
  // as does the reminder time above it, or one row would state two zones. Pinned
  // to Europe/Berlin it told a reader in another zone a different day than the
  // one their task is actually due on.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return (
    <Card as="div" style={{ marginBottom: "var(--space-2)" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
        <span style={{ flex: 1 }}>
          <strong>{task.subject ?? ""}</strong>
          {task.due_at && (
            <span className="t-caption">
              {" "}
              · {formatDateTime(task.due_at, locale, zone)}
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
    </Card>
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
        throwProblem(error, t);
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
        throwProblem(error, t);
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
        throwProblem(error, t);
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
      <div className="wrap">
        <OverlayUnavailable />
      </div>
    );
  }

  return (
    <div className="wrap">
      <div className="list-head">
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
        error={create.isError ? problemMessageOf(create.error, t) : null}
        onSubmit={(values) => create.mutate(values)}
      />
      <QueryGate
        query={query}
        empty={(page) => page.data.filter((task) => !task.is_done).length === 0}
      >
        {(page) => {
          const now = new Date();
          // The same zone the rows print their dates in, so a task filed for
          // today cannot appear under Upcoming.
          const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
          const open = page.data.filter((task) => !task.is_done);
          return (
            <div>
              {GROUP_ORDER.map((group) => {
                const tasks = open.filter(
                  (task) => groupTask(task, now, zone) === group,
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
