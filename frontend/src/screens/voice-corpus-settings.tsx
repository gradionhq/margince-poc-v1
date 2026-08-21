import { Upload } from "lucide-react";
import type { ChangeEvent } from "react";
import { useEffect, useId, useRef, useState } from "react";
import { Button, Field, Modal, Radio, Textarea } from "../design-system/atoms";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { useT } from "../i18n";
import { problemMessageOf } from "./common";
import { useFileDrop } from "./use-file-drop";
import type { IntakeNotice, SpeakerAsk } from "./use-voice-intake";
import { useVoiceIntake } from "./use-voice-intake";
import { ACCEPTED_CORPUS_ATTR } from "./voice-intake-core";

// The Settings intake: browse or drop a file, paste writing, and answer who is
// speaking when a file turns out to be a conversation. It owns the intake hook
// so the build control beside it can be disabled while a source is still
// arriving — a build started mid-ingest describes a corpus that no longer
// exists by the time it finishes.
//
// The settings row language decides the SHAPE. Adding writing is one decision,
// so it is one row: what it is on the left, and the two ways in on the right.
// The paste box is a form — a box somebody types a whole email into and then
// commits — so it sits behind the verb, in a dialog, which keeps the row an
// answer rather than a card-sized composer.
//
// What deliberately did NOT move into that dialog is everything the intake has
// to say back. The queue, the unanswered speaker questions and the per-source
// notices all live in this component's hook, so a dialog holding them would
// discard a pending question and stop reporting `busy` the moment the reader
// closed it — the build guard beside this card reads that flag. They stay on
// the card, where a reader who has just handed over six files can see what
// happened to each.

type VoiceCorpusIntakeProps = Readonly<{
  /** null for an owner who has no profile yet: the first sample mints it. */
  profileId: string | null;
  onChanged: () => void;
  /** Told when intake is in progress, so the caller can hold the build. */
  onBusyChange?: (busy: boolean) => void;
  /** The first sample is the one that MINTS the profile: the row names itself
   * for that, and its verb leads rather than sitting beside a peer. */
  first?: boolean;
}>;

export function VoiceCorpusIntake({
  profileId,
  onChanged,
  onBusyChange,
  first = false,
}: VoiceCorpusIntakeProps) {
  const t = useT();
  const intake = useVoiceIntake({ profileId, onChanged });
  const [paste, setPaste] = useState("");
  const [pasting, setPasting] = useState(false);
  const pasteLabelId = useId();
  const fileRef = useRef<HTMLInputElement>(null);
  const zoneRef = useRef<HTMLDivElement>(null);

  // Files are accepted only inside this card. The listeners are still on the
  // window so a file dropped anywhere cannot navigate the browser away from
  // the app, but a drop on the command palette or a modal belongs to nobody
  // and must not silently become a writing sample.
  const { dragOver } = useFileDrop({
    container: zoneRef,
    active: intake.pendingAsk === null,
    onFiles: intake.addFiles,
  });

  // Told AFTER the render that changed it: calling a parent's setState from a
  // render body updates one component while another is rendering, which React
  // does not support.
  const busy = intake.busy;
  useEffect(() => {
    onBusyChange?.(busy);
  }, [busy, onBusyChange]);

  const onBrowsed = (event: ChangeEvent<HTMLInputElement>) => {
    intake.addFiles(Array.from(event.target.files ?? []));
    // Clearing lets the same file be chosen again after a failed attempt.
    event.target.value = "";
  };

  // The box is emptied only once its contents are on their way, and the dialog
  // closes with them: the reader is done, and the outcome arrives as a notice
  // on the card behind. Closing the dialog any other way KEEPS what was typed —
  // a whole pasted email is a draft, and a dialog that discards one punishes
  // the reader for pressing Escape.
  const submitPaste = () => {
    intake.addPaste(paste, t("settings.voice.pastedLabel"));
    setPaste("");
    setPasting(false);
  };

  const sampleLabel = t(
    first ? "settings.voice.addFirstLabel" : "settings.voice.addSource",
  );

  return (
    <div
      ref={zoneRef}
      className={`vdna-intake${dragOver ? " vdna-intake-dragover" : ""}`}
    >
      <SettingList>
        <SettingRow
          label={sampleLabel}
          description={t("settings.voice.dropHint")}
          control={
            <>
              {/* Named for what it does — it opens the paste form rather than
                  performing the add, and the button inside that form is named
                  for the add itself. Two controls reading the same is ambiguous
                  for a reader and for getByRole. */}
              <Button
                small
                variant={first ? "primary" : undefined}
                onClick={() => setPasting(true)}
              >
                {t(
                  first
                    ? "settings.voice.addFirstOpen"
                    : "settings.voice.addSourceOpen",
                )}
              </Button>
              <Button small onClick={() => fileRef.current?.click()}>
                <Upload aria-hidden /> {t("settings.voice.browseFiles")}
              </Button>
              <input
                ref={fileRef}
                type="file"
                multiple
                accept={ACCEPTED_CORPUS_ATTR}
                hidden
                onChange={onBrowsed}
              />
            </>
          }
        />
      </SettingList>

      {intake.pendingAsk && (
        // Keyed by the source: when the queue advances to the next file the
        // panel is a NEW panel, so the previous file's chosen speaker cannot
        // survive into a question about different people.
        //
        // Not a SettingRow: this is not a setting the reader may leave as it
        // is. It is a file held out of the corpus until a question about it is
        // answered, and it carries the warn surface that says so — a row would
        // draw it in the same neutral ink as the decisions above it.
        <SpeakerPanel
          key={intake.pendingAsk.ref}
          ask={intake.pendingAsk}
          onAnswer={intake.answerSpeaker}
          onDismiss={intake.dismissAsk}
        />
      )}

      {intake.notices.length > 0 && (
        <ul className="vdna-notices">
          {intake.notices.map((notice) => (
            <NoticeRow key={notice.ref} notice={notice} />
          ))}
        </ul>
      )}

      <Modal
        open={pasting}
        onClose={() => setPasting(false)}
        labelledBy={pasteLabelId}
      >
        {/* A real form, so Enter-with-modifier and the submit button are the
            same path. The dialog is named by the field's own label rather than
            by a heading repeating it: the box IS the dialog, and two spellings
            of one name is how a control ends up announcing something other
            than the words above it (WCAG 2.5.3). */}
        <form
          className="form-stack"
          onSubmit={(event) => {
            event.preventDefault();
            if (paste.trim().length > 0) {
              submitPaste();
            }
          }}
        >
          <Field label={<span id={pasteLabelId}>{sampleLabel}</span>}>
            {(control) => (
              <Textarea
                {...control}
                rows={8}
                value={paste}
                placeholder={t("settings.voice.addPlaceholder")}
                onChange={(e) => setPaste(e.target.value)}
              />
            )}
          </Field>
          <div className="form-actions">
            <Button variant="ghost" onClick={() => setPasting(false)}>
              {t("settings.voice.pasteCancel")}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={paste.trim().length === 0}
            >
              {first
                ? t("settings.voice.addFirstCta")
                : t("settings.voice.addSource")}
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}

// A file the preview found several speakers in: only the owner's own turns may
// become their voice, so the source waits here until they say which is theirs.
function SpeakerPanel({
  ask,
  onAnswer,
  onDismiss,
}: Readonly<{
  ask: SpeakerAsk;
  onAnswer: (speakerLabel: string) => void;
  onDismiss: () => void;
}>) {
  const t = useT();
  const [choice, setChoice] = useState<string | null>(null);
  return (
    <fieldset className="vdna-speaker">
      <legend className="vdna-label">
        {t("settings.voice.speakerQuestion", { name: ask.label })}
      </legend>
      <ul className="vdna-speaker-options">
        {ask.preview.speakers.map((speaker) => (
          <li key={speaker.label}>
            <Radio
              name={`speaker:${ask.ref}`}
              checked={choice === speaker.label}
              onChange={() => setChoice(speaker.label)}
              label={`${speaker.label} · ${t("settings.voice.speakerDetail", {
                words: speaker.words.toLocaleString(),
                turns: speaker.turns,
              })}`}
            />
          </li>
        ))}
      </ul>
      <div className="vdna-composer-actions">
        <Button
          small
          variant="primary"
          disabled={choice === null}
          onClick={() => choice !== null && onAnswer(choice)}
        >
          {t("settings.voice.speakerConfirm")}
        </Button>
        <Button small onClick={onDismiss}>
          {t("settings.voice.speakerDismiss")}
        </Button>
      </div>
    </fieldset>
  );
}

// What one finished intake says to the reader. A refusal the core did not
// recognize quotes the server's own detail rather than inventing a reason.
function noticeText(t: ReturnType<typeof useT>, notice: IntakeNotice): string {
  switch (notice.kind) {
    case "kept":
      return t("settings.voice.noticeKept", {
        name: notice.label,
        kept: (notice.keptWords ?? 0).toLocaleString(),
        total: (notice.inputWords ?? 0).toLocaleString(),
      });
    case "skippedType":
      return t("settings.voice.noticeSkippedType", { name: notice.label });
    case "skippedEmpty":
      return t("settings.voice.noticeSkippedEmpty", { name: notice.label });
    case "dismissed":
      return t("settings.voice.noticeDismissed", { name: notice.label });
    case "askQueueFull":
      return t("settings.voice.noticeAskQueueFull", { name: notice.label });
    case "refused":
      switch (notice.reason) {
        case "unattributed":
          return t("settings.voice.refusalUnattributed", {
            name: notice.label,
          });
        case "speaker":
          return t("settings.voice.refusalSpeaker", { name: notice.label });
        case "unsupported":
          return t("settings.voice.refusalUnsupported", { name: notice.label });
        default:
          return t("settings.voice.noticeFailed", {
            name: notice.label,
            detail: problemMessageOf(notice.problem, t),
          });
      }
    case "failed":
      return t("settings.voice.noticeUnexpected", { name: notice.label });
  }
}

function NoticeRow({ notice }: Readonly<{ notice: IntakeNotice }>) {
  const t = useT();
  return (
    <li
      className={`t-small vdna-notice vdna-notice-${notice.tone}`}
      role={notice.tone === "warn" ? "alert" : undefined}
    >
      {noticeText(t, notice)}
    </li>
  );
}
