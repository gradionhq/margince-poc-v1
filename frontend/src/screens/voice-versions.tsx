import { useMutation, useQuery } from "@tanstack/react-query";
import { History, RotateCcw } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";
import { parseVoiceInsights, VoiceInsights } from "./voice-insights";
import "./voice-dna.css";

type VoiceProfileVersion = components["schemas"]["VoiceProfileVersion"];
type VoiceProfileDelta = components["schemas"]["VoiceProfileDelta"];
type VoiceLearningSummary = components["schemas"]["VoiceLearningSummary"];

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
      {error && <p className="t-small">{error}</p>}
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
  const versions = useVoiceVersions(profileId);
  const deltas = useQuery({
    queryKey: ["voice-deltas", profileId],
    queryFn: async (): Promise<VoiceProfileDelta[]> => {
      const { data, error } = await api.GET("/voice-profiles/{id}/deltas", {
        params: { path: { id: profileId } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
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
        {(list) =>
          list.length === 0 ? (
            <p className="t-small">{t("voice.history.empty")}</p>
          ) : (
            <ul className="vdna-list">
              {list.map((version) => (
                <VersionRow
                  key={version.id}
                  profileId={profileId}
                  version={version}
                  onChanged={onChanged}
                />
              ))}
            </ul>
          )
        }
      </QueryGate>
      <QueryGate query={deltas}>
        {(list) =>
          list.length === 0 ? null : (
            <div className="vdna-deltas">
              <div className="vdna-label">{t("voice.history.deltasLabel")}</div>
              <ul className="vdna-list">
                {list.map((delta) => (
                  <li key={delta.id} className="vdna-row">
                    <span>
                      {t("voice.history.deltaRow", {
                        from: delta.from_version ?? 0,
                        to: delta.to_version,
                      })}
                      {" · "}
                      {delta.classification} · {delta.activation_outcome}
                    </span>
                  </li>
                ))}
              </ul>
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
  return (
    <li className="vdna-row">
      <span>
        v{version.profile_version} · <Badge>{version.status}</Badge>
        {" · "}
        {new Date(version.created_at).toLocaleDateString()}
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
      {error && <span className="t-small">{error}</span>}
    </li>
  );
}
