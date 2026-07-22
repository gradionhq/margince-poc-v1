import { FileText, Lightbulb, Quote } from "lucide-react";
import type { components } from "../api/schema";
import { Badge } from "../design-system/atoms";
import { useT } from "../i18n";
import "./voice-dna.css";

type VoiceProfileVersion = components["schemas"]["VoiceProfileVersion"];

// The derived artifact's structured half (profile_json/stats_json) is
// free-form JSON on the wire; these narrowing helpers keep the screen
// honest about what the builder actually stored — a missing or malformed
// section renders as absent, never as a crash or an invented value.
function asRecord(value: unknown): Record<string, unknown> | null {
  if (typeof value === "object" && value !== null && !Array.isArray(value)) {
    return value as Record<string, unknown>;
  }
  return null;
}

function asString(value: unknown): string | null {
  return typeof value === "string" && value.trim() !== "" ? value : null;
}

function asNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function asStringList(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const out: string[] = [];
  for (const item of value) {
    const text = asString(item);
    if (text) {
      out.push(text);
    }
  }
  return out;
}

export type VoiceSignatureMove = { move: string; quote: string };
export type VoiceSampleDraft = {
  subject: string;
  body: string;
  score: number | null;
};

export type VoiceInsightsData = {
  identity: string | null;
  thinking: string | null;
  obsessions: string[];
  moves: VoiceSignatureMove[];
  avoid: string[];
  sampleDrafts: VoiceSampleDraft[];
  nextBest: string | null;
  nextBestKey: string | null;
  nextBestWords: number | null;
  words: number | null;
  meanSentence: number | null;
  sources: number | null;
  modelName: string | null;
};

function parseMoves(value: unknown): VoiceSignatureMove[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const moves: VoiceSignatureMove[] = [];
  for (const raw of value) {
    const record = asRecord(raw);
    const move = asString(record?.move);
    const quote = asString(record?.quote);
    if (move && quote) {
      moves.push({ move, quote });
    }
  }
  return moves;
}

function parseSampleDrafts(value: unknown): VoiceSampleDraft[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const drafts: VoiceSampleDraft[] = [];
  for (const raw of value) {
    const record = asRecord(raw);
    const body = asString(record?.body);
    if (body) {
      drafts.push({
        subject: asString(record?.subject) ?? "",
        body,
        score: asNumber(record?.voice_score),
      });
    }
  }
  return drafts;
}

// parseVoiceInsights extracts the presentation payload one active or
// candidate version carries. Everything is optional by construction.
export function parseVoiceInsights(
  version: VoiceProfileVersion,
): VoiceInsightsData {
  const profileJSON = asRecord(version.profile_json) ?? {};
  const statsJSON = asRecord(version.stats_json) ?? {};
  const inference = asRecord(profileJSON.inference) ?? {};
  const guidance = asRecord(profileJSON.guidance) ?? {};
  const moves = parseMoves(inference.signature_moves);
  const sampleDrafts = parseSampleDrafts(profileJSON.sample_drafts);
  return {
    identity: asString(inference.identity_summary),
    thinking: asString(inference.thinking_pattern),
    obsessions: asStringList(inference.observed_obsessions),
    moves,
    avoid: asStringList(inference.avoid),
    sampleDrafts,
    nextBest: asString(guidance.next_best),
    nextBestKey: asString(guidance.next_best_key),
    nextBestWords: asNumber(guidance.next_best_words),
    words: asNumber(statsJSON.word_count),
    meanSentence: asNumber(statsJSON.mean_sentence_words),
    sources: asNumber(statsJSON.sample_count),
    modelName: asString(version.model_name),
  };
}

// nextBestCopy localizes the guidance nudge from its structured key; an
// unknown key (a newer server) falls back to the server's prose.
function nextBestCopy(
  t: ReturnType<typeof useT>,
  data: VoiceInsightsData,
): string | null {
  switch (data.nextBestKey) {
    case "add_transcript":
      return t("voice.insights.next.addTranscript");
    case "add_email":
      return t("voice.insights.next.addEmail");
    case "add_words":
      return t("voice.insights.next.addWords", {
        count: (data.nextBestWords ?? 0).toLocaleString(),
      });
    case "at_target":
      return t("voice.insights.next.atTarget");
    default:
      return null;
  }
}

// VoiceInsights is the impress surface both the onboarding success card and
// the Settings screen render: what the build learned, with the user's own
// words as proof, plus the honest what-to-add-next nudge.
export function VoiceInsights({
  data,
  profileVersion,
}: Readonly<{ data: VoiceInsightsData; profileVersion: number }>) {
  const t = useT();
  return (
    <div className="vdna-insights">
      <div className="vdna-provenance t-small">
        {t("voice.insights.provenance", { n: profileVersion })}
        {data.modelName && data.modelName !== "unrecorded"
          ? ` · ${data.modelName}`
          : ""}
      </div>
      {(data.words !== null ||
        data.sources !== null ||
        data.meanSentence !== null) && (
        <div className="vdna-stats t-small">
          {data.words !== null &&
            t("voice.insights.statWords", {
              count: data.words.toLocaleString(),
            })}
          {data.sources !== null &&
            ` · ${t("voice.insights.statSources", { count: data.sources })}`}
          {data.meanSentence !== null &&
            ` · ${t("voice.insights.statSentence", { count: data.meanSentence })}`}
        </div>
      )}
      {data.thinking && (
        <div className="vdna-thinking">
          <div className="vdna-label">
            <Lightbulb aria-hidden /> {t("voice.insights.thinkingLabel")}
          </div>
          <p>{data.thinking}</p>
        </div>
      )}
      {data.identity && <p className="vdna-identity">{data.identity}</p>}
      {data.obsessions.length > 0 && (
        <div className="vdna-chips">
          {data.obsessions.map((theme) => (
            <Badge key={theme}>{theme}</Badge>
          ))}
        </div>
      )}
      <SignatureMoves moves={data.moves} />
      {data.avoid.length > 0 && (
        <div className="vdna-avoid">
          <div className="vdna-label">{t("voice.insights.avoidLabel")}</div>
          <ul>
            {data.avoid.map((rule) => (
              <li key={rule}>{rule}</li>
            ))}
          </ul>
        </div>
      )}
      <SampleDrafts drafts={data.sampleDrafts} />
      {(data.nextBestKey || data.nextBest) && (
        <div className="vdna-nextbest">
          <b>{t("voice.insights.nextBestLabel")}</b>{" "}
          {nextBestCopy(t, data) ?? data.nextBest}
        </div>
      )}
    </div>
  );
}

function SignatureMoves({ moves }: Readonly<{ moves: VoiceSignatureMove[] }>) {
  const t = useT();
  if (moves.length === 0) {
    return null;
  }
  return (
    <div className="vdna-moves">
      <div className="vdna-label">
        <Quote aria-hidden /> {t("voice.insights.movesLabel")}
      </div>
      <ul>
        {moves.slice(0, 3).map((move) => (
          <li key={move.move}>
            <b>{move.move}</b>
            <span className="vdna-quote">“{move.quote}”</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function SampleDrafts({ drafts }: Readonly<{ drafts: VoiceSampleDraft[] }>) {
  const t = useT();
  if (drafts.length === 0) {
    return null;
  }
  return (
    <div className="vdna-samples">
      <div className="vdna-label">
        <FileText aria-hidden /> {t("voice.insights.samplesLabel")}
      </div>
      {drafts.map((draft) => (
        <div key={draft.body} className="vdna-sample card">
          <div className="vdna-sample-head">
            <span className="vdna-pill">{t("voice.insights.draftOnly")}</span>
            {draft.subject && <b>{draft.subject}</b>}
            {draft.score !== null && (
              <span className="t-small vdna-score">
                {t("voice.insights.voiceScore", {
                  pct: Math.round(draft.score * 100),
                })}
              </span>
            )}
          </div>
          <p>{draft.body}</p>
        </div>
      ))}
      <p className="t-small vdna-disclosure">
        {t("voice.insights.disclosure")}
      </p>
    </div>
  );
}
