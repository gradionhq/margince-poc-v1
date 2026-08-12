import {
  type QueryKey,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useId } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, Modal, Skeleton } from "../design-system/atoms";
import { formatDate, formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import { RECORD_ZONE } from "./company360";
import { EntityRef } from "./entityref";

// Acting on a task from the record it belongs to. The tasks screen owns the
// standing work queue; this is the same two verbs (complete, snooze) offered
// where the rep already is, plus the detail a next-step row has no room for.
//
// Which cached reads a task write invalidates depends on where it was written
// from, so the caller passes them in — the work queue is workspace-wide, the
// record's timeline is not.

type Activity = components["schemas"]["Activity"];
type TaskPatch = {
  id: string;
  body: { is_done?: boolean; due_at?: string; remind_at?: string | null };
};

const ONE_DAY_MS = 86_400_000;

export function useTaskUpdate(invalidateKeys: readonly QueryKey[]) {
  const t = useT();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: TaskPatch) => {
      const { error } = await api.PATCH("/activities/{id}", {
        params: { path: { id: input.id } },
        body: input.body,
      });
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: (_data, input) => {
      for (const queryKey of invalidateKeys) {
        queryClient.invalidateQueries({ queryKey });
      }
      // The task's own detail read too, always: a modal open on the task that
      // was just completed would otherwise keep showing the old due date and
      // offering the verbs that no longer apply.
      queryClient.invalidateQueries({ queryKey: ["activity", input.id] });
    },
  });
}

/** The next due date one snooze away, or null for a task that has no date to move. */
export function snoozedDueAt(dueAt: string | null | undefined): string | null {
  if (!dueAt) {
    return null;
  }
  return new Date(new Date(dueAt).getTime() + ONE_DAY_MS).toISOString();
}

/**
 * TaskCompleteCheck is the tick affordance itself — a checkbox rather than a
 * labelled button, for a row that names its own verb (ticking IS completing)
 * rather than one that reads as a menu of actions. Every caller lists OPEN
 * tasks only, so the box always starts unchecked; ticking it fires the
 * completion and the row leaves the list on the invalidated re-read, rather
 * than the checkbox itself flipping to a done state it would then have to
 * keep showing.
 */
export function TaskCompleteCheck({
  activityId,
  update,
}: Readonly<{
  activityId: string;
  update: ReturnType<typeof useTaskUpdate>;
}>) {
  const t = useT();
  const isThisTask = update.variables?.id === activityId;
  const pending = update.isPending && isThisTask;
  // A rejected PATCH re-enables the box and leaves it unchecked — the same
  // rendering a click that did nothing would leave. Without this, the two are
  // indistinguishable and the reader has no reason to try again.
  const failed = update.isError && isThisTask;
  return (
    <>
      <input
        type="checkbox"
        className="co-task-check"
        checked={false}
        disabled={pending}
        aria-label={t("tasks.complete")}
        onChange={() =>
          update.mutate({ id: activityId, body: { is_done: true } })
        }
      />
      {failed && (
        <span className="co-part-error" role="alert">
          {problemMessageOf(update.error, t)}
        </span>
      )}
    </>
  );
}

/**
 * TaskQuickActions is the verb a rep needs on a next-step row beyond the tick:
 * snooze, offered only for a dated task, since there is no day to move a task
 * to that never had one. Complete lives on `TaskCompleteCheck` instead —
 * `showComplete` keeps it here too for the one caller (the detail modal) that
 * has no row-level checkbox of its own to tick.
 */
export function TaskQuickActions({
  activityId,
  dueAt,
  update,
  showComplete = true,
}: Readonly<{
  activityId: string;
  dueAt?: string | null;
  update: ReturnType<typeof useTaskUpdate>;
  showComplete?: boolean;
}>) {
  const t = useT();
  const nextDue = snoozedDueAt(dueAt);
  const pending = update.isPending && update.variables?.id === activityId;
  return (
    <>
      {showComplete && (
        <Button
          small
          variant="primary"
          disabled={pending}
          onClick={() =>
            update.mutate({ id: activityId, body: { is_done: true } })
          }
        >
          {t("tasks.complete")}
        </Button>
      )}
      {nextDue && (
        <Button
          small
          disabled={pending}
          onClick={() =>
            update.mutate({ id: activityId, body: { due_at: nextDue } })
          }
        >
          {t("tasks.snooze")}
        </Button>
      )}
    </>
  );
}

/**
 * TaskDetailModal opens one task where it was listed.
 *
 * A task has no screen of its own — it lives in a timeline, not on a record
 * page — so the detail comes to the reader rather than routing them away from
 * the account they are reading.
 */
export function TaskDetailModal({
  activityId,
  readOnly,
  onClose,
  update,
}: Readonly<{
  activityId: string;
  // An archived company takes no new activity, so the verbs below would only
  // be refused server-side — omitted here rather than disabled and left
  // visible.
  readOnly: boolean;
  onClose: () => void;
  update: ReturnType<typeof useTaskUpdate>;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const titleId = useId();
  const query = useQuery({
    queryKey: ["activity", activityId],
    queryFn: async () => {
      const { data, error } = await api.GET("/activities/{id}", {
        params: { path: { id: activityId } },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
  });
  const task: Activity | undefined = query.data;
  return (
    <Modal open onClose={onClose} labelledBy={titleId}>
      <h2 id={titleId} className="t-h2 modal-title">
        {task?.subject ?? t("tasks.detail")}
      </h2>
      {query.isPending && <Skeleton width="100%" height={48} />}
      {query.isError && (
        <p className="t-caption form-error">
          {problemMessageOf(query.error, t)}
        </p>
      )}
      {task && (
        <div className="form-stack">
          {task.body && <p className="t-body">{task.body}</p>}
          <p className="t-caption task-detail-meta">
            {task.due_at ? (
              <span>
                {t("co.next.due", {
                  when: formatDate(task.due_at, locale, RECORD_ZONE),
                })}
              </span>
            ) : (
              <span>{t("co.next.undated")}</span>
            )}
            <span>
              {t("tasks.logged")}{" "}
              {formatDateTime(task.occurred_at, locale, RECORD_ZONE)}
            </span>
            {task.is_done && <Badge tone="success">{t("tasks.isDone")}</Badge>}
            {task.assignee_id && (
              <EntityRef kind="user" id={task.assignee_id} />
            )}
          </p>
          {!task.is_done && !readOnly && (
            <div className="form-actions">
              <TaskQuickActions
                activityId={task.id}
                dueAt={task.due_at}
                update={update}
              />
            </div>
          )}
        </div>
      )}
    </Modal>
  );
}
