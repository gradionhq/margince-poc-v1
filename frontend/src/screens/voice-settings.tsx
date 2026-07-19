import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, History, RefreshCw, ShieldCheck } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, SectionHeader } from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage } from "./common";
import "./voice-settings.css";

type VoiceProfile = components["schemas"]["VoiceProfile"];
type VoiceSource = components["schemas"]["VoiceCorpusSource"];
type VoiceVersion = components["schemas"]["VoiceProfileVersion"];

async function ownVoiceProfile(): Promise<VoiceProfile | null> {
  const { data, error } = await api.GET("/voice-profiles", {
    params: { query: { limit: 10 } },
  });
  if (error) {
    throw new Error(problemMessage(error));
  }
  return data.data[0] ?? null;
}

export function VoiceSettingsCard() {
  const t = useT();
  const queryClient = useQueryClient();
  const profileQuery = useQuery({
    queryKey: ["voice-profiles", "mine"],
    queryFn: ownVoiceProfile,
  });
  const create = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/voice-profiles", {
        body: { scope: "user", personality_md: "" },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (profile) =>
      queryClient.setQueryData(["voice-profiles", "mine"], profile),
  });

  if (profileQuery.isPending) {
    return <section className="card voice-admin">{t("voice.loading")}</section>;
  }
  if (profileQuery.error) {
    return (
      <section className="card voice-admin error">
        {profileQuery.error.message}
      </section>
    );
  }
  if (!profileQuery.data) {
    return (
      <section className="card voice-admin">
        <SectionHeader title={t("voice.title")} sub={t("voice.emptySub")} />
        <Button
          variant="primary"
          disabled={create.isPending}
          onClick={() => create.mutate()}
        >
          {t("voice.create")}
        </Button>
        {create.error && <p className="voice-error">{create.error.message}</p>}
      </section>
    );
  }
  return <VoiceProfileAdmin profile={profileQuery.data} />;
}

function VoiceProfileAdmin({ profile }: Readonly<{ profile: VoiceProfile }>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const [personality, setPersonality] = useState(profile.personality_md);
  const [buildID, setBuildID] = useState<string | null>(null);
  const [clearOpen, setClearOpen] = useState(false);
  const [buildRefreshError, setBuildRefreshError] = useState<string | null>(
    null,
  );

  useEffect(
    () => setPersonality(profile.personality_md),
    [profile.personality_md],
  );

  const sources = useQuery({
    queryKey: ["voice-sources", profile.id],
    queryFn: async () => {
      const { data, error } = await api.GET("/voice-profiles/{id}/sources", {
        params: { path: { id: profile.id } },
      });
      if (error) throw new Error(problemMessage(error));
      return data;
    },
  });
  const versions = useQuery({
    queryKey: ["voice-versions", profile.id],
    queryFn: async () => {
      const { data, error } = await api.GET("/voice-profiles/{id}/versions", {
        params: { path: { id: profile.id } },
      });
      if (error) throw new Error(problemMessage(error));
      return data.data;
    },
  });
  const deltas = useQuery({
    queryKey: ["voice-deltas", profile.id],
    queryFn: async () => {
      const { data, error } = await api.GET("/voice-profiles/{id}/deltas", {
        params: { path: { id: profile.id } },
      });
      if (error) throw new Error(problemMessage(error));
      return data.data;
    },
  });

  const refresh = useCallback(async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["voice-profiles", "mine"] }),
      queryClient.invalidateQueries({
        queryKey: ["voice-sources", profile.id],
      }),
      queryClient.invalidateQueries({
        queryKey: ["voice-versions", profile.id],
      }),
      queryClient.invalidateQueries({ queryKey: ["voice-deltas", profile.id] }),
    ]);
  }, [profile.id, queryClient]);

  const update = useMutation({
    mutationFn: async (next: { personality: string; automatic: boolean }) => {
      const { data, error } = await api.PATCH("/voice-profiles/{id}", {
        params: {
          path: { id: profile.id },
          header: { "If-Match": String(profile.version ?? 1) },
        },
        body: {
          personality_md: next.personality,
          auto_learning_enabled: next.automatic,
        },
      });
      if (error) throw new Error(problemMessage(error));
      return data;
    },
    onSuccess: (updated) =>
      queryClient.setQueryData(["voice-profiles", "mine"], updated),
  });
  const sourceUpdate = useMutation({
    mutationFn: async ({
      source,
      excluded,
      weight,
    }: {
      source: VoiceSource;
      excluded?: boolean;
      weight?: number;
    }) => {
      const { error } = await api.PATCH(
        "/voice-profiles/{id}/sources/{sourceId}",
        {
          params: { path: { id: profile.id, sourceId: source.id } },
          body: {
            excluded: excluded ?? source.excluded,
            exclusion_reason: excluded === true ? "user" : null,
            weight: weight ?? source.weight,
          },
        },
      );
      if (error) throw new Error(problemMessage(error));
    },
    onSuccess: refresh,
  });
  const build = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/voice-profiles/{id}/builds", {
        params: { path: { id: profile.id } },
        body: { reason: "manual" },
      });
      if (error) throw new Error(problemMessage(error));
      setBuildID(data.id);
      return data;
    },
  });
  const buildStatus = useQuery({
    queryKey: ["voice-build", profile.id, buildID],
    enabled: buildID !== null,
    queryFn: async () => {
      if (!buildID) throw new Error(t("voice.buildMissing"));
      const { data, error } = await api.GET(
        "/voice-profiles/{id}/builds/{buildId}",
        { params: { path: { id: profile.id, buildId: buildID } } },
      );
      if (error) throw new Error(problemMessage(error));
      return data;
    },
    refetchInterval: (query) =>
      query.state.data?.status === "queued" ||
      query.state.data?.status === "running"
        ? 1200
        : false,
  });
  useEffect(() => {
    if (buildStatus.data?.status === "succeeded") {
      refresh().catch((refreshFailure: Error) =>
        setBuildRefreshError(refreshFailure.message),
      );
    }
  }, [buildStatus.data?.status, refresh]);

  const rollback = useMutation({
    mutationFn: async (version: number) => {
      const { error } = await api.POST(
        "/voice-profiles/{id}/versions/{profileVersion}/rollback",
        { params: { path: { id: profile.id, profileVersion: version } } },
      );
      if (error) throw new Error(problemMessage(error));
    },
    onSuccess: refresh,
  });
  const clear = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/voice-profiles/{id}/corpus", {
        params: { path: { id: profile.id } },
      });
      if (error) throw new Error(problemMessage(error));
    },
    onSuccess: async () => {
      setClearOpen(false);
      await refresh();
    },
  });

  const summary = sources.data?.summary;
  const busy =
    build.isPending ||
    buildStatus.data?.status === "queued" ||
    buildStatus.data?.status === "running";
  const lastBuild = profile.last_built_at
    ? formatDateTime(profile.last_built_at, locale, "Europe/Berlin")
    : t("voice.never");
  const error =
    update.error ??
    sourceUpdate.error ??
    build.error ??
    buildStatus.error ??
    rollback.error;

  return (
    <div className="voice-admin">
      <section className="card">
        <SectionHeader title={t("voice.title")} sub={t("voice.sub")} />
        <div className="voice-stat-grid">
          <VoiceStat label={t("voice.status")} value={profile.status} />
          <VoiceStat
            label={t("voice.words")}
            value={String(summary?.total_words ?? 0)}
          />
          <VoiceStat
            label={t("voice.version")}
            value={String(profile.profile_version)}
          />
          <VoiceStat label={t("voice.lastBuild")} value={lastBuild} />
        </div>
        <label className="voice-switch">
          <input
            type="checkbox"
            checked={profile.auto_learning_enabled}
            disabled={update.isPending}
            onChange={(event) =>
              update.mutate({ personality, automatic: event.target.checked })
            }
          />
          <span>
            <strong>{t("voice.automatic")}</strong>
            <small>{t("voice.automaticSub")}</small>
          </span>
        </label>
      </section>

      <section className="card">
        <SectionHeader
          title={t("voice.preferences")}
          sub={t("voice.preferencesSub")}
        />
        <textarea
          className="voice-textarea"
          value={personality}
          onChange={(event) => setPersonality(event.target.value)}
          placeholder={t("voice.preferencesPlaceholder")}
        />
        <Button
          variant="primary"
          disabled={update.isPending || personality === profile.personality_md}
          onClick={() =>
            update.mutate({
              personality: personality.trim(),
              automatic: profile.auto_learning_enabled,
            })
          }
        >
          {t("voice.save")}
        </Button>
      </section>

      <section className="card">
        <SectionHeader title={t("voice.derived")} sub={t("voice.derivedSub")} />
        <VoiceProfilePreview markdown={profile.voice_profile_md} />
        <Button
          variant="primary"
          disabled={busy || (summary?.total_words ?? 0) < 800}
          onClick={() => build.mutate()}
        >
          <RefreshCw aria-hidden />{" "}
          {busy ? t("voice.building") : t("voice.rebuild")}
        </Button>
        {buildStatus.data?.failure_detail && (
          <p className="voice-error">{buildStatus.data.failure_detail}</p>
        )}
      </section>

      <section className="card">
        <SectionHeader title={t("voice.sources")} sub={t("voice.sourcesSub")} />
        <AddVoiceSource profileID={profile.id} onAdded={refresh} />
        {sources.isPending && <p>{t("voice.loading")}</p>}
        {sources.data?.data.length === 0 && (
          <p className="t-caption">{t("voice.noSources")}</p>
        )}
        <div className="voice-source-list">
          {sources.data?.data.map((source) => (
            <div className="voice-source" key={source.id}>
              <div>
                <strong>{source.source_label}</strong>
                <small>
                  {source.kind} · {source.word_count} {t("voice.wordsLower")} ·{" "}
                  {source.origin}
                </small>
              </div>
              <select
                aria-label={t("voice.weightFor", {
                  source: source.source_label,
                })}
                value={source.weight}
                disabled={sourceUpdate.isPending}
                onChange={(event) =>
                  sourceUpdate.mutate({
                    source,
                    weight: Number(event.target.value),
                  })
                }
              >
                <option value="0.5">0.5×</option>
                <option value="1">1×</option>
                <option value="2">2×</option>
              </select>
              <Button
                small
                onClick={() =>
                  sourceUpdate.mutate({ source, excluded: !source.excluded })
                }
              >
                {source.excluded ? t("voice.include") : t("voice.exclude")}
              </Button>
            </div>
          ))}
        </div>
      </section>

      <section className="card">
        <SectionHeader title={t("voice.history")} sub={t("voice.historySub")} />
        <div className="voice-version-list">
          {versions.data?.map((version: VoiceVersion) => (
            <div className="voice-version" key={version.id}>
              <History aria-hidden />
              <span>
                <strong>
                  {t("voice.versionNumber", {
                    version: version.profile_version,
                  })}
                </strong>
                <small>
                  {version.reason} ·{" "}
                  {formatDateTime(version.created_at, locale, "Europe/Berlin")}
                </small>
              </span>
              {version.active ? (
                <Badge tone="success">
                  <CheckCircle2 aria-hidden /> {t("voice.active")}
                </Badge>
              ) : (
                <Button
                  small
                  disabled={rollback.isPending}
                  onClick={() => rollback.mutate(version.profile_version)}
                >
                  {t("voice.rollback")}
                </Button>
              )}
            </div>
          ))}
        </div>
        {(deltas.data?.length ?? 0) > 0 && (
          <details className="voice-deltas">
            <summary>{t("voice.changes")}</summary>
            {deltas.data?.map((delta) => (
              <p key={delta.id}>
                {t("voice.changeVersion", {
                  from: delta.from_version,
                  to: delta.to_version,
                })}
              </p>
            ))}
          </details>
        )}
      </section>

      <section className="card voice-danger-zone">
        <ShieldCheck aria-hidden />
        <div>
          <strong>{t("voice.clearTitle")}</strong>
          <p>{t("voice.clearSub")}</p>
        </div>
        <Button variant="danger" onClick={() => setClearOpen(true)}>
          {t("voice.clear")}
        </Button>
      </section>

      {error && <p className="voice-error">{error.message}</p>}
      {buildRefreshError && <p className="voice-error">{buildRefreshError}</p>}
      <ConfirmModal
        open={clearOpen}
        onClose={() => setClearOpen(false)}
        title={t("voice.clearTitle")}
        confirmLabel={t("voice.clear")}
        confirmVariant="danger"
        pending={clear.isPending}
        error={clear.error?.message}
        onConfirm={() => clear.mutate()}
      >
        <p>{t("voice.clearConfirm")}</p>
      </ConfirmModal>
    </div>
  );
}

function VoiceStat({
  label,
  value,
}: Readonly<{ label: string; value: string }>) {
  return (
    <div className="voice-stat">
      <small>{label}</small>
      <strong>{value}</strong>
    </div>
  );
}

function VoiceProfilePreview({ markdown }: Readonly<{ markdown: string }>) {
  const t = useT();
  if (markdown === "") {
    return <p className="t-caption">{t("voice.noProfile")}</p>;
  }
  return <pre className="voice-profile-preview">{markdown}</pre>;
}

function AddVoiceSource({
  profileID,
  onAdded,
}: Readonly<{ profileID: string; onAdded: () => Promise<void> }>) {
  const t = useT();
  const [text, setText] = useState("");
  const add = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST("/voice-profiles/{id}/sources", {
        params: { path: { id: profileID } },
        body: {
          kind: "longform",
          register: "written",
          source_label: t("voice.manualSource"),
          content: text.trim(),
          format: "txt",
        },
      });
      if (error) throw new Error(problemMessage(error));
    },
    onSuccess: async () => {
      setText("");
      await onAdded();
    },
  });
  return (
    <div className="voice-add-source">
      <textarea
        className="voice-textarea"
        value={text}
        onChange={(event) => setText(event.target.value)}
        placeholder={t("voice.addPlaceholder")}
      />
      <Button
        disabled={add.isPending || text.trim() === ""}
        onClick={() => add.mutate()}
      >
        {t("voice.addSource")}
      </Button>
      {add.error && <p className="voice-error">{add.error.message}</p>}
    </div>
  );
}
