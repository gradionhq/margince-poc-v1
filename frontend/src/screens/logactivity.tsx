import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { EntityKind } from "../app/entity";
import { Button, SectionHeader, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";

// Log a note or task from a 360 (person/company/deal/lead): the contract's
// logActivity POST, linked to the record being viewed, occurred_at stamped
// at submit, source=manual. On success the screen's timeline query is
// invalidated so the fresh entry appears without a reload. Server-side
// validation is the truth — a 422 renders its RFC 7807 detail verbatim.

// Back-compat alias for pre-registry callers; exactly EntityKind now.
export type LinkedEntityType = EntityKind;

type ActivityDraft = {
  kind: "note" | "task";
  subject: string;
  body: string;
  // yyyy-mm-dd from the date input; only a task carries a due date.
  dueAt: string;
};

const EMPTY_DRAFT: ActivityDraft = {
  kind: "note",
  subject: "",
  body: "",
  dueAt: "",
};

export function LogActivity({
  entityType,
  entityId,
}: Readonly<{ entityType: LinkedEntityType; entityId: string }>) {
  const t = useT();
  const formId = useId();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<ActivityDraft>(EMPTY_DRAFT);

  const log = useMutation({
    mutationFn: async (input: ActivityDraft) => {
      const { data, error } = await api.POST("/activities", {
        body: {
          kind: input.kind,
          subject: input.subject.trim(),
          body: input.body.trim() || null,
          occurred_at: new Date().toISOString(),
          ...(input.kind === "task" && input.dueAt
            ? { due_at: new Date(input.dueAt).toISOString() }
            : {}),
          links: [{ entity_type: entityType, entity_id: entityId }],
          source: "manual",
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["activities", entityType, entityId],
      });
      setDraft(EMPTY_DRAFT);
    },
  });

  const setField = (patch: Partial<ActivityDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <SectionHeader title={t("log.title")} sub={t("log.sub")} />
      <form
        onSubmit={(event) => {
          event.preventDefault();
          log.mutate(draft);
        }}
        style={{ display: "flex", flexDirection: "column", gap: 10 }}
      >
        <div style={{ display: "flex", gap: 10 }}>
          <div className="field">
            <label className="t-label" htmlFor={`${formId}-kind`}>
              {t("log.kind")}
            </label>
            <select
              id={`${formId}-kind`}
              className="input"
              value={draft.kind}
              onChange={(event) =>
                setField({
                  kind: event.target.value === "task" ? "task" : "note",
                })
              }
            >
              <option value="note">{t("log.kindNote")}</option>
              <option value="task">{t("log.kindTask")}</option>
            </select>
          </div>
          {draft.kind === "task" && (
            <div className="field">
              <label className="t-label" htmlFor={`${formId}-due`}>
                {t("log.dueAt")}
              </label>
              <TextInput
                id={`${formId}-due`}
                type="date"
                value={draft.dueAt}
                onChange={(event) => setField({ dueAt: event.target.value })}
              />
            </div>
          )}
        </div>
        <div className="field">
          <label className="t-label" htmlFor={`${formId}-subject`}>
            {t("log.subject")} *
          </label>
          <TextInput
            id={`${formId}-subject`}
            value={draft.subject}
            required
            onChange={(event) => setField({ subject: event.target.value })}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={`${formId}-body`}>
            {t("log.body")}
          </label>
          <textarea
            id={`${formId}-body`}
            className="textarea"
            rows={3}
            value={draft.body}
            onChange={(event) => setField({ body: event.target.value })}
          />
        </div>
        {log.isError && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {log.error.message}
          </p>
        )}
        <div style={{ display: "flex", justifyContent: "flex-end" }}>
          <Button
            small
            variant="primary"
            type="submit"
            disabled={log.isPending || !draft.subject.trim()}
          >
            {log.isPending ? t("log.saving") : t("log.save")}
          </Button>
        </div>
      </form>
    </section>
  );
}
