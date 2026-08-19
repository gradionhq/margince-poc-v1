import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, Field, Modal, Textarea } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import { useLeadDisqualifyReasons } from "./leadsources";

type Lead = components["schemas"]["Lead"];

// Closing a lead asks why. The reason comes from the administered list
// (Settings › Data model), is required here because the list exists to be
// answered from, and lands on the lead where the page and the list's
// Disqualified view read it back.

export function DisqualifyDialog({
  lead,
  open,
  onClose,
  onDisqualified,
}: Readonly<{
  lead: Lead;
  open: boolean;
  onClose: () => void;
  onDisqualified: (closed: Lead) => void;
}>) {
  const t = useT();
  const headingId = useId();
  const queryClient = useQueryClient();
  const reasons = useLeadDisqualifyReasons();
  const [reasonId, setReasonId] = useState("");
  const [note, setNote] = useState("");
  const active = (Array.isArray(reasons.data) ? reasons.data : []).filter(
    (reason) => reason.active,
  );

  const disqualify = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.DELETE("/leads/{id}", {
        params: { path: { id: lead.id } },
        body: { reason_id: reasonId, note: note.trim() || null },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: (closed) => {
      queryClient.invalidateQueries({ queryKey: ["leads"] });
      queryClient.invalidateQueries({ queryKey: ["lead", lead.id] });
      onDisqualified(closed);
    },
  });

  // A close in flight is not something to walk away from: the dialog stays
  // until the server has answered.
  const close = () => {
    if (disqualify.isPending) return;
    disqualify.reset();
    onClose();
  };
  const name = lead.full_name ?? lead.email ?? "";

  return (
    <Modal open={open} onClose={close} labelledBy={headingId}>
      <h2
        id={headingId}
        className="t-h2"
        style={{ marginBottom: "var(--space-3)" }}
      >
        {t("lead.disqualify.title", { name })}
      </h2>
      <div className="lead-qualify">
        <Field label={t("lead.disqualify.reason")} required>
          {(control) => (
            <Select
              {...control}
              value={reasonId}
              placeholder={t("lead.disqualify.pickReason")}
              onChange={setReasonId}
              options={active.map((reason) => ({
                value: reason.id,
                label: reason.label,
              }))}
            />
          )}
        </Field>
        <Field label={t("lead.disqualify.note")}>
          {(control) => (
            <Textarea
              {...control}
              value={note}
              onChange={(event) => setNote(event.target.value)}
            />
          )}
        </Field>
        {disqualify.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(disqualify.error, t)}
          </Callout>
        )}
        <div
          style={{
            display: "flex",
            gap: "var(--space-2)",
            justifyContent: "flex-end",
          }}
        >
          <Button small onClick={close} disabled={disqualify.isPending}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="danger"
            data-testid="lead-disqualify-confirm"
            // Required, and said so: a closed lead with no reason is what the
            // list exists to prevent.
            reason={reasonId ? undefined : t("lead.disqualify.reasonRequired")}
            disabled={disqualify.isPending}
            onClick={() => disqualify.mutate()}
          >
            {t("lead.disqualify.confirm")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
