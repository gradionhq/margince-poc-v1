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
 * The result scene: what a succeeded build learned, with Continue pinned to
 * its own foot in the `.ob-triage-continue` bar every scene's primary action
 * now sits in — the rail narrates the build finishing, but acting on that is
 * this surface's alone. `version` is the built profile once it has arrived;
 * a candidate version's review note lives IN the bar, next to the action it
 * actually gates, instead of repeating itself above the insights too.
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
        <VoiceResultInsights data={parseVoiceInsights(version)} />
      )}
      <div className="ob-triage-continue">
        <p className="ob-triage-continue-status" role="status">
          {candidate ? t("ob.conv.voice.candidateNote") : ""}
        </p>
        <div className="ob-voice-continue-acts">
          <Button small variant="primary" onClick={onContinue}>
            {t("ob.conv.results.continue")}
          </Button>
        </div>
      </div>
    </VoiceScene>
  );
}

// A dimension bar's fill is decorative scale, not a measured percentage the
// server returns — the number beside it is the real, honest count; the bar
// only gives it a shape to compare at a glance. References are generous
// (rarely full) rather than tight, so filling the bar is the exception that
// tells the reader something, not the common case.
const WORDS_REFERENCE = 8000;
const SENTENCE_REFERENCE = 30;
const SOURCES_REFERENCE = 8;

function VoiceDimension({
  label,
  value,
  reference,
}: Readonly<{ label: string; value: number; reference: number }>) {
  const fill = Math.max(0, Math.min(1, value / reference));
  return (
    <div className="ob-voice-dimension">
      <span className="ob-voice-dimension-label">{label}</span>
      <span className="ob-voice-dimension-track" aria-hidden>
        <span
          className="ob-voice-dimension-fill"
          style={{ width: `${fill * 100}%` }}
        />
      </span>
    </div>
  );
}

// The sample: a real draft in a card of its own, with the reading that
// explains why it lands the way it does directly underneath it. `identity`
// is already claimed for this card once one is passed — the caller decides
// that, so this component never has to guess whether it is also showing
// up in the thinking card below.
function VoiceSampleCard({
  sample,
  why,
}: Readonly<{
  sample: { subject: string; body: string };
  why: string;
}>) {
  const t = useT();
  return (
    <div className="ob-voice-result-card ob-voice-sample">
      <p className="ob-voice-result-label">
        {t("voice.insights.samplesLabel")}
      </p>
      <p className="ob-voice-sample-subject">{sample.subject}</p>
      <p className="ob-voice-sample-body">{sample.body}</p>
      <p className="ob-voice-sample-why">{why}</p>
    </div>
  );
}

// The measured dimensions, one bar per number the server actually returned.
function VoiceDimensionsCard({
  data,
}: Readonly<{
  data: Pick<VoiceInsightsData, "words" | "sources" | "meanSentence">;
}>) {
  const t = useT();
  return (
    <div className="ob-voice-result-card ob-voice-dimensions">
      {data.words !== null && (
        <VoiceDimension
          label={t("voice.insights.statWords", { count: data.words })}
          value={data.words}
          reference={WORDS_REFERENCE}
        />
      )}
      {data.sources !== null && (
        <VoiceDimension
          label={t("voice.insights.statSources", { count: data.sources })}
          value={data.sources}
          reference={SOURCES_REFERENCE}
        />
      )}
      {data.meanSentence !== null && (
        <VoiceDimension
          label={t("voice.insights.statSentence", {
            count: data.meanSentence,
          })}
          value={data.meanSentence}
          reference={SENTENCE_REFERENCE}
        />
      )}
    </div>
  );
}

// The reading: the thinking pattern where the build found one, the identity
// summary where it has not already introduced the sample above it.
function VoiceThinkingCard({
  thinking,
  identity,
}: Readonly<{ thinking: string | null; identity: string | null }>) {
  const t = useT();
  return (
    <div className="ob-voice-result-card">
      {thinking !== null && (
        <>
          <p className="ob-voice-result-label">
            <Lightbulb aria-hidden /> {t("voice.insights.thinkingLabel")}
          </p>
          <p>{thinking}</p>
        </>
      )}
      {identity !== null && (
        <p className="ob-voice-result-identity">{identity}</p>
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
 * What a succeeded build learned, as cards with a clear hierarchy — this
 * step's own reading of `VoiceInsightsData`, not the flat stacked text the
 * Settings screen's shared component renders. Every fact still comes from
 * `parseVoiceInsights`; this is a second RENDERING of that data, never a
 * second parser, so the two surfaces can never disagree about what a build
 * actually produced. A section that has nothing to show renders nothing —
 * a build that skipped a stage never gets an empty card pretending it ran.
 */
function VoiceResultInsights({ data }: Readonly<{ data: VoiceInsightsData }>) {
  const t = useT();
  const sample = data.sampleDrafts[0] ?? null;
  // The identity summary reads as the reason a sample landed the way it did
  // when there is one to sit under; with no sample it is the lead line of
  // what the build learned instead — never both, or the same sentence would
  // print twice.
  const identityInSample = sample !== null && data.identity !== null;
  const hasDimensions =
    data.words !== null || data.sources !== null || data.meanSentence !== null;
  const hasThinking = data.thinking !== null || data.identity !== null;
  return (
    <div className="ob-voice-result">
      {sample !== null && (
        <VoiceSampleCard
          sample={sample}
          why={
            identityInSample
              ? (data.identity ?? "")
              : t("voice.insights.draftOnly")
          }
        />
      )}
      {hasDimensions && <VoiceDimensionsCard data={data} />}
      {hasThinking && (
        <VoiceThinkingCard
          thinking={data.thinking}
          identity={identityInSample ? null : data.identity}
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
  );
}
