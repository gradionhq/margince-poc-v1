// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { navigate, type Route } from "../app/router";
import { Button, Modal, SearchField } from "../design-system/atoms";
import { useT } from "../i18n";
import { useSorMode } from "./common";

// The shared "Merge into…" affordance (P-2): a human direct call that folds
// this record (the source, A) into a picked survivor (B) — A is archived
// with merged_into_id=B, B keeps the id the rest of the CRM already points
// at. Person and Company 360s have an identical merge shape (target_id body
// + If-Match precondition, survivor Person/Organization back), so this stays
// resource-agnostic: the screen supplies the search transport, the merge
// transport, and where the survivor's 360 lives.

const SEARCH_DEBOUNCE_MS = 250;

type MergeCandidate = { id: string; name: string };

export function MergeAction<Survivor extends { id: string }>({
  label,
  sourceId,
  sourceName,
  searchTargets,
  merge,
  invalidate,
  recordKey,
  survivorRoute,
}: Readonly<{
  label: string;
  sourceId: string;
  sourceName: string;
  // Excludes sourceId from its result — the source row is never a valid
  // merge target for itself.
  searchTargets: (q: string) => Promise<MergeCandidate[]>;
  // POSTs the merge; the screen attaches ifMatch(sourceVersion) itself, so
  // this stays agnostic of the source record's shape. Returns the surviving
  // record.
  merge: (targetId: string) => Promise<Survivor>;
  invalidate: string;
  recordKey: string;
  survivorRoute: (targetId: string) => Route;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const headingId = useId();
  // Merge folds one mirrored record into another — a write the incumbent
  // mirror refuses (unsupported_by_sor). Render nothing in overlay rather than
  // a button that can only fail (guarded after the hooks below).
  const overlay = useSorMode() === "overlay";
  const [open, setOpen] = useState(false);
  const [term, setTerm] = useState("");
  const [candidates, setCandidates] = useState<MergeCandidate[]>([]);
  const [target, setTarget] = useState<MergeCandidate | null>(null);
  const [searchError, setSearchError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    const query = term.trim();
    if (!query) {
      setCandidates([]);
      setSearchError(null);
      return;
    }
    let cancelled = false;
    const timer = setTimeout(async () => {
      try {
        const results = await searchTargets(query);
        if (!cancelled) {
          setCandidates(
            results.filter((candidate) => candidate.id !== sourceId),
          );
          setSearchError(null);
        }
      } catch (error) {
        if (!cancelled) {
          setCandidates([]);
          setSearchError(
            error instanceof Error ? error.message : "request failed",
          );
        }
      }
    }, SEARCH_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [open, term, sourceId, searchTargets]);

  const mutation = useMutation({
    mutationFn: (targetId: string) => merge(targetId),
    onSuccess: (survivor) => {
      queryClient.invalidateQueries({ queryKey: [invalidate] });
      queryClient.invalidateQueries({ queryKey: [recordKey, sourceId] });
      queryClient.invalidateQueries({ queryKey: [recordKey, survivor.id] });
      setOpen(false);
      navigate(survivorRoute(survivor.id));
    },
  });

  const close = () => {
    setOpen(false);
    setTerm("");
    setCandidates([]);
    setTarget(null);
    setSearchError(null);
    mutation.reset();
  };

  if (overlay) {
    return null;
  }

  return (
    <>
      <Button small onClick={() => setOpen(true)} data-testid="merge-record">
        {label}
      </Button>
      <Modal open={open} onClose={close} labelledBy={headingId}>
        <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
          {label}
        </h2>
        <p className="t-caption" style={{ marginBottom: 8 }}>
          {t("merge.pickTarget")}
        </p>
        <SearchField
          placeholder={t("merge.searchPlaceholder")}
          aria-label={t("merge.searchPlaceholder")}
          value={term}
          onChange={(event) => {
            setTerm(event.target.value);
            setTarget(null);
          }}
        />
        {searchError && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {searchError}
          </p>
        )}
        <ul style={{ listStyle: "none", margin: "8px 0", padding: 0 }}>
          {candidates.map((candidate) => (
            <li key={candidate.id}>
              <button
                type="button"
                className="btn btn-ghost"
                aria-pressed={target?.id === candidate.id}
                onClick={() => setTarget(candidate)}
                style={{ width: "100%", textAlign: "left" }}
              >
                {candidate.name}
              </button>
            </li>
          ))}
        </ul>
        {target && (
          <p style={{ marginBottom: 16 }}>
            {t("merge.confirm", { source: sourceName, target: target.name })}
          </p>
        )}
        {mutation.isError && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {mutation.error instanceof Error ? mutation.error.message : null}
          </p>
        )}
        <div className="actions">
          <Button small onClick={close} disabled={mutation.isPending}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="danger"
            disabled={!target || mutation.isPending}
            onClick={() => {
              if (target) {
                mutation.mutate(target.id);
              }
            }}
            data-testid="merge-confirm"
          >
            {t("merge.submit")}
          </Button>
        </div>
      </Modal>
    </>
  );
}

export type { MergeCandidate };
