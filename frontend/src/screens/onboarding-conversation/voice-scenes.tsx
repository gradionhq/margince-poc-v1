import { Check, Lightbulb } from "lucide-react";
import type { ChangeEvent, ReactNode, RefObject } from "react";
import { useEffect, useId, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { MarginceCoreScene } from "../../design-system/margince-core";
import { usePrefersReducedMotion } from "../../design-system/motion";
import { useT } from "../../i18n";
import { ACCEPTED_CORPUS_ATTR, VOICE_MIN_WORDS } from "../onboarding";
import type { VoiceInsightsData } from "../voice-insights";
import { parseVoiceInsights } from "../voice-insights";
import type { BuildStage, ConversationQuestion } from "./conversation-machine";
import type { CorpusManifestEntry } from "./use-voice-corpus";

// The voice act's work surface, as scenes: collect the writing, decide who
// is speaking when a transcript needs it, watch the model learn it, then
// read what it learned. One scene at a time, the same rule the company act
// follows — the rail beside them stays conversation, and every scene's own
// primary action is pinned to ITS foot, never the rail's.

type CorpusSummary = components["schemas"]["VoiceCorpusSummary"];
type VoiceProfileVersion = components["schemas"]["VoiceProfileVersion"];

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
 * The payoff, stated once, before the mechanics. The scene's own heading and
 * sub already say the CRM drafts mail in the reader's words; this band adds
 * the two things that make that credible — where the voice comes from, and
 * that it stays theirs alone — without repeating either sentence. The Core
 * sits at the size the brand line uses (`mw-core`'s pattern), not the hero
 * size the build scene reaches for, because this is context beside copy, not
 * the scene's own subject.
 */
function VoiceHeroBand() {
  const t = useT();
  return (
    <div className="ob-voice-hero">
      <MarginceCoreScene
        state="idle"
        feed={false}
        className="ob-voice-hero-core"
      />
      <div className="ob-voice-hero-copy">
        <p className="ob-voice-hero-kicker">{t("ob.conv.voice.heroKicker")}</p>
        <p className="ob-voice-hero-body">{t("ob.conv.voice.heroBody")}</p>
      </div>
    </div>
  );
}

// Fires the floor-reached announcement exactly once, on the transition from
// below the floor to at/above it — not on every word the server adds, which
// would talk over the reader every time a source lands. `ready` is the same
// boolean the Build button's own `disabled` reads, so the announcement can
// never fire at a moment the button disagrees with.
function useFloorReachedAnnouncement(ready: boolean, words: number): string {
  const t = useT();
  const [message, setMessage] = useState("");
  const wasReady = useRef(ready);
  useEffect(() => {
    if (ready && !wasReady.current) {
      setMessage(t("ob.conv.voice.meterReady", { words }));
    }
    wasReady.current = ready;
  }, [ready, words, t]);
  return message;
}

/**
 * The floor meter: a real `<progress>` plus the numbers behind it, reading
 * the same corpus total the sources list and the build gate already read —
 * never a second count, and never a size estimate. The bar caps at the
 * floor rather than growing past it: more words keep helping the build, but
 * the floor itself has nothing further to fill toward, so a full bar past
 * that point would misreport progress that does not exist. `ready` decides
 * only the WORDING (still short of the floor vs. cleared it); it is never
 * presented as a finished task, because more material still sharpens it.
 */
function VoiceCorpusFloorMeter({
  words,
  ready,
}: Readonly<{ words: number; ready: boolean }>) {
  const t = useT();
  const announcement = useFloorReachedAnnouncement(ready, words);
  return (
    <div className="ob-voice-meter">
      <progress
        className="ob-voice-meter-bar"
        value={Math.min(words, VOICE_MIN_WORDS)}
        max={VOICE_MIN_WORDS}
        aria-label={t("ob.conv.voice.meterLabel", { min: VOICE_MIN_WORDS })}
      />
      <p className="ob-voice-meter-line">
        {ready
          ? t("ob.conv.voice.meterReady", { words })
          : t("ob.conv.voice.meterProgress", { words, min: VOICE_MIN_WORDS })}
      </p>
      {/* Visually hidden: the visible line above already carries this exact
          sentence once the floor clears, so this only exists to say it out
          loud the one time that happens. */}
      <p className="sr-only" role="status">
        {announcement}
      </p>
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
  // The floor alone — never `canBuild`, which also folds in a request in
  // flight. A meter that dims for a busy corpus it has already cleared
  // would tell the reader they lost ground they never lost.
  const floorCleared = words >= VOICE_MIN_WORDS;
  const [pasteOpen, setPasteOpen] = useState(false);
  const [pasteText, setPasteText] = useState("");
  return (
    <VoiceScene
      eyebrow={eyebrow}
      title={t("ob.conv.voice.sceneTitle")}
      sub={t("ob.conv.voice.sceneSub")}
    >
      <VoiceHeroBand />
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

      <VoiceCorpusFloorMeter words={words} ready={floorCleared} />

      {manifest.length > 0 && (
        <section className="ob-voice-sources">
          <p className="ob-voice-sources-head">
            <span>{t("ob.conv.voice.sourcesTitle")}</span>
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
 * The speaker decision, as the scene: which voice in a multi-speaker
 * transcript is the reader's own. This is a decision with options, so it
 * takes the whole surface the same way the collect and build scenes do —
 * never a card competing for room in the rail beside it. Every number on a
 * card (words, turns) is the preview's own count, the same one the collect
 * scene's sources list uses elsewhere.
 */
export function VoiceSpeakerScene({
  eyebrow,
  question,
  onAnswer,
}: Readonly<{
  eyebrow: string;
  question: ConversationQuestion;
  onAnswer: (questionId: string, value: string) => void;
}>) {
  const t = useT();
  const group = useId();
  const [picked, setPicked] = useState("");
  return (
    <VoiceScene eyebrow={eyebrow} title={t(question.i18nKey, question.params)}>
      <div role="radiogroup" aria-label={t(question.i18nKey, question.params)}>
        <div className="ob-voice-speakers">
          {question.options.map((option) => {
            const label = option.labelKey
              ? t(option.labelKey, option.params)
              : option.label;
            const detail = option.detailKey
              ? t(option.detailKey, option.params)
              : undefined;
            const checked = picked === option.value;
            return (
              <label
                key={option.value}
                className={`ob-voice-speaker${checked ? " is-picked" : ""}`}
              >
                <input
                  type="radio"
                  name={group}
                  value={option.value}
                  checked={checked}
                  onChange={() => setPicked(option.value)}
                />
                <span className="ob-voice-speaker-disc" aria-hidden>
                  {checked && <Check />}
                </span>
                <span className="ob-voice-speaker-body">
                  <b>{label}</b>
                  {detail !== undefined && <small>{detail}</small>}
                </span>
              </label>
            );
          })}
        </div>
      </div>
      <div className="ob-voice-foot">
        <p>{t("ob.conv.voice.speakerFoot")}</p>
        <div className="ob-voice-foot-acts">
          <Button
            variant="primary"
            disabled={picked === ""}
            onClick={() => {
              // The disabled attribute keeps the pointer out; this keeps a
              // programmatic click from answering with a choice nobody made.
              if (picked !== "") {
                onAnswer(question.id, picked);
              }
            }}
          >
            {t("ob.conv.voice.speakerContinue")}
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

/**
 * The result scene: what a succeeded build learned, as the two-column board
 * the reference lays out — the sample it would send on the left, the reading
 * behind it on the right. Continue stays in the pinned `.ob-triage-continue`
 * bar every other scene's primary action already sits in, worded for what it
 * confirms here rather than the generic label; the reference's own action
 * row under the sample would otherwise say the same thing twice.
 */
export function VoiceResultScene({
  eyebrow,
  loading,
  version,
  onContinue,
}: Readonly<{
  eyebrow: string;
  loading: boolean;
  version: VoiceProfileVersion | null;
  onContinue: () => void;
}>) {
  const t = useT();
  const candidate = version !== null && version.status === "candidate";
  return (
    <VoiceScene
      eyebrow={eyebrow}
      title={t("ob.conv.voice.resultTitle")}
      sub={t("ob.conv.voice.resultSub")}
    >
      {loading && (
        <p className="ob-conv-artifact-empty">
          {t("ob.conv.voice.resultLoading")}
        </p>
      )}
      {!loading && version === null && (
        <p className="ob-conv-artifact-empty">
          {t("ob.conv.voice.resultEmpty")}
        </p>
      )}
      {version !== null && (
        <VoiceResultBoard data={parseVoiceInsights(version)} />
      )}
      <div className="ob-triage-continue">
        <p className="ob-triage-continue-status" role="status">
          {candidate ? t("ob.conv.voice.candidateNote") : ""}
        </p>
        <div className="ob-voice-continue-acts">
          <Button small variant="primary" onClick={onContinue}>
            {t("ob.conv.voice.resultContinue")}
          </Button>
        </div>
      </div>
    </VoiceScene>
  );
}

// The sample: a real draft in a card of its own. `drafts` is every sample
// the build kept; "Another scenario" cycles them locally (no server round
// trip — they are all already in hand), and disappears once there is only
// one to show, rather than offering a control with nothing to switch to.
// The header block shows only Subject: `VoiceSampleDraft` carries no To/From,
// and inventing either would be exactly the fabricated data this redesign
// keeps refusing to show.
function VoiceSampleCard({
  drafts,
  why,
}: Readonly<{
  drafts: VoiceInsightsData["sampleDrafts"];
  why: string;
}>) {
  const t = useT();
  const [index, setIndex] = useState(0);
  const sample = drafts[index % drafts.length];
  return (
    <div className="ob-voice-result-card ob-voice-sample">
      <div className="ob-voice-sample-head">
        <p className="ob-voice-result-label">
          {t("ob.conv.voice.sampleEyebrow")}
        </p>
        {drafts.length > 1 && (
          <Button
            small
            variant="ghost"
            onClick={() => setIndex((prev) => (prev + 1) % drafts.length)}
          >
            {t("ob.conv.voice.sampleAnother")}
          </Button>
        )}
      </div>
      <p className="ob-voice-sample-subject">
        <span className="ob-voice-sample-field">
          {t("ob.conv.voice.sampleSubjectLabel")}
        </span>
        <b>{sample.subject}</b>
      </p>
      <p className="ob-voice-sample-body">{sample.body}</p>
      <p className="ob-voice-sample-why">
        <span className="ob-voice-sample-why-tag">
          {t("ob.conv.voice.sampleWhyTag")}
        </span>
        {why}
      </p>
    </div>
  );
}

type MeasuredDimension = Readonly<{
  key: string;
  name: string;
  value: string;
  poleLow: string;
  poleHigh: string;
  /** A decorative marker position, 0..1 — never itself the measurement;
   * `value` and `evidence` carry every number a reader can trust. */
  fraction: number;
  evidence: string;
}>;

// The bucket edges and the marker's comparison bounds are both plain,
// documented arithmetic on the real mean-sentence-length number — never an
// invented confidence score. Generous bounds (6..30) keep the common case
// away from either end, the same rule the earlier reference bars used.
const SENTENCE_TERSE_MAX = 12;
const SENTENCE_ELABORATE_MIN = 20;
const SENTENCE_LOW_BOUND = 6;
const SENTENCE_HIGH_BOUND = 30;

function sentenceDimension(
  meanSentence: number,
  t: ReturnType<typeof useT>,
): MeasuredDimension {
  const value =
    meanSentence < SENTENCE_TERSE_MAX
      ? t("ob.conv.voice.dimSentencePoleLow")
      : meanSentence > SENTENCE_ELABORATE_MIN
        ? t("ob.conv.voice.dimSentencePoleHigh")
        : t("ob.conv.voice.dimSentenceMeasured");
  const fraction = Math.max(
    0,
    Math.min(
      1,
      (meanSentence - SENTENCE_LOW_BOUND) /
        (SENTENCE_HIGH_BOUND - SENTENCE_LOW_BOUND),
    ),
  );
  return {
    key: "sentence",
    name: t("ob.conv.voice.dimSentenceName"),
    value,
    poleLow: t("ob.conv.voice.dimSentencePoleLow"),
    poleHigh: t("ob.conv.voice.dimSentencePoleHigh"),
    fraction,
    evidence: t("ob.conv.voice.dimSentenceEvidence", { count: meanSentence }),
  };
}

// `parseVoiceInsights` is the only axis this shape currently carries; the
// reference shows five (formality, sentence length, warmth, directness,
// vocabulary), and the other four have no server-measured equivalent — so
// they are absent here rather than rendered with an invented marker.
function measuredDimensions(
  data: VoiceInsightsData,
  t: ReturnType<typeof useT>,
): readonly MeasuredDimension[] {
  return data.meanSentence === null
    ? []
    : [sentenceDimension(data.meanSentence, t)];
}

// A readout, not a control: no input, no drag handle, nothing focusable — a
// slider-shaped element a reader could not move must not look movable.
function VoiceDimensionGauge({ dim }: Readonly<{ dim: MeasuredDimension }>) {
  return (
    <div className="ob-voice-dim">
      <div className="ob-voice-dim-head">
        <span className="ob-voice-dim-name">{dim.name}</span>
        <span className="ob-voice-dim-value">{dim.value}</span>
      </div>
      <div className="ob-voice-dim-track" aria-hidden>
        <span
          className="ob-voice-dim-marker"
          style={{ left: `${dim.fraction * 100}%` }}
        />
      </div>
      <div className="ob-voice-dim-poles" aria-hidden>
        <span>{dim.poleLow}</span>
        <span>{dim.poleHigh}</span>
      </div>
      <p className="ob-voice-dim-evidence">{dim.evidence}</p>
    </div>
  );
}

function VoiceDimensionsCard({
  dimensions,
  words,
  sources,
}: Readonly<{
  dimensions: readonly MeasuredDimension[];
  words: number | null;
  sources: number | null;
}>) {
  const t = useT();
  return (
    <div className="ob-voice-result-card">
      <div className="ob-voice-dims-head">
        <span className="ob-voice-result-label">
          {t("ob.conv.voice.dimensionsTitle")}
        </span>
        <span className="ob-voice-dims-count">
          {t("ob.conv.voice.dimensionsCount", { count: dimensions.length })}
        </span>
      </div>
      {(words !== null || sources !== null) && (
        <p className="ob-voice-dims-meta">
          {words !== null && t("voice.insights.statWords", { count: words })}
          {words !== null && sources !== null ? " · " : ""}
          {sources !== null &&
            t("voice.insights.statSources", { count: sources })}
        </p>
      )}
      <div className="ob-voice-dims-list">
        {dimensions.map((dim) => (
          <VoiceDimensionGauge key={dim.key} dim={dim} />
        ))}
      </div>
    </div>
  );
}

// The reading: the identity summary as the lead line, the thinking pattern
// underneath where the build found one.
function VoiceThinkingCard({
  thinking,
  identity,
}: Readonly<{ thinking: string | null; identity: string | null }>) {
  const t = useT();
  return (
    <div className="ob-voice-result-card">
      {identity !== null && (
        <p className="ob-voice-result-identity">{identity}</p>
      )}
      {thinking !== null && (
        <>
          <p className="ob-voice-result-label">
            <Lightbulb aria-hidden /> {t("voice.insights.thinkingLabel")}
          </p>
          <p>{thinking}</p>
        </>
      )}
    </div>
  );
}

function VoiceMovesCard({
  moves,
}: Readonly<{ moves: VoiceInsightsData["moves"] }>) {
  const t = useT();
  return (
    <div className="ob-voice-result-card">
      <p className="ob-voice-result-label">{t("voice.insights.movesLabel")}</p>
      <ul className="ob-voice-moves">
        {moves.map((move) => (
          <li key={move.move}>
            <b>{move.move}</b>
            <blockquote>{move.quote}</blockquote>
          </li>
        ))}
      </ul>
    </div>
  );
}

function VoiceAvoidCard({ avoid }: Readonly<{ avoid: readonly string[] }>) {
  const t = useT();
  return (
    <div className="ob-voice-result-card">
      <p className="ob-voice-result-label">{t("voice.insights.avoidLabel")}</p>
      <ul className="ob-voice-avoid">
        {avoid.map((item) => (
          <li key={item}>{item}</li>
        ))}
      </ul>
    </div>
  );
}

/**
 * What a succeeded build learned, as the two-column board: the sample on the
 * left, everything measured or observed about the reading on the right. Every
 * fact still comes from `parseVoiceInsights`; this is a second RENDERING of
 * that data, never a second parser. A section with nothing to show renders
 * nothing — a build that skipped a stage never gets an empty card pretending
 * it ran, and a dimension this shape does not carry never gets an invented
 * marker standing in for it.
 */
function VoiceResultBoard({ data }: Readonly<{ data: VoiceInsightsData }>) {
  const t = useT();
  const hasThinking = data.thinking !== null || data.identity !== null;
  const dimensions = measuredDimensions(data, t);
  // The sample's own "why" line borrows the same signature-move names the
  // moves card lists in full below, at the reference's own granularity — a
  // short joined phrase, not the identity summary (which now leads the
  // thinking card instead, never repeated here).
  const why =
    data.moves.length > 0
      ? data.moves.map((move) => move.move).join(", ")
      : t("voice.insights.draftOnly");
  return (
    <div className="ob-voice-board">
      <div className="ob-voice-board-col">
        {data.sampleDrafts.length > 0 && (
          <VoiceSampleCard drafts={data.sampleDrafts} why={why} />
        )}
      </div>
      <div className="ob-voice-board-col">
        {dimensions.length > 0 && (
          <VoiceDimensionsCard
            dimensions={dimensions}
            words={data.words}
            sources={data.sources}
          />
        )}
        {hasThinking && (
          <VoiceThinkingCard
            thinking={data.thinking}
            identity={data.identity}
          />
        )}
        {data.moves.length > 0 && <VoiceMovesCard moves={data.moves} />}
        {data.avoid.length > 0 && <VoiceAvoidCard avoid={data.avoid} />}
        {data.nextBest !== null && (
          <p className="ob-voice-result-next">
            <b>{t("voice.insights.nextBestLabel")}</b> {data.nextBest}
          </p>
        )}
      </div>
    </div>
  );
}
