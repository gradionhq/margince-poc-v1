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
import "./voice-dna.css";

type VoiceProfile = components["schemas"]["VoiceProfile"];
type VoiceCorpusSource = components["schemas"]["VoiceCorpusSource"];

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

function useVoiceSources(profileId: string | undefined) {
  return useQuery({
    queryKey: ["voice-sources", profileId],
    enabled: Boolean(profileId),
    queryFn: async (): Promise<VoiceCorpusSource[]> => {
      const { data, error } = await api.GET("/voice-profiles/{id}/sources", {
        params: { path: { id: profileId as string } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
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

      <DerivedVoice profile={profile} />
      <PersonalityEditor profile={profile} onSaved={invalidate} />
      <CorpusSources profileId={profile.id as string} onChanged={invalidate} />
      <BuildControls profile={profile} onBuilt={invalidate} />
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
        {(list) =>
          list.length === 0 ? (
            <p className="t-small">{t("settings.voice.corpusEmpty")}</p>
          ) : (
            <ul className="vdna-list">
              {list.map((s) => (
                <li key={s.id} className="vdna-row">
                  <span>
                    {s.source_label} · {s.word_count.toLocaleString()}
                    {!s.included && ` · ${t("settings.voice.excluded")}`}
                  </span>
                  <button
                    type="button"
                    className="iconbtn"
                    aria-label={t("settings.voice.removeSource")}
                    style={{ marginLeft: "auto" }}
                    disabled={remove.isPending}
                    onClick={() => remove.mutate(s.id)}
                  >
                    <Trash2 aria-hidden />
                  </button>
                </li>
              ))}
            </ul>
          )
        }
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

// Build creates a durable background build; poll to a terminal state. A slow or
// budget-deferred build is honestly reported, not spun on forever.
function BuildControls({
  profile,
  onBuilt,
}: Readonly<{ profile: VoiceProfile; onBuilt: () => void }>) {
  const t = useT();
  const [status, setStatus] = useState<
    "succeeded" | "failed" | "deferred" | null
  >(null);
  const [error, setError] = useState<string | null>(null);

  const build = useMutation({
    mutationFn: async (): Promise<"succeeded" | "failed" | "deferred"> => {
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
      return "deferred";
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
