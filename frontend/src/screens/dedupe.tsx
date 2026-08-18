import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GitMerge } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, Card, Radio } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import "./dedupe.css";

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
        throwProblem(error);
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
        throwProblem(error);
      }
      return data;
    },
    // A fresh decision replaces any lingering undo notice — the two
    // banners must never stack.
    onSuccess: () => {
      undo.reset();
      return qc.invalidateQueries({ queryKey: queueKey });
    },
  });

  const undo = useMutation({
    mutationFn: async (id: string) => {
      const { data, error } = await api.POST("/dedupe/candidates/{id}/undo", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    // Undoing clears the "decision saved" banner (and its stale Undo
    // button) along with it.
    onSuccess: () => {
      dispose.reset();
      return qc.invalidateQueries({ queryKey: queueKey });
    },
  });

  const candidates = queue.data?.data ?? [];
  // A decision that LANDED, narrowed once here rather than at each of the two
  // places the notice reads it: `status` is what says the server took it, and
  // an "open" pair came back undecided.
  const decided =
    dispose.data && dispose.data.status !== "open" ? dispose.data : undefined;
  const writeError = dispose.isError || undo.isError;

  return (
    <div className="wrap">
      {queue.isError && (
        <Callout tone="danger" live="alert" className="dedupe-notice">
          {problemMessageOf(queue.error, t)}
        </Callout>
      )}
      {writeError && (
        <Callout tone="danger" live="alert" className="dedupe-notice">
          {problemMessageOf(dispose.error ?? undo.error, t)}
        </Callout>
      )}
      {/* A failed read says what the server said and nothing else: drawn as one
          of SurfaceState's states it would either claim the queue is clear or
          replace the server's own sentence with the generic "could not be
          loaded". The three states below are the ones the read can honestly be
          in, and `empty` — the only one allowed to say there is none — is
          reached only once the queue has actually answered. */}
      {!queue.isError && (
        <div className="dedupe-queue">
          {/* SurfaceState's loading state is a shimmer bar, which carries no
              text at all — the same gap QueryStates covers with a spoken line
              beside its skeletons. Without it a reader who cannot see the bar
              hears nothing between mount and the first pair. */}
          {queue.isPending && (
            <span className="sr-only" role="status">
              {t("dedupe.loading")}
            </span>
          )}
          <SurfaceState
            state={queueState(queue.isPending, candidates.length)}
            emptyLabel={t("dedupe.empty")}
          >
            {candidates.map((c) => (
              <CandidateCard
                key={c.id}
                candidate={c}
                busy={dispose.isPending || undo.isPending}
                onDispose={(disposition, winner) =>
                  dispose.mutate({ id: c.id, disposition, winner_id: winner })
                }
              />
            ))}
          </SurfaceState>
        </div>
      )}
      {undo.data && (
        <Callout
          tone="success"
          live="status"
          className="dedupe-notice"
          actions={
            // No glyph: `.link-button` is a TEXT affordance and carries no
            // icon-sizing rule of its own (unlike `.btn` / `.iconbtn`), so a
            // lucide default of 24px lands above a 12px label. Sizing it here
            // would be the per-caller drift base.css warns about.
            <button
              type="button"
              className="link-button"
              onClick={() => undo.reset()}
            >
              {t("dedupe.dismissNote")}
            </button>
          }
        >
          {t("dedupe.undone")}
        </Callout>
      )}
      {decided && (
        <Callout
          tone="success"
          live="status"
          className="dedupe-notice"
          actions={
            decided.status === "not_a_duplicate" ? (
              <button
                type="button"
                className="link-button"
                disabled={undo.isPending}
                onClick={() => undo.mutate(decided.id)}
              >
                {t("dedupe.undoCta")}
              </button>
            ) : undefined
          }
        >
          {t("dedupe.decided")}
        </Callout>
      )}
    </div>
  );
}

// Which of the three states the queue read is in. Pending is `loading` rather
// than an empty list, because a read still in flight knows nothing about how
// many pairs are waiting.
function queueState(pending: boolean, count: number): SectionState {
  if (pending) {
    return "loading";
  }
  return count === 0 ? "empty" : "ready";
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
    // level={2}: the shell's head carries the h1 and this screen's own title is
    // the h2 above, so a pair is a section INSIDE that rather than beside it.
    <Card
      as="article"
      level={2}
      title={t(kindLabel(candidate.entity_type))}
      actions={
        <Badge>
          {t("dedupe.confidence")} {pct}%
        </Badge>
      }
    >
      {/* The design system's table, not a second one: DataTable cannot express
          either of the two things this table needs — a column header that IS
          the winner radio, and a row carrying the detector's signal — so the
          caller draws the rows and `.table` / `.table-scroll` keep the chrome
          one spelling. */}
      <div className="table-scroll">
        <table className="table dedupe-evidence">
          <thead>
            <tr>
              <th>{t("dedupe.field")}</th>
              <th>
                <Radio
                  name={`winner-${candidate.id}`}
                  checked={winner === candidate.left_id}
                  onChange={() => setWinner(candidate.left_id)}
                  label={t("dedupe.left")}
                />
              </th>
              <th>
                <Radio
                  name={`winner-${candidate.id}`}
                  checked={winner === candidate.right_id}
                  onChange={() => setWinner(candidate.right_id)}
                  label={t("dedupe.right")}
                />
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
      </div>
      <div className="card-actions">
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
    </Card>
  );
}

function kindLabel(
  entityType: Candidate["entity_type"],
): "dedupe.kindPerson" | "dedupe.kindOrganization" {
  return entityType === "person"
    ? "dedupe.kindPerson"
    : "dedupe.kindOrganization";
}
