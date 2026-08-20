import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { Button } from "../design-system/atoms";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import { ProblemError, problemMessageOf, throwProblem } from "./common";
import { useRoster } from "./entityref";

type Deal = components["schemas"]["Deal"];
type Stage = components["schemas"]["Stage"];

/** One row's outcome in a fan-out: it went through, or it did not and why. */
export type DealBulkOutcome = { id: string; name: string; error?: string };

/**
 * Bulk verbs over selected deals: assign an owner, move to a stage, archive.
 *
 * Every verb is a client-side fan-out of the record's own write — there is no
 * bulk endpoint, and inventing one would bypass the per-row version guard.
 * Each row sends ITS OWN If-Match from the version the list holds; one version
 * copied across the selection would conflict on every row but the one it came
 * from. A row that moved under the reader answers 409, is reported by name,
 * and can be retried once the list has refetched.
 *
 * Only OPEN stages are offered. Closing a deal asks for a lost reason and
 * freezes an exchange rate, and doing that to a dozen deals behind one button
 * — with one reason standing for all of them — is not a thing this bar should
 * make easy.
 */
export function DealBulkBar({
  deals,
  stages,
  onDone,
}: Readonly<{
  /** The selected rows, with the versions the list currently holds. */
  deals: readonly Deal[];
  /** The pipeline's stages; the terminal ones are filtered out here. */
  stages: readonly Stage[];
  /** Called after any run — the caller refetches and clears the selection. */
  onDone: (outcomes: readonly DealBulkOutcome[]) => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [ownerId, setOwnerId] = useState("");
  const [stageId, setStageId] = useState("");
  const roster = useRoster("user", true);
  const [outcomes, setOutcomes] = useState<readonly DealBulkOutcome[]>([]);
  const openStages = stages.filter((stage) => stage.semantic === "open");

  const run = useMutation({
    mutationFn: async ({
      write,
    }: {
      write: (deal: Deal) => Promise<void>;
    }): Promise<DealBulkOutcome[]> =>
      // Sequential, not Promise.all: a bulk verb over a working list is a
      // handful of rows, and a burst of concurrent writes against one
      // reader's own deals buys nothing but contention.
      deals.reduce<Promise<DealBulkOutcome[]>>(async (acc, deal) => {
        const done = await acc;
        const name = deal.name;
        try {
          await write(deal);
          done.push({ id: deal.id, name });
        } catch (error) {
          done.push({
            id: deal.id,
            name,
            error:
              error instanceof ProblemError
                ? problemMessageOf(error, t)
                : t("deals.bulkFailedRow"),
          });
        }
        return done;
      }, Promise.resolve([])),
    onSuccess: async (result) => {
      // Awaited: the rows that refused keep their selection so they can be
      // retried, and a retry that fired before the refetch landed would
      // resend the very version that just conflicted. The run stays pending
      // — and the verbs disabled — until the list holds fresh versions.
      await queryClient.invalidateQueries({ queryKey: ["deals"] });
      setOutcomes(result);
      onDone(result);
    },
  });

  const assign = () =>
    run.mutate({
      write: async (deal) => {
        const { error } = await api.PATCH("/deals/{id}", {
          params: {
            path: { id: deal.id },
            ...ifMatch(requireVersion(deal.version)),
          },
          body: { owner_id: ownerId },
        });
        if (error) {
          throwProblem(error, t);
        }
      },
    });

  const moveStage = () =>
    run.mutate({
      write: async (deal) => {
        const { error } = await api.POST("/deals/{id}/advance", {
          params: {
            path: { id: deal.id },
            ...ifMatch(requireVersion(deal.version)),
          },
          body: { to_stage_id: stageId },
        });
        if (error) {
          throwProblem(error, t);
        }
      },
    });

  const archive = () =>
    run.mutate({
      write: async (deal) => {
        const { error } = await api.DELETE("/deals/{id}", {
          params: { path: { id: deal.id } },
        });
        if (error) {
          throwProblem(error, t);
        }
      },
    });

  const failed = outcomes.filter((outcome) => outcome.error);
  const busy = run.isPending || deals.length === 0;
  return (
    <>
      <span className="t-caption">
        {t("deals.bulkSelected", { count: deals.length })}
      </span>
      <Select
        aria-label={t("deals.bulkOwner")}
        value={ownerId}
        placeholder={t("deals.bulkOwnerPick")}
        disabled={run.isPending}
        onChange={setOwnerId}
        options={(roster.data ?? []).map((entry) => ({
          value: entry.id,
          // The user roster: every entry carries a display name; a team
          // (the other roster kind) is never asked for here.
          label: "display_name" in entry ? entry.display_name : entry.id,
        }))}
      />
      <Button
        small
        variant="primary"
        disabled={busy || ownerId === ""}
        onClick={assign}
      >
        {t("deals.bulkAssign")}
      </Button>
      <Select
        aria-label={t("deals.bulkStage")}
        value={stageId}
        placeholder={t("deals.bulkStagePick")}
        disabled={run.isPending}
        onChange={setStageId}
        options={openStages.map((stage) => ({
          value: stage.id,
          label: stage.name,
        }))}
      />
      <Button small disabled={busy || stageId === ""} onClick={moveStage}>
        {t("deals.bulkMove")}
      </Button>
      <Button small disabled={busy} onClick={archive}>
        {t("deals.bulkArchive")}
      </Button>
      {failed.length > 0 && (
        <span className="t-caption" style={{ color: "var(--danger)" }}>
          {t("deals.bulkFailed", { count: failed.length })}{" "}
          {failed
            .map((outcome) => `${outcome.name}: ${outcome.error}`)
            .join(" · ")}
        </span>
      )}
    </>
  );
}
