// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import { ifMatch, requireVersion } from "../api/version";
import { Field, Textarea } from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import {
  PHASE_LABEL,
  PROJECT_PHASES,
  type ProjectPhase,
} from "./projects.form";

// The project's phase: where it is, and the one dialog that moves it.
//
// Movement goes both ways — a closed project is reopened into delivery by
// the same verb — and every move lands a phase-history row, so the dialog
// always offers a reason. Closing REQUIRES one (the server refuses a close
// without it, 422 closed_reason_required), and the dialog holds Confirm back
// until the reason says something rather than letting the reader learn that
// from the refusal.

/**
 * PhaseStepper draws the four phases in order. The current one is a fact and
 * stays a marker; every other one is the move to it. The same `.stepper`
 * chrome the deal page's stage ladder uses, because a reader who has moved a
 * deal has already learned what this row does.
 */
export function PhaseStepper({
  phase,
  refusedReasonId,
  pending,
  onMove,
}: Readonly<{
  phase: ProjectPhase;
  // The id of the page's sentence saying why no move is offered — an
  // archived project. Every step points at that one element.
  refusedReasonId?: string;
  pending: boolean;
  onMove: (to: ProjectPhase) => void;
}>) {
  const t = useT();
  return (
    <fieldset
      className="stepper project-stepper"
      aria-label={t("project.phaseLabel")}
    >
      {PROJECT_PHASES.map((step) =>
        step === phase ? (
          <span key={step} className="step current" aria-current="step">
            {t(PHASE_LABEL[step])}
          </span>
        ) : (
          <button
            key={step}
            type="button"
            className="step"
            data-testid={`project-step-${step}`}
            disabled={pending || refusedReasonId !== undefined}
            aria-describedby={refusedReasonId}
            onClick={() => onMove(step)}
          >
            {t(PHASE_LABEL[step])}
          </button>
        ),
      )}
    </fieldset>
  );
}

export type PhaseMove = Readonly<{
  projectId: string;
  // The version the page drew the project from: the move names the row as
  // the reader saw it, so a change made elsewhere meanwhile fails loud.
  version: number | undefined;
  to: ProjectPhase;
  reason: string;
}>;

/** The one write that moves a phase, and the cache it refreshes after. */
export function useAdvanceProject(onDone: () => void) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (move: PhaseMove) => {
      const { data, error } = await api.POST("/projects/{id}/advance", {
        params: {
          path: { id: move.projectId },
          ...ifMatch(requireVersion(move.version)),
        },
        body: { to_phase: move.to, reason: move.reason || null },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (_data, move) => {
      queryClient.invalidateQueries({ queryKey: ["project", move.projectId] });
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      onDone();
    },
  });
}

/**
 * AdvanceProjectModal asks the one question every move has — why — and
 * refuses to close a project without an answer.
 */
export function AdvanceProjectModal({
  projectId,
  version,
  to,
  onClose,
}: Readonly<{
  projectId: string;
  version: number | undefined;
  // The phase being moved to, or null while no move is pending.
  to: ProjectPhase | null;
  onClose: () => void;
}>) {
  const t = useT();
  const [reason, setReason] = useState("");
  const [openFor, setOpenFor] = useState<ProjectPhase | null>(null);
  // A fresh dialog per move: a reason typed for one close must not be offered
  // for the next reopen. Reset during render on the transition, the way the
  // create dialog seeds its defaults.
  if (to !== openFor) {
    setOpenFor(to);
    setReason("");
  }
  const advance = useAdvanceProject(onClose);
  const closing = to === "closed";
  const reasonMissing = closing && reason.trim() === "";
  return (
    <ConfirmModal
      open={to !== null}
      onClose={onClose}
      title={
        to ? t("project.advance.title", { phase: t(PHASE_LABEL[to]) }) : ""
      }
      confirmLabel={
        closing ? t("project.advance.close") : t("project.advance.confirm")
      }
      confirmVariant={closing ? "danger" : "primary"}
      confirmDisabled={reasonMissing}
      pending={advance.isPending}
      error={advance.isError ? problemMessageOf(advance.error, t) : null}
      onConfirm={() => {
        if (to) {
          advance.mutate({ projectId, version, to, reason: reason.trim() });
        }
      }}
    >
      <p className="t-small">
        {closing ? t("project.advance.closeBody") : t("project.advance.body")}
      </p>
      <Field
        label={t("project.advance.reason")}
        required={closing}
        hint={closing ? t("project.advance.reasonRequired") : undefined}
      >
        {(control) => (
          <Textarea
            {...control}
            data-testid="project-advance-reason"
            rows={3}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
        )}
      </Field>
    </ConfirmModal>
  );
}
