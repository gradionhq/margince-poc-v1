// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { Button, Modal } from "../design-system/atoms";
import { useT } from "../i18n";

// The shared archive/disqualify affordance (P-3): a human-direct DELETE that
// soft-archives a person/organization/lead (sets archived_at; leads also
// flip to status=disqualified). There is NO restore endpoint in the
// contract, so this hook and action are archive-only — never wire a restore
// control against them. Mirrors useUpdateRecord/EditAction (edit.tsx): the
// screen supplies the transport, this stays resource-agnostic.

export function useArchiveRecord<Archived extends { id: string }>({
  archive,
  invalidate,
  recordKey,
  onDone,
}: Readonly<{
  archive: () => Promise<Archived>;
  invalidate: string;
  recordKey: string;
  onDone: (archived: Archived) => void;
}>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: archive,
    onSuccess: (archived) => {
      queryClient.invalidateQueries({ queryKey: [invalidate] });
      queryClient.invalidateQueries({ queryKey: [recordKey, archived.id] });
      onDone(archived);
    },
  });
}

// The whole per-screen archive affordance in one piece: the danger trigger
// button, a confirm modal (nothing destructive fires without it), and the
// archive choreography above. A screen supplies its label/confirm copy and
// its DELETE transport — nothing else.
export function ArchiveAction<Archived extends { id: string }>({
  label,
  confirmText,
  archive,
  invalidate,
  recordKey,
  onArchived,
}: Readonly<{
  label: string;
  confirmText: string;
  archive: () => Promise<Archived>;
  invalidate: string;
  recordKey: string;
  onArchived: () => void;
}>) {
  const t = useT();
  const headingId = useId();
  const [confirming, setConfirming] = useState(false);
  const mutation = useArchiveRecord({
    archive,
    invalidate,
    recordKey,
    onDone: () => {
      setConfirming(false);
      onArchived();
    },
  });

  return (
    <>
      <Button
        small
        variant="danger"
        onClick={() => setConfirming(true)}
        data-testid="archive-record"
      >
        {label}
      </Button>
      <Modal
        open={confirming}
        onClose={() => setConfirming(false)}
        labelledBy={headingId}
      >
        <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
          {label}
        </h2>
        <p style={{ marginBottom: 16 }}>{confirmText}</p>
        {mutation.isError && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {mutation.error instanceof Error ? mutation.error.message : null}
          </p>
        )}
        <div className="actions">
          <Button
            small
            onClick={() => setConfirming(false)}
            disabled={mutation.isPending}
          >
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="danger"
            onClick={() => mutation.mutate()}
            disabled={mutation.isPending}
            data-testid="archive-confirm"
          >
            {label}
          </Button>
        </div>
      </Modal>
    </>
  );
}
