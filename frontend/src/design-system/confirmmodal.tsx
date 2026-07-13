import type { ReactNode } from "react";
import { useId } from "react";
import { useT } from "../i18n";
import { Button, Modal } from "./atoms";
import { AutonomyDot } from "./trust";

// The shared confirm-dialog chrome (Phase 3, task 3.1): this used to live
// duplicated, near-identically, inline in the deals.tsx terminal-stage
// advance confirm and archive.tsx's ArchiveAction. Both wire a Modal, a
// title (deals.tsx's carries an autonomy dot, archive.tsx's doesn't), an
// inline mutation error, and a Cancel/Confirm pair that disables while a
// mutation is in flight. The caller owns the body copy and any extra
// fields (e.g. the lost-reason input) via children — this atom only owns
// the modal chrome and the actions.

export function ConfirmModal({
  open,
  onClose,
  title,
  tier,
  confirmLabel,
  onConfirm,
  pending,
  error,
  children,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  title: string;
  tier?: "confirm";
  confirmLabel: string;
  onConfirm: () => void;
  pending?: boolean;
  error?: string | null;
  children: ReactNode;
}>) {
  const t = useT();
  const headingId = useId();
  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
        {tier && (
          <>
            <AutonomyDot tier={tier} />{" "}
          </>
        )}
        {title}
      </h2>
      {children}
      {error && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {error}
        </p>
      )}
      <div className="actions">
        <Button onClick={onClose} disabled={pending}>
          {t("create.cancel")}
        </Button>
        <Button variant="primary" onClick={onConfirm} disabled={pending}>
          {confirmLabel}
        </Button>
      </div>
    </Modal>
  );
}
