import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { EntityKind } from "../app/entity";
import {
  Button,
  Card,
  Checkbox,
  Field,
  Modal,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { Select } from "../design-system/select";
import { calendarDay, dueInstant } from "../format/calendarday";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { entityTimelineKeys, taskWriteKeys } from "./activitykeys";
import { problemMessageOf, throwProblem, useSorMode } from "./common";

// Log a note or task from a 360 (person/company/deal/lead): the contract's
// logActivity POST, linked to the record being viewed, occurred_at stamped
// at submit, source=manual. On success every read that renders this record's
// timeline is invalidated (see activitykeys) so the fresh entry appears
// without a reload. Server-side validation is the truth — a 422 renders its
// RFC 7807 detail verbatim.

type ActivityDraft = {
  kind: "note" | "task" | "meeting";
  subject: string;
  body: string;
  // yyyy-mm-dd from the date input; only a task carries a due date.
  dueAt: string;
  // A meeting's body is ordinary notes UNLESS this is explicitly checked —
  // otherwise "discussed pricing, follow up Tuesday" typed while logging a
  // meeting would silently carry source_system: transcript, which the
  // backend documents as meaning pasted/uploaded transcript TEXT and which
  // the activity/transcript retention scope sweeps on a different schedule
  // than an ordinary meeting note. Meaningless outside kind: meeting.
  asTranscript: boolean;
};

const EMPTY_DRAFT: ActivityDraft = {
  kind: "note",
  subject: "",
  body: "",
  dueAt: "",
  asTranscript: false,
};

// Only a plain-text paste round-trips through normalizeTranscript's line
// splitting the way ADR-0058's line-addressing promises: a `.vtt` file's cue
// timestamps and header would themselves become "transcript lines", pointing
// any future line citation at a timestamp instead of what was said.
const ACCEPTED_TRANSCRIPT_EXTENSION = ".txt";

// The writer's own calendar day. A task's due date starts here the moment the
// kind becomes task — the day that WOULD apply is shown in the field instead
// of being assumed at submit behind an empty box.
function todayDay(): string {
  return calendarDay(
    new Date(),
    Intl.DateTimeFormat().resolvedOptions().timeZone,
  );
}

/**
 * LogActivityForm is the composer itself, without a frame, so the same fields
 * serve the standing card on the person and deal screens and the modal the
 * company screen opens.
 */
export function LogActivityForm({
  entityType,
  entityId,
  onLogged,
  initialKind,
}: Readonly<{
  entityType: EntityKind;
  entityId: string;
  onLogged?: () => void;
  // The kind the form starts on, when the caller already knows which one the
  // reader asked for. Absent means note, the ordinary case.
  initialKind?: "note" | "task";
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<ActivityDraft>(() =>
    initialKind
      ? {
          ...EMPTY_DRAFT,
          kind: initialKind,
          dueAt: initialKind === "task" ? todayDay() : "",
        }
      : EMPTY_DRAFT,
  );
  const [fileError, setFileError] = useState<string | null>(null);

  const log = useMutation({
    mutationFn: async (input: ActivityDraft) => {
      const trimmedBody = input.body.trim();
      // source_system: transcript is what routes the body through the
      // server's ADR-0058 normalizer and what the activity/transcript
      // retention scope keys its sweep on (see backend logActivity's
      // `transcript` example) — only when the writer has explicitly marked
      // this text as one (asTranscript), never inferred from kind: meeting
      // alone, or ordinary meeting notes would carry a marker meaning
      // something else and sweep on a different retention schedule.
      const isTranscript = input.kind === "meeting" && input.asTranscript;
      // A transcript is sent RAW, not trimmed: the server's normalizer
      // (transcriptnorm.go) is the one place line-1-indexing gets decided,
      // and it only trims trailing whitespace per line — a leading blank
      // line or leading indentation the client stripped first would make a
      // transcript pasted here normalize to different stored text (and
      // different line numbers) than the identical paste sent by an agent
      // or another client straight to the API.
      const outgoingBody = isTranscript ? input.body : trimmedBody;
      const { data, error } = await api.POST("/activities", {
        body: {
          kind: input.kind,
          subject: input.subject.trim(),
          body: outgoingBody || null,
          occurred_at: new Date().toISOString(),
          // The picked day becomes the instant that day ENDS in the writer's
          // own zone (format/calendarday). Handing the bare `yyyy-mm-dd` to
          // `new Date` reads it as UTC midnight instead, which is neither the
          // end of the day nor, west of UTC, the day the writer picked: the task
          // arrived already overdue, and the tasks list — which buckets in the
          // reader's zone — filed it under yesterday.
          ...(input.kind === "task" && input.dueAt
            ? { due_at: dueInstant(input.dueAt) }
            : {}),
          ...(isTranscript ? { source_system: "transcript" } : {}),
          links: [{ entity_type: entityType, entity_id: entityId }],
          source: "manual",
        },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: (_data, input) => {
      const keys =
        input.kind === "task"
          ? taskWriteKeys(entityType, entityId)
          : entityTimelineKeys(entityType, entityId);
      for (const queryKey of keys) {
        queryClient.invalidateQueries({ queryKey });
      }
      setDraft(EMPTY_DRAFT);
      onLogged?.();
    },
  });

  const setField = (patch: Partial<ActivityDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));

  return (
    <form
      className="form-stack"
      onSubmit={(event) => {
        event.preventDefault();
        log.mutate(draft);
      }}
    >
      <div className="form-row">
        <Field label={t("log.kind")}>
          {(control) => (
            <Select
              {...control}
              options={[
                { value: "note", label: t("log.kindNote") },
                { value: "task", label: t("log.kindTask") },
                { value: "meeting", label: t("log.kindMeeting") },
              ]}
              value={draft.kind}
              onChange={(value) =>
                setDraft((current) => {
                  const kind =
                    value === "task" || value === "meeting" ? value : "note";
                  // Becoming a task fills the empty due date with today — the
                  // likeliest answer, standing where the writer can change it.
                  // A date the writer already picked is never overwritten.
                  const dueAt =
                    kind === "task" && current.dueAt === ""
                      ? todayDay()
                      : current.dueAt;
                  return { ...current, kind, dueAt };
                })
              }
            />
          )}
        </Field>
        {/* Only a task carries a due date, but the field stays in place for a
            note — disabled, not hidden. Mounting it on the kind switch moved
            every control below it down while the writer was reading them, and
            `hidden` would do the same thing by another name (it is
            display:none). Disabled keeps the row's height AND shows the reader
            that the field exists and why it is not theirs to fill yet. */}
        <Field
          label={t("log.dueAt")}
          hint={draft.kind === "task" ? undefined : t("log.dueAtHint")}
        >
          {(control) => (
            <TextInput
              {...control}
              type="date"
              value={draft.dueAt}
              disabled={draft.kind !== "task"}
              onChange={(event) => setField({ dueAt: event.target.value })}
              // A native date input opens its calendar only from the tiny
              // icon; a click on the value just places a caret. Opening on
              // any click is what a writer reaching for "the date" expects.
              onClick={(event) => event.currentTarget.showPicker?.()}
            />
          )}
        </Field>
      </div>
      <Field label={t("log.subject")} required>
        {(control) => (
          <TextInput
            {...control}
            value={draft.subject}
            onChange={(event) => setField({ subject: event.target.value })}
          />
        )}
      </Field>
      {draft.kind === "meeting" && (
        <Checkbox
          label={t("log.asTranscript")}
          checked={draft.asTranscript}
          onChange={(event) => setField({ asTranscript: event.target.checked })}
        />
      )}
      <Field
        label={draft.asTranscript ? t("log.transcriptLabel") : t("log.body")}
        hint={draft.asTranscript ? t("log.transcriptHint") : undefined}
      >
        {(control) => (
          <Textarea
            {...control}
            rows={draft.asTranscript ? 10 : 3}
            value={draft.body}
            onChange={(event) => setField({ body: event.target.value })}
          />
        )}
      </Field>
      {draft.kind === "meeting" && draft.asTranscript && (
        <Field label={t("log.transcriptUpload")} hint={fileError ?? undefined}>
          {(control) => (
            <TextInput
              {...control}
              type="file"
              accept={ACCEPTED_TRANSCRIPT_EXTENSION}
              onChange={async (event) => {
                const file = event.target.files?.[0];
                event.target.value = "";
                if (!file) {
                  return;
                }
                if (
                  !file.name
                    .toLowerCase()
                    .endsWith(ACCEPTED_TRANSCRIPT_EXTENSION)
                ) {
                  setFileError(t("log.transcriptUploadRejected"));
                  return;
                }
                try {
                  const text = await file.text();
                  setFileError(null);
                  setField({ body: text });
                } catch {
                  setFileError(t("log.transcriptUploadFailed"));
                }
              }}
            />
          )}
        </Field>
      )}
      {log.isError && (
        <p className="t-caption form-error">{problemMessageOf(log.error, t)}</p>
      )}
      <div className="form-actions">
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
  );
}

/**
 * LogActivity is the standing composer card the person and deal screens keep
 * open in their rail.
 */
export function LogActivity({
  entityType,
  entityId,
}: Readonly<{ entityType: EntityKind; entityId: string }>) {
  const t = useT();
  // Logging an activity writes to a mirrored record; in overlay every write
  // answers unsupported_by_sor, so the form would only fail on submit. Guarded
  // to render nothing rather than an affordance that can't work (P1/A107,
  // ADR-0018).
  const overlay = useSorMode() === "overlay";
  if (overlay) {
    return null;
  }
  return (
    <Card className="card-stack" title={t("log.title")} sub={t("log.sub")}>
      <LogActivityForm entityType={entityType} entityId={entityId} />
    </Card>
  );
}

/**
 * LogActivityAction is the same composer as a header button, for a screen
 * whose header strip is a row of actions rather than a column of cards.
 */
export function LogActivityAction({
  entityType,
  entityId,
  initialKind,
  openOnMount,
  triggerLabel,
  disabledReasonId,
  onClose,
}: Readonly<{
  entityType: EntityKind;
  entityId: string;
  // The kind the form starts on. A suggestion that says "no task says what
  // happens next" opens straight onto a task rather than making the reader
  // pick the kind the advice already named.
  initialKind?: "note" | "task";
  // Rendered already open, with no trigger button — for a caller that IS the
  // trigger (a suggestion's action), rather than a toolbar offering the verb.
  openOnMount?: boolean;
  // What the trigger says. A header offering two ways into this form — log
  // what happened, and set what happens next — needs each button to name its
  // own verb; two buttons both reading "Log activity" is a toolbar that has
  // stopped telling the reader anything.
  triggerLabel?: MessageKey;
  // The sentence that refuses this verb, already on the page. A record that
  // takes no new activity must still SHOW the verb it will not accept — a
  // reader who cannot tell "this record is archived" from "this build has no
  // such button" learns nothing from the absence.
  disabledReasonId?: string;
  onClose?: () => void;
}>) {
  const t = useT();
  const titleId = useId();
  const [open, setOpen] = useState(Boolean(openOnMount));
  const overlay = useSorMode() === "overlay";
  const close = () => {
    setOpen(false);
    onClose?.();
  };
  if (overlay) {
    return null;
  }
  return (
    <>
      {!openOnMount && (
        <Button small reasonId={disabledReasonId} onClick={() => setOpen(true)}>
          {t(triggerLabel ?? "log.title")}
        </Button>
      )}
      <Modal open={open} onClose={close} labelledBy={titleId}>
        <h2 id={titleId} className="t-h2 modal-title">
          {/* The heading answers the verb that opened it. Titled "log an
              activity" regardless, a reader who pressed "Add task" was shown
              a different form's name and read it as the wrong dialog. */}
          {t(triggerLabel ?? "log.title")}
        </h2>
        <LogActivityForm
          entityType={entityType}
          entityId={entityId}
          initialKind={initialKind}
          onLogged={close}
        />
      </Modal>
    </>
  );
}
