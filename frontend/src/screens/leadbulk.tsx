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
import { useLeadDisqualifyReasons } from "./leadsources";

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
 * Disqualify asks why, exactly as the single-lead dialog does: the reason is
 * an ACTIVE administered reason, it is required before the verb will run, and
 * it rides on every row's own DELETE. A bulk path that skipped it would leave
 * `disqualify_reason_id` null on whole batches, which is how the column stops
 * being worth reporting on. No note here, though the dialog offers one: a note
 * is prose about ONE lead, and the same sentence stamped on eight rows is
 * detail nobody wrote about any of them.
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
  const [reasonId, setReasonId] = useState("");
  const reasons = useLeadDisqualifyReasons();
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
  // The reason list is administered (Settings › Data model); only its ACTIVE
  // rows may be applied, and a payload that is not the contract's array is
  // read as nothing rather than crashing the bar that renders it.
  const activeReasons = (
    Array.isArray(reasons.data) ? reasons.data : []
  ).filter((reason) => reason.active);
  // The reason is read where the CLICK happens, not inside a `mutationFn` that
  // react-query re-arms in a passive effect: this closure belongs to the render
  // whose button the reader pressed, so the reason it sends is the one that was
  // on screen. The per-row `write` is handed to `run` and called with each
  // lead — the row identity is the parameter, and the reason is the one thing
  // the whole batch shares.
  const disqualify = () =>
    run.mutate({
      action: { kind: "disqualify" },
      write: async (lead) => {
        const { error } = await api.DELETE("/leads/{id}", {
          params: { path: { id: lead.id } },
          body: { reason_id: reasonId },
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
      <Select
        aria-label={t("lead.disqualify.reason")}
        value={reasonId}
        placeholder={t("lead.disqualify.pickReason")}
        disabled={run.isPending}
        onChange={setReasonId}
        options={activeReasons.map((reason) => ({
          value: reason.id,
          label: reason.label,
        }))}
      />
      <Button
        small
        disabled={run.isPending || leads.length === 0}
        // The same requirement, and the same sentence, as the single-lead
        // dialog: a batch closed with no reason is exactly what the
        // administered list exists to prevent.
        reason={reasonId ? undefined : t("lead.disqualify.reasonRequired")}
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
