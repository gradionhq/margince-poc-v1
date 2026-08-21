import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GitMerge } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  Card,
  Radio,
  TableScroll,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";
import { EntityRef } from "./entityref";
import "./dedupe.css";

// The dedupe review queue (M4, DH-EXT-1/2): confidence-sorted open pairs
// with the detection-time evidence the detector actually saw — never
// re-derived. Merge picks a winner and runs the ONE server-side merge;
// Not-a-duplicate suppresses the pair from every future sweep. Every
// number and every evidence line on this screen is a persisted row.

type Candidate = components["schemas"]["DedupeCandidate"];

// The three signals the detector records (agree | collide | one_sided), in the
// reader's own language.
//
// A MAP rather than an object literal, for two reasons that are the same reason:
// `get` answers `MessageKey | undefined` with no cast, and a Map has no
// prototype keys to confuse a lookup — an object would answer `true` to
// "toString" or "constructor", and the wire types this field as a plain string
// rather than a closed enum, so those are values a server can actually send.
const SIGNAL_KEYS = new Map<string, MessageKey>([
  ["agree", "dedupe.signalAgree"],
  ["collide", "dedupe.signalCollide"],
  ["one_sided", "dedupe.signalOneSided"],
]);

function signalWord(signal: string, t: ReturnType<typeof useT>): string {
  const key = SIGNAL_KEYS.get(signal);
  return key === undefined ? signal : t(key);
}

const queueKey = ["dedupe-candidates"];

/**
 * The open duplicate queue, in one spelling.
 *
 * Exported because the screen is no longer the only reader: chrome that reports
 * what is waiting on a person reads the same queue, and two queries against one
 * path are two answers that can disagree on screen.
 */
export function useDedupeQueue() {
  return useQuery({
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
}

export function DedupeScreen() {
  const t = useT();
  const qc = useQueryClient();
  const queue = useDedupeQueue();

  const dispose = useMutation({
    // No single record: this one mutation serves the whole open queue, and
    // which pair is being decided is known only at mutate() time, not here.
    mutationKey: ["dedupe"],
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
        <div className="dedupe-queue arrive-stack">
          <SurfaceState
            state={queueState(queue.isPending, candidates.length)}
            emptyLabel={t("dedupe.empty")}
            loadingLabel={t("dedupe.loading")}
            // A pair of candidate cards, which is what one review looks like.
            loadingLines={6}
          >
            {candidates.map((c) => (
              <CandidateCard
                key={c.id}
                candidate={c}
                // The two states the README keeps apart. `deciding` names the
                // verb whose own write is in flight — react-query's `variables`
                // carry both the pair and the disposition it was fired for, so
                // only the button the reader actually pressed turns. `blocked` is
                // somebody else's write, and reads as refusal.
                deciding={
                  dispose.isPending && dispose.variables?.id === c.id
                    ? dispose.variables.disposition
                    : undefined
                }
                blocked={dispose.isPending || undo.isPending}
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
            // The design system's Button, not `.link-button`: this control
            // has TWO unavailable states and the class can only draw one of
            // them. Its own undo in flight is `pending` — aria-disabled, so
            // the reader keeps the focus they just put here. A disposition in
            // flight is refusal: that write is about to replace this notice
            // outright, and letting both run left the two onSuccess handlers
            // resetting each other's state, so the reader saw whichever
            // landed second.
            decided.status === "not_a_duplicate" ? (
              <Button
                small
                pending={undo.isPending}
                disabled={dispose.isPending}
                onClick={() => undo.mutate(decided.id)}
              >
                {t("dedupe.undoCta")}
              </Button>
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
  deciding,
  blocked,
  onDispose,
}: {
  candidate: Candidate;
  // Which of this pair's own verbs is mid-write, if either: that button keeps
  // focus and draws in full ink with a turning mark.
  deciding: "merge" | "not_a_duplicate" | undefined;
  // SOME write is in flight — this pair's, another pair's, or an undo. Every
  // verb except the one that is turning refuses until it lands, including this
  // pair's other verb: a reader who could still dismiss the pair they are
  // merging would have two dispositions racing on one row.
  blocked: boolean;
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
      {/* Both records by name, openable — the evidence rows show what the
          detector saw, and a reviewer often wants to see the whole record
          before deciding. */}
      <p className="t-caption dedupe-pair">
        <EntityRef kind={candidate.entity_type} id={candidate.left_id} />
        {" · "}
        <EntityRef kind={candidate.entity_type} id={candidate.right_id} />
      </p>
      {/* The design system's table, not a second one: DataTable cannot express
          either of the two things this table needs — a column header that IS
          the winner radio, and a row carrying the detector's signal — so the
          caller draws the rows, and `.table` / `TableScroll` keep the chrome
          one spelling. */}
      <TableScroll label={t("dedupe.evidenceTable")}>
        <table className="table dedupe-evidence">
          <thead>
            <tr>
              <th>{t("dedupe.field")}</th>
              <th>{t("dedupe.signal")}</th>
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
                {/* The signal in WORDS. Colour alone told `collide` apart, which
                    reaches nobody who cannot see the difference and nothing at
                    all in print — and it left the other two signals told apart
                    by nothing, so a reader could not tell "these agree" from
                    "only one side has it". The wire types this field as a plain
                    string rather than a closed enum, so a value this release has
                    no word for renders as itself: a signal we cannot name is
                    still one the detector acted on, and a blank cell would read
                    as no signal. */}
                <td className="dedupe-signal">{signalWord(e.signal, t)}</td>
                <td>{e.left_value ?? "—"}</td>
                <td>{e.right_value ?? "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableScroll>
      <div className="card-actions">
        <Button
          variant="primary"
          pending={deciding === "merge"}
          // The README is explicit that refusal outranks pending: a control
          // nobody may press cannot also be mid-press. So the turning button
          // takes `pending` alone and every other one takes the refusal.
          disabled={blocked && deciding !== "merge"}
          onClick={() => onDispose("merge", winner)}
        >
          <GitMerge aria-hidden /> {t("dedupe.mergeCta")}
        </Button>
        <Button
          variant="ghost"
          pending={deciding === "not_a_duplicate"}
          disabled={blocked && deciding !== "not_a_duplicate"}
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
): "dedupe.kindPerson" | "dedupe.kindOrganization" | "dedupe.kindLead" {
  switch (entityType) {
    case "person":
      return "dedupe.kindPerson";
    case "organization":
      return "dedupe.kindOrganization";
    case "lead":
      return "dedupe.kindLead";
  }
}
