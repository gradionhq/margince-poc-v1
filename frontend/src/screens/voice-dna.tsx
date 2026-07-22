import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Sparkles, Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";
import { ActiveVoiceInsights, VoiceHistory } from "./voice-versions";
import "./voice-dna.css";

type VoiceProfile = components["schemas"]["VoiceProfile"];
type VoiceCorpusSource = components["schemas"]["VoiceCorpusSource"];
type VoiceCorpusSummary = components["schemas"]["VoiceCorpusSummary"];

// The owner's single Voice DNA (listVoiceProfiles caps at one). Owner-private
// and human-only server-side; this card is the "…later in Settings" surface the
// onboarding Voice step promises.
function useVoiceProfile() {
  return useQuery({
    queryKey: ["voice-profile"],
    queryFn: async (): Promise<VoiceProfile | null> => {
      const { data, error } = await api.GET("/voice-profiles");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data[0] ?? null;
    },
  });
}

type CorpusManifest = {
  sources: VoiceCorpusSource[];
  summary: VoiceCorpusSummary;
};

function useVoiceSources(profileId: string | undefined) {
  return useQuery({
    queryKey: ["voice-sources", profileId],
    enabled: Boolean(profileId),
    queryFn: async (): Promise<CorpusManifest> => {
      const { data, error } = await api.GET("/voice-profiles/{id}/sources", {
        params: { path: { id: profileId as string } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return { sources: data.data, summary: data.summary };
    },
  });
}

// bandFor mirrors the server's §B1.4 thresholds so the removal warning can
// predict a drop before it happens; the server remains the authority.
function bandFor(totalWords: number): string {
  if (totalWords < 8000) {
    return "thin";
  }
  if (totalWords < 20000) {
    return "good";
  }
  if (totalWords < 30000) {
    return "rich";
  }
  return "sharp";
}

export function VoiceDnaCard() {
  const t = useT();
  const profile = useVoiceProfile();
  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader title={t("settings.voice.title")} />
      <p className="t-small" style={{ marginBottom: "var(--space-3)" }}>
        {t("settings.voice.intro")}
      </p>
      <QueryGate query={profile}>
        {(data) =>
          data ? (
            <VoiceDnaBody profile={data} />
          ) : (
            <EmptyState>
              <b>{t("settings.voice.emptyTitle")}</b>
              <p className="t-small">{t("settings.voice.emptyBody")}</p>
            </EmptyState>
          )
        }
      </QueryGate>
    </section>
  );
}

function bandLabel(
  t: ReturnType<typeof useT>,
  band: string | undefined,
): string {
  switch (band) {
    case "thin":
      return t("settings.voice.bandThin");
    case "good":
      return t("settings.voice.bandGood");
    case "rich":
      return t("settings.voice.bandRich");
    case "sharp":
      return t("settings.voice.bandSharp");
    default:
      return band ?? "";
  }
}

function VoiceDnaBody({ profile }: Readonly<{ profile: VoiceProfile }>) {
  const t = useT();
  const qc = useQueryClient();
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["voice-profile"] });
    qc.invalidateQueries({ queryKey: ["voice-sources", profile.id] });
    qc.invalidateQueries({ queryKey: ["voice-versions", profile.id] });
    qc.invalidateQueries({ queryKey: ["voice-deltas", profile.id] });
    qc.invalidateQueries({ queryKey: ["voice-learning", profile.id] });
  };
  return (
    <div>
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
          alignItems: "center",
          flexWrap: "wrap",
        }}
      >
        <Badge>{t(`settings.voice.status.${profile.status}`)}</Badge>
        {profile.quality_band && (
          <span className="t-small">{bandLabel(t, profile.quality_band)}</span>
        )}
        <span className="t-small" style={{ marginLeft: "auto" }}>
          {t("settings.voice.version", { n: profile.profile_version ?? 0 })}
        </span>
      </div>

      {profile.status === "ready" ? (
        <ActiveVoiceInsights
          profileId={profile.id as string}
          onChanged={invalidate}
        />
      ) : (
        <DerivedVoice profile={profile} />
      )}
      <PersonalityEditor profile={profile} onSaved={invalidate} />
      <CorpusSources profileId={profile.id as string} onChanged={invalidate} />
      <BuildControls profile={profile} onBuilt={invalidate} />
      <VoiceHistory profileId={profile.id as string} onChanged={invalidate} />
    </div>
  );
}

function DerivedVoice({ profile }: Readonly<{ profile: VoiceProfile }>) {
  const t = useT();
  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <div className="vdna-label">{t("settings.voice.derivedLabel")}</div>
      {profile.voice_profile_md ? (
        <p style={{ whiteSpace: "pre-wrap", lineHeight: 1.55 }}>
          {profile.voice_profile_md}
        </p>
      ) : (
        <p className="t-small">{t("settings.voice.derivedEmpty")}</p>
      )}
    </div>
  );
}

// personality_md is the owner-authored preferences the model output never
// overwrites; the PATCH is version-guarded (If-Match on the profile version).
function PersonalityEditor({
  profile,
  onSaved,
}: Readonly<{ profile: VoiceProfile; onSaved: () => void }>) {
  const t = useT();
  const [text, setText] = useState(profile.personality_md);
  const [error, setError] = useState<string | null>(null);
  const save = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.PATCH("/voice-profiles/{id}", {
        params: {
          path: { id: profile.id as string },
          header: { "If-Match": String(profile.version) },
        },
        body: { personality_md: text },
      });
      if (err) {
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: () => {
      setError(null);
      onSaved();
    },
    onError: (e: Error) => setError(e.message),
  });
  const dirty = text !== profile.personality_md;
  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <div className="vdna-label">{t("settings.voice.personalityLabel")}</div>
      <textarea
        className="textarea"
        rows={4}
        value={text}
        placeholder={t("settings.voice.personalityPlaceholder")}
        onChange={(e) => setText(e.target.value)}
      />
      {error && (
        <p className="t-small" style={{ marginTop: "var(--space-2)" }}>
          {error}
        </p>
      )}
      <Button
        small
        disabled={!dirty || save.isPending}
        onClick={() => save.mutate()}
        style={{ marginTop: "var(--space-2)" }}
      >
        {t("settings.voice.savePreferences")}
      </Button>
    </div>
  );
}

function CorpusSources({
  profileId,
  onChanged,
}: Readonly<{ profileId: string; onChanged: () => void }>) {
  const t = useT();
  const sources = useVoiceSources(profileId);
  const [paste, setPaste] = useState("");
  const [error, setError] = useState<string | null>(null);

  const add = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.POST("/voice-profiles/{id}/sources", {
        params: { path: { id: profileId } },
        body: {
          kind: "other",
          register: "general",
          weight: 1,
          source_label: t("settings.voice.pastedLabel"),
          source_ref: `settings:paste:${Date.now()}`,
          format: "text",
          content: paste,
        },
      });
      if (err) {
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: () => {
      setPaste("");
      setError(null);
      onChanged();
    },
    onError: (e: Error) => setError(e.message),
  });

  const remove = useMutation({
    mutationFn: async (sourceId: string) => {
      const { error: err } = await api.DELETE(
        "/voice-profiles/{id}/sources/{sourceId}",
        { params: { path: { id: profileId, sourceId } } },
      );
      if (err) {
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: onChanged,
    onError: (e: Error) => setError(e.message),
  });

  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <div className="vdna-label">{t("settings.voice.corpusLabel")}</div>
      <QueryGate query={sources}>
        {(manifest) => (
          <div>
            <p className="t-small">
              {t("settings.voice.meter", {
                count: manifest.summary.total_words.toLocaleString(),
                target: manifest.summary.target_words.toLocaleString(),
              })}
            </p>
            <RegisterMix summary={manifest.summary} />
            {manifest.sources.length === 0 ? (
              <p className="t-small">{t("settings.voice.corpusEmpty")}</p>
            ) : (
              <ul className="vdna-list">
                {manifest.sources.map((s) => (
                  <SourceRow
                    key={s.id}
                    source={s}
                    summary={manifest.summary}
                    pending={remove.isPending}
                    onRemove={() => remove.mutate(s.id)}
                  />
                ))}
              </ul>
            )}
          </div>
        )}
      </QueryGate>
      <textarea
        className="textarea"
        rows={3}
        value={paste}
        placeholder={t("settings.voice.addPlaceholder")}
        onChange={(e) => setPaste(e.target.value)}
        style={{ marginTop: "var(--space-2)" }}
      />
      {error && (
        <p className="t-small" style={{ marginTop: "var(--space-2)" }}>
          {error}
        </p>
      )}
      <Button
        small
        disabled={paste.trim().length === 0 || add.isPending}
        onClick={() => add.mutate()}
        style={{ marginTop: "var(--space-2)" }}
      >
        {t("settings.voice.addSource")}
      </Button>
    </div>
  );
}

// registerLabel names one closed-vocabulary register; an unknown value (a
// newer server) renders verbatim rather than crashing the card.
function registerLabel(t: ReturnType<typeof useT>, register: string): string {
  switch (register) {
    case "email":
      return t("settings.voice.register.email");
    case "social":
      return t("settings.voice.register.social");
    case "long_form":
      return t("settings.voice.register.long_form");
    case "spoken":
      return t("settings.voice.register.spoken");
    case "general":
      return t("settings.voice.register.general");
    default:
      return register;
  }
}

// RegisterMix shows where the corpus words come from; spoken sources are the
// highest-signal gap to name.
function RegisterMix({ summary }: Readonly<{ summary: VoiceCorpusSummary }>) {
  const t = useT();
  const entries = Object.entries(summary.register_words).filter(
    ([, words]) => words > 0,
  );
  if (entries.length === 0 || summary.total_words === 0) {
    return null;
  }
  return (
    <p className="t-small vdna-regmix">
      {entries
        .map(
          ([register, words]) =>
            `${registerLabel(t, register)} ${Math.round((words / summary.total_words) * 100)}%`,
        )
        .join(" · ")}
    </p>
  );
}

// Removing a source is armed-then-confirmed when it would drop the quality
// band: the warning names the drop before anything is deleted.
function SourceRow({
  source,
  summary,
  pending,
  onRemove,
}: Readonly<{
  source: VoiceCorpusSource;
  summary: VoiceCorpusSummary;
  pending: boolean;
  onRemove: () => void;
}>) {
  const t = useT();
  const [armed, setArmed] = useState(false);
  const bandAfter = bandFor(
    Math.max(0, summary.total_words - source.word_count),
  );
  const drops = source.included && bandAfter !== summary.quality_band;
  const handleRemove = () => {
    if (drops && !armed) {
      setArmed(true);
      return;
    }
    onRemove();
  };
  return (
    <li className="vdna-row">
      <span>
        {source.source_label} · {source.word_count.toLocaleString()}
        <span className="vdna-register">
          {registerLabel(t, source.register)}
        </span>
        {!source.included && ` · ${t("settings.voice.excluded")}`}
      </span>
      {armed && drops && (
        <span className="t-small vdna-banddrop">
          {t("settings.voice.bandDrop", {
            from: bandLabel(t, summary.quality_band),
            to: bandLabel(t, bandAfter),
          })}
        </span>
      )}
      <button
        type="button"
        className="iconbtn"
        aria-label={t("settings.voice.removeSource")}
        style={{ marginLeft: "auto" }}
        disabled={pending}
        onClick={handleRemove}
      >
        <Trash2 aria-hidden />
      </button>
    </li>
  );
}

// Build creates a durable background build; poll to a terminal state. A slow or
// budget-deferred build is honestly reported, not spun on forever.
function BuildControls({
  profile,
  onBuilt,
}: Readonly<{ profile: VoiceProfile; onBuilt: () => void }>) {
  const t = useT();
  const [status, setStatus] = useState<
    "succeeded" | "failed" | "deferred" | "pending" | null
  >(null);
  const [error, setError] = useState<string | null>(null);

  const build = useMutation({
    mutationFn: async (): Promise<
      "succeeded" | "failed" | "deferred" | "pending"
    > => {
      const created = await api.POST("/voice-profiles/{id}/builds", {
        params: { path: { id: profile.id as string } },
        body: { reason: "manual" },
      });
      if (created.error) {
        throw new Error(problemMessage(created.error));
      }
      const buildId = created.data.id;
      for (let attempt = 0; attempt < 40; attempt++) {
        const { data, error: err } = await api.GET(
          "/voice-profiles/{id}/builds/{buildId}",
          { params: { path: { id: profile.id as string, buildId } } },
        );
        if (err) {
          throw new Error(problemMessage(err));
        }
        if (
          data.status === "succeeded" ||
          data.status === "failed" ||
          data.status === "deferred"
        ) {
          return data.status;
        }
        await new Promise((resolve) => {
          globalThis.setTimeout(resolve, 1500);
        });
      }
      // Still queued/running after the poll budget — honestly "pending", not
      // "deferred" (which specifically means the AI budget snoozed it).
      return "pending";
    },
    onSuccess: (finalStatus) => {
      setStatus(finalStatus);
      setError(null);
      onBuilt();
    },
    onError: (e: Error) => setError(e.message),
  });

  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <Button
        variant="primary"
        small
        disabled={build.isPending}
        onClick={() => build.mutate()}
      >
        <Sparkles aria-hidden />{" "}
        {build.isPending
          ? t("settings.voice.building")
          : t("settings.voice.rebuild")}
      </Button>
      {status && (
        <p className="t-small" style={{ marginTop: "var(--space-2)" }}>
          {t(`settings.voice.buildStatus.${status}`)}
        </p>
      )}
      {error && (
        <p className="t-small" style={{ marginTop: "var(--space-2)" }}>
          {error}
        </p>
      )}
    </div>
  );
}
