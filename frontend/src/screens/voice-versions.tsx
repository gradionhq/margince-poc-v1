import { useMutation, useQuery } from "@tanstack/react-query";
import { History, RotateCcw } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button } from "../design-system/atoms";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";
import { parseVoiceInsights, VoiceInsights } from "./voice-insights";
import "./voice-dna.css";

type VoiceProfileVersion = components["schemas"]["VoiceProfileVersion"];
type VoiceProfileDelta = components["schemas"]["VoiceProfileDelta"];
type VoiceLearningSummary = components["schemas"]["VoiceLearningSummary"];

type VersionsPage = { items: VoiceProfileVersion[]; next: string | null };
type DeltasPage = { items: VoiceProfileDelta[]; next: string | null };

// mergeById accumulates keyset pages without duplicating rows when a page
// is refetched after invalidation.
function mergeById<T extends { id: string }>(prev: T[], page: T[]): T[] {
  const seen = new Set(page.map((item) => item.id));
  return [...prev.filter((item) => !seen.has(item.id)), ...page];
}

export function useVoiceVersions(profileId: string | undefined) {
  return useQuery({
    queryKey: ["voice-versions", profileId],
    enabled: Boolean(profileId),
    queryFn: async (): Promise<VoiceProfileVersion[]> => {
      const { data, error } = await api.GET("/voice-profiles/{id}/versions", {
        params: { path: { id: profileId as string } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
}

// ActiveVoiceInsights renders the impress surface from the active version
// and, when a candidate awaits review, the apply/reject banner above it.
export function ActiveVoiceInsights({
  profileId,
  onChanged,
}: Readonly<{ profileId: string; onChanged: () => void }>) {
  const versions = useVoiceVersions(profileId);
  return (
    <QueryGate query={versions}>
      {(list) => {
        const active = list.find((version) => version.status === "active");
        const candidate = list.find(
          (version) => version.status === "candidate",
        );
        return (
          <div>
            {candidate && (
              <CandidateBanner
                profileId={profileId}
                candidate={candidate}
                onChanged={onChanged}
              />
            )}
            {active && (
              <VoiceInsights
                data={parseVoiceInsights(active)}
                profileVersion={active.profile_version}
              />
            )}
          </div>
        );
      }}
    </QueryGate>
  );
}

// A candidate never replaces the active voice silently: the owner applies
// or rejects it, with the evaluator's reasons in view.
function CandidateBanner({
  profileId,
  candidate,
  onChanged,
}: Readonly<{
  profileId: string;
  candidate: VoiceProfileVersion;
  onChanged: () => void;
}>) {
  const t = useT();
  const [error, setError] = useState<string | null>(null);
  const transition = useMutation({
    mutationFn: async (action: "apply" | "reject") => {
      const path =
        action === "apply"
          ? ("/voice-profiles/{id}/versions/{profileVersion}/apply" as const)
          : ("/voice-profiles/{id}/versions/{profileVersion}/reject" as const);
      const { error: err } = await api.POST(path, {
        params: {
          path: { id: profileId, profileVersion: candidate.profile_version },
          header: { "If-Match": String(candidate.version) },
        },
      });
      if (err) {
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: () => {
      setError(null);
      onChanged();
    },
    onError: (e: Error) => setError(e.message),
  });
  return (
    <div className="vdna-candidate card">
      <b>{t("voice.candidate.title", { n: candidate.profile_version })}</b>
      {candidate.review_reasons.length > 0 && (
        <ul className="vdna-reasons">
          {candidate.review_reasons.map((reason) => (
            <li key={reason}>{reason}</li>
          ))}
        </ul>
      )}
      {error && (
        <p className="t-small" role="alert">
          {error}
        </p>
      )}
      <div className="vdna-candidate-acts">
        <Button
          variant="primary"
          small
          disabled={transition.isPending}
          onClick={() => transition.mutate("apply")}
        >
          {t("voice.candidate.apply")}
        </Button>
        <Button
          small
          disabled={transition.isPending}
          onClick={() => transition.mutate("reject")}
        >
          {t("voice.candidate.reject")}
        </Button>
      </div>
    </div>
  );
}

// VoiceHistory is the append-only record: versions with rollback, the
// "what changed" delta timeline, and the learning-signal counters.
export function VoiceHistory({
  profileId,
  onChanged,
}: Readonly<{ profileId: string; onChanged: () => void }>) {
  const t = useT();
  const [versionCursor, setVersionCursor] = useState<string | undefined>();
  const [deltaCursor, setDeltaCursor] = useState<string | undefined>();
  const versions = useQuery({
    queryKey: ["voice-versions", profileId, versionCursor ?? ""],
    queryFn: async (): Promise<VersionsPage> => {
      const { data, error } = await api.GET("/voice-profiles/{id}/versions", {
        params: {
          path: { id: profileId },
          query: versionCursor ? { cursor: versionCursor } : {},
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      setAllVersions((prev) => mergeById(prev, data.data));
      return { items: data.data, next: data.page.next_cursor ?? null };
    },
  });
  const [allVersions, setAllVersions] = useState<VoiceProfileVersion[]>([]);
  const deltas = useQuery({
    queryKey: ["voice-deltas", profileId, deltaCursor ?? ""],
    queryFn: async (): Promise<DeltasPage> => {
      const { data, error } = await api.GET("/voice-profiles/{id}/deltas", {
        params: {
          path: { id: profileId },
          query: deltaCursor ? { cursor: deltaCursor } : {},
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      setAllDeltas((prev) => mergeById(prev, data.data));
      return { items: data.data, next: data.page.next_cursor ?? null };
    },
  });
  const [allDeltas, setAllDeltas] = useState<VoiceProfileDelta[]>([]);
  const learning = useQuery({
    queryKey: ["voice-learning", profileId],
    queryFn: async (): Promise<VoiceLearningSummary> => {
      const { data, error } = await api.GET("/voice-profiles/{id}/learning", {
        params: { path: { id: profileId } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <div className="vdna-label">
        <History aria-hidden /> {t("voice.history.label")}
      </div>
      <QueryGate query={versions}>
        {(page) =>
          allVersions.length === 0 ? (
            <p className="t-small">{t("voice.history.empty")}</p>
          ) : (
            <div>
              <ul className="vdna-list">
                {[...allVersions]
                  .sort((a, b) => b.profile_version - a.profile_version)
                  .map((version) => (
                    <VersionRow
                      key={version.id}
                      profileId={profileId}
                      version={version}
                      onChanged={onChanged}
                    />
                  ))}
              </ul>
              {page.next && (
                <Button
                  small
                  onClick={() => setVersionCursor(page.next ?? undefined)}
                >
                  {t("voice.history.loadMore")}
                </Button>
              )}
            </div>
          )
        }
      </QueryGate>
      <QueryGate query={deltas}>
        {(page) =>
          allDeltas.length === 0 ? null : (
            <div className="vdna-deltas">
              <div className="vdna-label">{t("voice.history.deltasLabel")}</div>
              <ul className="vdna-list">
                {[...allDeltas]
                  .sort((a, b) => b.to_version - a.to_version)
                  .map((delta) => (
                    <li key={delta.id} className="vdna-row">
                      <span>
                        {t("voice.history.deltaRow", {
                          from: delta.from_version ?? 0,
                          to: delta.to_version,
                        })}
                        {" · "}
                        {classificationLabel(t, delta.classification)} ·{" "}
                        {outcomeLabel(t, delta.activation_outcome)}
                      </span>
                    </li>
                  ))}
              </ul>
              {page.next && (
                <Button
                  small
                  onClick={() => setDeltaCursor(page.next ?? undefined)}
                >
                  {t("voice.history.loadMore")}
                </Button>
              )}
            </div>
          )
        }
      </QueryGate>
      <QueryGate query={learning}>
        {(summary) => (
          <p className="t-small vdna-learning">
            {t("voice.history.learning", {
              drafted: summary.drafted,
              edited: summary.edited_sent,
              rejected: summary.rejected,
            })}
          </p>
        )}
      </QueryGate>
    </div>
  );
}

function VersionRow({
  profileId,
  version,
  onChanged,
}: Readonly<{
  profileId: string;
  version: VoiceProfileVersion;
  onChanged: () => void;
}>) {
  const t = useT();
  const [error, setError] = useState<string | null>(null);
  const rollback = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.POST(
        "/voice-profiles/{id}/versions/{profileVersion}/rollback",
        {
          params: {
            path: { id: profileId, profileVersion: version.profile_version },
          },
        },
      );
      if (err) {
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: () => {
      setError(null);
      onChanged();
    },
    onError: (e: Error) => setError(e.message),
  });
  const { locale } = useLocale();
  return (
    <li className="vdna-row">
      <span>
        {t("voice.history.versionRow", { n: version.profile_version })}{" "}
        <Badge>{versionStatusLabel(t, version.status)}</Badge>
        {" · "}
        {new Date(version.created_at).toLocaleDateString(locale)}
      </span>
      {version.status === "superseded" && (
        <button
          type="button"
          className="iconbtn"
          aria-label={t("voice.history.rollback", {
            n: version.profile_version,
          })}
          style={{ marginLeft: "auto" }}
          disabled={rollback.isPending}
          onClick={() => rollback.mutate()}
        >
          <RotateCcw aria-hidden />
        </button>
      )}
      {error && (
        <span className="t-small" role="alert">
          {error}
        </span>
      )}
    </li>
  );
}

// The wire vocabularies rendered through i18n; an unknown value (a newer
// server) renders verbatim rather than hiding the row.
function versionStatusLabel(
  t: ReturnType<typeof useT>,
  status: string,
): string {
  switch (status) {
    case "active":
      return t("voice.status.active");
    case "candidate":
      return t("voice.status.candidate");
    case "superseded":
      return t("voice.status.superseded");
    case "rejected":
      return t("voice.status.rejected");
    default:
      return status;
  }
}

function classificationLabel(
  t: ReturnType<typeof useT>,
  value: string,
): string {
  switch (value) {
    case "routine":
      return t("voice.classification.routine");
    case "material":
      return t("voice.classification.material");
    default:
      return value;
  }
}

function outcomeLabel(t: ReturnType<typeof useT>, value: string): string {
  switch (value) {
    case "auto_activated":
      return t("voice.outcome.autoActivated");
    case "review_required":
      return t("voice.outcome.reviewRequired");
    case "manually_activated":
      return t("voice.outcome.manuallyActivated");
    case "rejected":
      return t("voice.outcome.rejected");
    case "rollback":
      return t("voice.outcome.rollback");
    default:
      return value;
  }
}
