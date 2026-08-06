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
 * TaskQuickActions is the pair of verbs a rep needs on a next-step row.
 * Snooze is offered only for a dated task: there is no day to move a task to
 * that never had one.
 */
export function TaskQuickActions({
  activityId,
  dueAt,
  update,
}: Readonly<{
  activityId: string;
  dueAt?: string | null;
  update: ReturnType<typeof useTaskUpdate>;
}>) {
  const t = useT();
  const nextDue = snoozedDueAt(dueAt);
  const pending = update.isPending && update.variables?.id === activityId;
  return (
    <>
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
  onClose,
  update,
}: Readonly<{
  activityId: string;
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
          {!task.is_done && (
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
