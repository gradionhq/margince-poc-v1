import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GitMerge, Undo2, X } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";

// The dedupe review queue (M4, DH-EXT-1/2): confidence-sorted open pairs
// with the detection-time evidence the detector actually saw — never
// re-derived. Merge picks a winner and runs the ONE server-side merge;
// Not-a-duplicate suppresses the pair from every future sweep. Every
// number and every evidence line on this screen is a persisted row.

type Candidate = components["schemas"]["DedupeCandidate"];

const queueKey = ["dedupe-candidates"];

export function DedupeScreen() {
  const t = useT();
  const qc = useQueryClient();
  const queue = useQuery({
    queryKey: queueKey,
    queryFn: async () => {
      const { data, error } = await api.GET("/dedupe/candidates", {
        params: { query: { status: "open", limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const dispose = useMutation({
    mutationFn: async (input: {
      id: string;
      disposition: "merge" | "not_a_duplicate";
      winner_id?: string;
    }) => {
      const { data, error } = await api.POST(
        "/dedupe/candidates/{id}/disposition",
        {
          params: { path: { id: input.id } },
          body: { disposition: input.disposition, winner_id: input.winner_id },
        },
      );
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: queueKey }),
  });

  const undo = useMutation({
    mutationFn: async (id: string) => {
      const { data, error } = await api.POST("/dedupe/candidates/{id}/undo", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: queueKey }),
  });

  return (
    <div className="dedupe-screen">
      <h1>{t("dedupe.title")}</h1>
      <p className="t-small">{t("dedupe.intro")}</p>
      {queue.isPending && <p className="t-small">{t("dedupe.loading")}</p>}
      {queue.isError && (
        <p className="t-small dedupe-error">{queue.error.message}</p>
      )}
      {queue.data && queue.data.data.length === 0 && (
        <p className="t-small">{t("dedupe.empty")}</p>
      )}
      {(dispose.isError || undo.isError) && (
        <p className="t-small dedupe-error">
          {dispose.error?.message ?? undo.error?.message}
        </p>
      )}
      {queue.data?.data.map((c) => (
        <CandidateCard
          key={c.id}
          candidate={c}
          busy={dispose.isPending || undo.isPending}
          onDispose={(disposition, winner) =>
            dispose.mutate({ id: c.id, disposition, winner_id: winner })
          }
        />
      ))}
      {undo.data && (
        <p className="t-small">
          {t("dedupe.undone")}{" "}
          <button
            type="button"
            className="dedupe-undo"
            onClick={() => undo.reset()}
          >
            <X aria-hidden /> {t("dedupe.dismissNote")}
          </button>
        </p>
      )}
      {dispose.data && dispose.data.status !== "open" && (
        <p className="t-small">
          {t("dedupe.decided")}{" "}
          {dispose.data.status === "not_a_duplicate" && (
            <button
              type="button"
              className="dedupe-undo"
              disabled={undo.isPending}
              onClick={() => undo.mutate(dispose.data.id)}
            >
              <Undo2 aria-hidden /> {t("dedupe.undoCta")}
            </button>
          )}
        </p>
      )}
    </div>
  );
}

function CandidateCard({
  candidate,
  busy,
  onDispose,
}: {
  candidate: Candidate;
  busy: boolean;
  onDispose: (
    disposition: "merge" | "not_a_duplicate",
    winner?: string,
  ) => void;
}) {
  const t = useT();
  const [winner, setWinner] = useState<string>(candidate.left_id);
  const pct = Math.round(candidate.confidence * 100);

  return (
    <div className="dedupe-card">
      <div className="dedupe-head">
        <span className="dedupe-kind">
          {t(kindLabel(candidate.entity_type))}
        </span>
        <span className="dedupe-conf">
          {t("dedupe.confidence")} {pct}%
        </span>
      </div>
      <table className="dedupe-evidence">
        <thead>
          <tr>
            <th>{t("dedupe.field")}</th>
            <th>
              <label className="dedupe-pick">
                <input
                  type="radio"
                  name={`winner-${candidate.id}`}
                  checked={winner === candidate.left_id}
                  onChange={() => setWinner(candidate.left_id)}
                />
                {t("dedupe.left")}
              </label>
            </th>
            <th>
              <label className="dedupe-pick">
                <input
                  type="radio"
                  name={`winner-${candidate.id}`}
                  checked={winner === candidate.right_id}
                  onChange={() => setWinner(candidate.right_id)}
                />
                {t("dedupe.right")}
              </label>
            </th>
          </tr>
        </thead>
        <tbody>
          {candidate.evidence.map((e) => (
            <tr key={e.field} data-signal={e.signal}>
              <td>{e.field}</td>
              <td>{e.left_value ?? "—"}</td>
              <td>{e.right_value ?? "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="dedupe-actions">
        <Button
          variant="primary"
          disabled={busy}
          onClick={() => onDispose("merge", winner)}
        >
          <GitMerge aria-hidden /> {t("dedupe.mergeCta")}
        </Button>
        <Button
          variant="ghost"
          disabled={busy}
          onClick={() => onDispose("not_a_duplicate")}
        >
          {t("dedupe.notDuplicateCta")}
        </Button>
      </div>
    </div>
  );
}

function kindLabel(
  entityType: Candidate["entity_type"],
): "dedupe.kindPerson" | "dedupe.kindOrganization" {
  return entityType === "person"
    ? "dedupe.kindPerson"
    : "dedupe.kindOrganization";
}
