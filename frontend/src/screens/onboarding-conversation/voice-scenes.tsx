import { Check } from "lucide-react";
import type { ChangeEvent, ReactNode, RefObject } from "react";
import { useEffect, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { MarginceCoreScene } from "../../design-system/margince-core";
import { usePrefersReducedMotion } from "../../design-system/motion";
import { useT } from "../../i18n";
import { ACCEPTED_CORPUS_ATTR, VOICE_MIN_WORDS } from "../onboarding";
import type { BuildStage } from "./conversation-machine";
import type { CorpusManifestEntry } from "./use-voice-corpus";

// The voice act's work surface, as scenes: collect the writing, watch the
// model learn it, then read what it learned. One scene at a time, the same
// rule the company act follows — the rail beside them stays conversation.

type CorpusSummary = components["schemas"]["VoiceCorpusSummary"];

const BUILD_STAGES: readonly BuildStage[] = [
  "snapshot",
  "extract",
  "evaluate",
  "activate",
];

const stageLabelKeys = {
  snapshot: "ob.conv.build.snapshot",
  extract: "ob.conv.build.extract",
  evaluate: "ob.conv.build.evaluate",
  activate: "ob.conv.build.activate",
} as const;

// How often the ring closes a slice of the gap to the server's ceiling, and
// how much of that gap it closes per tick. Timer-driven, not rAF: a
// background tab suspends rAF, and a build the reader tabbed away from must
// still read as moving when they come back.
const TWEEN_TICK_MS = 60;
const TWEEN_EASE = 0.08;

/**
 * The displayed progress, creeping toward `ceiling` (the highest fraction the
 * server has actually confirmed) instead of jumping to it. Starts at zero on
 * mount — there is no earlier value to inherit — and from then on only ever
 * closes the gap to whatever the current ceiling is, so it never exceeds it
 * and a long stage still keeps the ring visibly moving. `prefers-reduced-motion`
 * skips the crawl and reads the ceiling directly.
 */
function useCrawlingProgress(ceiling: number): number {
  const reduced = usePrefersReducedMotion();
  const [displayed, setDisplayed] = useState(0);
  const target = useRef(ceiling);
  target.current = ceiling;

  useEffect(() => {
    if (reduced) {
      return;
    }
    const timer = setInterval(() => {
      setDisplayed((prev) => {
        const gap = target.current - prev;
        return Math.abs(gap) < 0.001 ? target.current : prev + gap * TWEEN_EASE;
      });
    }, TWEEN_TICK_MS);
    return () => clearInterval(timer);
  }, [reduced]);

  return reduced ? ceiling : displayed;
}

/** The scene frame: eyebrow, headline, lead paragraph, then the body. */
export function VoiceScene({
  eyebrow,
  title,
  sub,
  aside,
  children,
}: Readonly<{
  eyebrow: string;
  title: string;
  sub?: string;
  aside?: ReactNode;
  children: ReactNode;
}>) {
  return (
    <div className="ob-scene ob-voice-scene">
      <div className="ob-decision-head">
        <div>
          <p className="ob-scene-eyebrow">{eyebrow}</p>
          <h2>{title}</h2>
          {sub !== undefined && <p className="ob-scene-sub">{sub}</p>}
        </div>
        {aside}
      </div>
      {children}
    </div>
  );
}

/**
 * The collect scene: the drop target, the sources the server has ingested,
 * and the one action that starts the build. Every number is the server's —
 * the meter counts what was actually kept, not what was handed over. Intake
 * is entirely the scene's: a file (browse or the window-wide drop) and a
 * pasted text both land here, so no other surface offers to add a source.
 */
export function VoiceCollectScene({
  eyebrow,
  summary,
  manifest,
  fileRef,
  onFiles,
  onAddPaste,
  onBuild,
  onSkip,
  canBuild,
  startPending,
  startError,
}: Readonly<{
  eyebrow: string;
  summary: CorpusSummary | null;
  manifest: readonly CorpusManifestEntry[];
  fileRef: RefObject<HTMLInputElement | null>;
  onFiles: (event: ChangeEvent<HTMLInputElement>) => void;
  onAddPaste: (text: string) => void;
  onBuild: () => void;
  onSkip: () => void;
  canBuild: boolean;
  startPending: boolean;
  startError: string | null;
}>) {
  const t = useT();
  const words = summary?.total_words ?? 0;
  const [pasteOpen, setPasteOpen] = useState(false);
  const [pasteText, setPasteText] = useState("");
  return (
    <VoiceScene
      eyebrow={eyebrow}
      title={t("ob.conv.voice.sceneTitle")}
      sub={t("ob.conv.voice.sceneSub")}
    >
      <div className="ob-voice-drop">
        <input
          ref={fileRef}
          type="file"
          multiple
          hidden
          accept={ACCEPTED_CORPUS_ATTR}
          onChange={onFiles}
        />
        <p className="ob-voice-drop-title">{t("ob.conv.voice.dropTitle")}</p>
        <p className="ob-voice-drop-sub">{t("ob.conv.voice.dropSub")}</p>
        <div className="ob-voice-drop-acts">
          <Button small onClick={() => fileRef.current?.click()}>
            {t("ob.conv.voice.browse")}
          </Button>
          <Button small variant="ghost" onClick={() => setPasteOpen(true)}>
            {t("ob.conv.voice.pasteInstead")}
          </Button>
        </div>
        {!pasteOpen && (
          <p className="ob-voice-drop-sub">{t("ob.conv.voice.dropHint")}</p>
        )}
        {pasteOpen && (
          <div className="ob-voice-paste">
            <textarea
              className="ob-voice-paste-area"
              rows={5}
              value={pasteText}
              placeholder={t("ob.conv.voice.composer")}
              aria-label={t("ob.conv.voice.composer")}
              onChange={(event) => setPasteText(event.target.value)}
            />
            <div className="ob-voice-drop-acts">
              <Button
                small
                variant="primary"
                disabled={pasteText.trim() === ""}
                onClick={() => {
                  onAddPaste(pasteText.trim());
                  setPasteText("");
                  setPasteOpen(false);
                }}
              >
                {t("ob.conv.voice.pasteAdd")}
              </Button>
              <Button
                small
                variant="ghost"
                onClick={() => {
                  setPasteText("");
                  setPasteOpen(false);
                }}
              >
                {t("ob.conv.voice.pasteDiscard")}
              </Button>
            </div>
          </div>
        )}
      </div>

      {manifest.length > 0 && (
        <section className="ob-voice-sources">
          <p className="ob-voice-sources-head">
            <span>{t("ob.conv.voice.sourcesTitle")}</span>
            <b>
              {t("ob.conv.voice.sourcesMeter", {
                words,
                min: VOICE_MIN_WORDS,
              })}
            </b>
          </p>
          <ul>
            {manifest.map((entry) => (
              <li key={entry.ref}>
                <span className="ob-voice-source-body">
                  <b>{entry.label}</b>
                  <small>
                    {entry.transcript
                      ? t("ob.conv.voice.manifestKept", {
                          kept: entry.keptWords,
                          total: entry.inputWords,
                        })
                      : t("ob.conv.voice.manifestWords", {
                          words: entry.keptWords,
                        })}
                  </small>
                </span>
                <Check className="ob-voice-source-check" aria-hidden />
              </li>
            ))}
          </ul>
        </section>
      )}

      {startError !== null && (
        <p className="mw-send-error" role="alert">
          {startError}
        </p>
      )}

      <div className="ob-voice-foot">
        <p>
          {canBuild
            ? t("ob.conv.voice.footReady")
            : t("ob.conv.voice.footFloor", { min: VOICE_MIN_WORDS })}
        </p>
        <div className="ob-voice-foot-acts">
          <Button small variant="ghost" onClick={onSkip}>
            {t("ob.conv.voice.skipped")}
          </Button>
          <Button
            variant="primary"
            className="ob-conv-build-chip"
            disabled={!canBuild || startPending}
            onClick={onBuild}
          >
            {t("ob.conv.voice.buildChip")}
          </Button>
        </div>
      </div>
    </VoiceScene>
  );
}

/**
 * The build scene: the Core carrying the progress ring with the percentage
 * inside it, and the four pipeline stages as a checklist. The ceiling is
 * DERIVED from the stage the server reports; the displayed number crawls
 * toward it (useCrawlingProgress) so the ring keeps moving during a stage
 * instead of sitting still, but it can never pass the ceiling the server has
 * actually confirmed, and it reaches 100 only once the build genuinely
 * completes — at which point this scene is no longer the one on screen.
 */
export function VoiceBuildScene({
  stage,
  summary,
  sources,
  model,
}: Readonly<{
  stage: BuildStage | null;
  summary: CorpusSummary | null;
  sources: number;
  model: string;
}>) {
  const t = useT();
  const reached = stage === null ? -1 : BUILD_STAGES.indexOf(stage);
  // A queued build (no stage yet) shows nothing claimed; each reached stage
  // is one quarter, and the last stage completes only when the build does.
  const ceiling = Math.max(0, reached + 1) / (BUILD_STAGES.length + 1);
  const progress = useCrawlingProgress(ceiling);
  return (
    <div className="ob-scene ob-voice-building">
      <div className="ob-voice-orb">
        <MarginceCoreScene state="working" progress={progress} feed={false} />
        {/* Decorative: the stage checklist below and the rail's own log
            (role="log" in ConversationThread) already carry the build's
            progress in words, so the crawling digits stay out of the a11y
            tree instead of being announced on every tick. */}
        <span className="ob-voice-orb-pct" aria-hidden>
          {Math.round(progress * 100)}
          <small>%</small>
        </span>
      </div>
      <h2>{t("ob.conv.voice.buildingTitle")}</h2>
      <p className="ob-voice-building-meta">
        {t("ob.conv.voice.buildingMeta", {
          words: summary?.total_words ?? 0,
          sources,
        })}
      </p>
      <p className="ob-voice-building-model">
        <i aria-hidden /> {model}
      </p>
      <ol className="ob-conv-stages" aria-label={t("ob.conv.voice.stageTitle")}>
        {BUILD_STAGES.map((name, index) => (
          <li
            key={name}
            data-state={
              index < reached ? "done" : index === reached ? "current" : "todo"
            }
          >
            {index < reached && <Check aria-hidden />}
            <span>{t(stageLabelKeys[name])}</span>
          </li>
        ))}
      </ol>
    </div>
  );
}
