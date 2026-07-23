import { Check, Loader } from "lucide-react";
import type { components } from "../../api/schema";
import { useT } from "../../i18n";
import type { BuildStage } from "./conversation-machine";
import { bandLabelKeys } from "./narration";
import type { CorpusManifestEntry } from "./use-voice-corpus";

// The right panel of the voice act: the corpus manifest, the honest meter,
// and the build stage tracker. Every number rendered here is a server
// number — the summary the last ingest returned and the per-source
// kept-of-total stats. Removing a source is deliberately absent for now;
// the classic settings voice card covers removal.

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

type VoiceActArtifactProps = Readonly<{
  summary: CorpusSummary | null;
  manifest: readonly CorpusManifestEntry[];
  /** The live build stage; null outside a running build. */
  stage: BuildStage | null;
  /** Whether a build is in flight (queued counts: the tracker shows). */
  building: boolean;
}>;

export function VoiceActArtifact({
  summary,
  manifest,
  stage,
  building,
}: VoiceActArtifactProps) {
  const t = useT();
  return (
    <div className="mw-review ob-conv-artifact">
      <div className="mw-review-heading">
        <span>{t("ob.ai.liveArtifact")}</span>
        <h2>{t("ob.conv.voice.artifactTitle")}</h2>
        <p>{t("ob.conv.voice.artifactBody")}</p>
      </div>
      {summary === null && manifest.length === 0 ? (
        <p className="ob-conv-artifact-empty">
          {t("ob.conv.voice.artifactEmpty")}
        </p>
      ) : (
        <>
          {summary !== null && <CorpusMeter summary={summary} />}
          {manifest.length > 0 && (
            <ul className="ob-conv-manifest">
              {manifest.map((entry) => (
                <li key={entry.ref}>
                  <Check aria-hidden />
                  <span>
                    <strong>{entry.label}</strong>
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
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      {building && <BuildTracker stage={stage} />}
    </div>
  );
}

function CorpusMeter({ summary }: Readonly<{ summary: CorpusSummary }>) {
  const t = useT();
  const percent = Math.min(
    100,
    (summary.total_words / summary.target_words) * 100,
  );
  const registers = Object.entries(summary.register_words)
    .filter(([, words]) => words > 0)
    .map(([register, words]) => `${register}: ${words}`)
    .join(" · ");
  return (
    <div className="ob-conv-meter">
      <div className="ob-conv-meter-top">
        <span>
          {t("ob.conv.voice.meterWords", {
            words: summary.total_words,
            target: summary.target_words,
          })}
        </span>
        <span>
          {t("ob.conv.voice.meterBand", {
            band: t(bandLabelKeys[summary.quality_band]),
          })}
        </span>
      </div>
      {/* Purely decorative: the words-of-target line above already carries
          the same numbers as text, so the bar stays out of the a11y tree. */}
      <div className="ob-conv-meter-bar" aria-hidden>
        <span style={{ width: `${percent}%` }} />
      </div>
      {registers !== "" && (
        <small>{t("ob.conv.voice.registerMix", { mix: registers })}</small>
      )}
    </div>
  );
}

// The four fixed pipeline stages; the current one carries the spinner, the
// ones before it a check. A queued build shows the full untouched ladder.
function BuildTracker({ stage }: Readonly<{ stage: BuildStage | null }>) {
  const t = useT();
  const reached = stage === null ? -1 : BUILD_STAGES.indexOf(stage);
  return (
    <ol className="ob-conv-stages" aria-label={t("ob.conv.voice.stageTitle")}>
      {BUILD_STAGES.map((name, index) => (
        <li
          key={name}
          data-state={
            index < reached ? "done" : index === reached ? "current" : "todo"
          }
        >
          {index < reached ? (
            <Check aria-hidden />
          ) : (
            <Loader aria-hidden className={index === reached ? "spin" : ""} />
          )}
          <span>{t(stageLabelKeys[name])}</span>
        </li>
      ))}
    </ol>
  );
}
