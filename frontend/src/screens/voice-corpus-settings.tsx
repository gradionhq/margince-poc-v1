import { Upload } from "lucide-react";
import type { ChangeEvent } from "react";
import { useEffect, useRef, useState } from "react";
import { Button, Radio, Textarea } from "../design-system/atoms";
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

type VoiceCorpusIntakeProps = Readonly<{
  /** null for an owner who has no profile yet: the first sample mints it. */
  profileId: string | null;
  onChanged: () => void;
  /** Told when intake is in progress, so the caller can hold the build. */
  onBusyChange?: (busy: boolean) => void;
  /** The empty-state box names itself and opens taller — it is the one a
   * person pastes a whole email into. */
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

  return (
    <div
      ref={zoneRef}
      className={`vdna-intake${dragOver ? " vdna-intake-dragover" : ""}`}
    >
      {intake.pendingAsk && (
        // Keyed by the source: when the queue advances to the next file the
        // panel is a NEW panel, so the previous file's chosen speaker cannot
        // survive into a question about different people.
        <SpeakerPanel
          key={intake.pendingAsk.ref}
          ask={intake.pendingAsk}
          onAnswer={intake.answerSpeaker}
          onDismiss={intake.dismissAsk}
        />
      )}

      <div className="vdna-composer">
        {first && (
          <div className="vdna-label">{t("settings.voice.addFirstLabel")}</div>
        )}
        <Textarea
          rows={first ? 8 : 4}
          value={paste}
          placeholder={t("settings.voice.addPlaceholder")}
          onChange={(e) => setPaste(e.target.value)}
        />
        <div className="vdna-composer-actions">
          <Button
            small
            variant={first ? "primary" : undefined}
            disabled={paste.trim().length === 0}
            onClick={() => {
              intake.addPaste(paste, t("settings.voice.pastedLabel"));
              setPaste("");
            }}
          >
            {first
              ? t("settings.voice.addFirstCta")
              : t("settings.voice.addSource")}
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
        </div>
        <p className="t-small vdna-drophint">{t("settings.voice.dropHint")}</p>
      </div>

      {intake.notices.length > 0 && (
        <ul className="vdna-notices">
          {intake.notices.map((notice) => (
            <NoticeRow key={notice.ref} notice={notice} />
          ))}
        </ul>
      )}
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
