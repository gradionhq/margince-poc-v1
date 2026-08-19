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

type Lead = components["schemas"]["Lead"];

/** One row's outcome in a fan-out: it went through, or it did not and why. */
export type BulkOutcome = { id: string; name: string; error?: string };

/** Which verb a bulk run applied, for the caller that has to say what moved. */
export type BulkAction =
  | { kind: "assign"; ownerId: string; ownerName: string }
  | { kind: "disqualify" };

/**
 * Bulk verbs over selected leads: assign an owner, disqualify. Both are a
 * client-side fan-out of the record's own write — there is no bulk endpoint,
 * and inventing one would bypass the per-row version guard.
 *
 * Every row sends ITS OWN If-Match: PATCH /leads/{id} requires the version
 * (428 without it), and one version copied across the selection would 409
 * on every row but the one it came from. The versions come from the rows the
 * list holds; a row that moved under the reader answers 409, is reported by
 * name, and can be retried after the list refreshes.
 */
export function LeadBulkBar({
  leads,
  onDone,
}: Readonly<{
  /** The selected rows, with the versions the list currently holds. */
  leads: readonly Lead[];
  /** Called after any run — the caller refetches and clears the selection. */
  onDone: (outcomes: readonly BulkOutcome[], action: BulkAction) => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [ownerId, setOwnerId] = useState("");
  const roster = useRoster("user", true);
  const [outcomes, setOutcomes] = useState<readonly BulkOutcome[]>([]);

  const run = useMutation({
    mutationFn: async ({
      write,
    }: {
      write: (lead: Lead) => Promise<void>;
      action: BulkAction;
    }): Promise<BulkOutcome[]> =>
      // Sequential, not Promise.all: a bulk verb over a work queue is a
      // handful of rows, and a burst of concurrent writes against one
      // rep's own leads buys nothing but contention.
      leads.reduce<Promise<BulkOutcome[]>>(async (acc, lead) => {
        const done = await acc;
        const name = lead.full_name ?? lead.email ?? lead.id;
        try {
          await write(lead);
          done.push({ id: lead.id, name });
        } catch (error) {
          done.push({
            id: lead.id,
            name,
            error:
              error instanceof ProblemError
                ? problemMessageOf(error, t)
                : t("lead.bulkFailedRow"),
          });
        }
        return done;
      }, Promise.resolve([])),
    onSuccess: async (result, { action }) => {
      // Awaited: the rows that refused keep their selection so they can be
      // retried, and a retry that fired before the refetch landed would
      // resend the very version that just conflicted. The run stays pending
      // — and the verbs disabled — until the list holds fresh versions.
      await queryClient.invalidateQueries({ queryKey: ["leads"] });
      setOutcomes(result);
      onDone(result, action);
    },
  });

  const ownerName =
    (roster.data ?? [])
      .filter((entry) => entry.id === ownerId)
      .map((entry) => ("display_name" in entry ? entry.display_name : null))
      .find((name) => typeof name === "string") ?? ownerId;
  const assign = () =>
    run.mutate({
      action: { kind: "assign", ownerId, ownerName },
      write: async (lead) => {
        const { error } = await api.PATCH("/leads/{id}", {
          params: {
            path: { id: lead.id },
            ...ifMatch(requireVersion(lead.version)),
          },
          body: { owner_id: ownerId },
        });
        if (error) {
          throwProblem(error, t);
        }
      },
    });
  const disqualify = () =>
    run.mutate({
      action: { kind: "disqualify" },
      write: async (lead) => {
        const { error } = await api.DELETE("/leads/{id}", {
          params: { path: { id: lead.id } },
        });
        if (error) {
          throwProblem(error, t);
        }
      },
    });

  const failed = outcomes.filter((o) => o.error);
  return (
    <>
      <span className="t-caption">
        {t("lead.bulkSelected", { count: leads.length })}
      </span>
      <Select
        aria-label={t("lead.bulkOwner")}
        value={ownerId}
        placeholder={t("lead.bulkOwnerPick")}
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
        disabled={run.isPending || ownerId === "" || leads.length === 0}
        onClick={assign}
      >
        {t("lead.bulkAssign")}
      </Button>
      <Button
        small
        disabled={run.isPending || leads.length === 0}
        onClick={disqualify}
      >
        {t("lead.bulkDisqualify")}
      </Button>
      {failed.length > 0 && (
        <span className="t-caption" style={{ color: "var(--danger)" }}>
          {t("lead.bulkFailed", { count: failed.length })}{" "}
          {failed.map((o) => `${o.name}: ${o.error}`).join(" · ")}
        </span>
      )}
    </>
  );
}
