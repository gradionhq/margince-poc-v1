// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Button,
  Card,
  EmptyState,
  Modal,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage, QueryGate, throwProblem } from "./common";

// The reachable UI for RC-2's personal-mail exclusions: a privacy control
// that has had live mail syncing (and its own `capture.skipped` event) since
// capture shipped, but no button a human could reach — the routes are
// human-only by contract (an agent must not widen or narrow a human's
// personal-mail boundary), so this card is the ONLY way to manage the rule
// set. A matching message produces ZERO CRM rows: nothing is captured then
// hidden, it is simply never captured (capture.md CAP-DDL-3).

type CaptureExclusionRule = components["schemas"]["CaptureExclusionRule"];
type ExclusionKind = CaptureExclusionRule["kind"];

// Plain-language kind labels: `recipient_domain` in particular reads as
// implementation jargon to the person whose privacy this protects, so every
// kind gets a full sentence fragment instead of the wire enum value.
const KIND_LABEL_KEY: Record<ExclusionKind, MessageKey> = {
  sender_domain: "exclusions.kind.senderDomain",
  recipient_domain: "exclusions.kind.recipientDomain",
  label: "exclusions.kind.label",
};

const KIND_OPTIONS: ExclusionKind[] = [
  "sender_domain",
  "recipient_domain",
  "label",
];

// Domain/label matching is case-insensitive in effect (the same domain
// written in any case is the same rule), so the client-side duplicate check
// normalizes the same way rather than only catching an exact-string repeat.
function normalizedValue(value: string): string {
  return value.trim().toLowerCase();
}

function isDuplicateRule(
  rules: readonly CaptureExclusionRule[],
  kind: ExclusionKind,
  value: string,
): boolean {
  const target = normalizedValue(value);
  if (target.length === 0) {
    return false;
  }
  return rules.some(
    (rule) => rule.kind === kind && normalizedValue(rule.value) === target,
  );
}

function useExclusionRules() {
  return useQuery({
    queryKey: ["capture-exclusions"],
    queryFn: async (): Promise<CaptureExclusionRule[]> => {
      const { data, error } = await api.GET("/capture/exclusions");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
}

// The server's Create is `ON CONFLICT DO UPDATE RETURNING` (exclusions.go:
// 78-104): re-adding an existing (kind, value) pair is a 2xx returning the
// existing row, never a 422. So this mutation's error path only ever
// surfaces a REAL failure (network, validation of an empty value, auth) —
// the UI prevents the confusing "silent no-op" case itself, client-side,
// by disabling the confirm before the request ever goes out.
function useCreateExclusionRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (request: {
      kind: ExclusionKind;
      value: string;
    }): Promise<CaptureExclusionRule> => {
      const { data, error } = await api.POST("/capture/exclusions", {
        body: request,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["capture-exclusions"] });
    },
  });
}

function useDeleteExclusionRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: string): Promise<void> => {
      const { error } = await api.DELETE("/capture/exclusions/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["capture-exclusions"] });
    },
  });
}

// The create form: its own small modal (mirrors imap-connect-form.tsx's
// bespoke-controlled-state shape) rather than the shared CreateRecordModal,
// because the one behavior this form MUST have — disabling the confirm the
// instant the typed (kind, value) pair already matches a loaded rule — needs
// live visibility into the in-progress field values that CreateRecordModal
// does not expose to its caller.
function AddExclusionModal({
  open,
  onClose,
  existing,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  existing: readonly CaptureExclusionRule[];
}>) {
  const t = useT();
  const headingId = useId();
  const [kind, setKind] = useState<ExclusionKind>("sender_domain");
  const [value, setValue] = useState("");
  const create = useCreateExclusionRule();

  // Only the closed→open transition resets the form, same guard
  // CreateRecordModal/ImapConnectForm use: a background re-render must never
  // wipe a value the person is mid-typing.
  const wasOpen = useRef(false);
  useEffect(() => {
    if (open && !wasOpen.current) {
      setKind("sender_domain");
      setValue("");
    }
    wasOpen.current = open;
  }, [open]);

  const duplicate = isDuplicateRule(existing, kind, value);
  const ready = value.trim().length > 0 && !duplicate;

  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2
        id={headingId}
        className="t-h2"
        style={{ marginBottom: "var(--space-3)" }}
      >
        {t("exclusions.modalTitle")}
      </h2>
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          if (!ready) {
            return;
          }
          create.mutate(
            { kind, value: value.trim() },
            { onSuccess: onClose },
          );
        }}
      >
        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-kind`}>
            {t("exclusions.fieldKind")}
          </label>
          <select
            id={`${headingId}-kind`}
            className="input"
            value={kind}
            onChange={(event) =>
              setKind(event.target.value as ExclusionKind)
            }
          >
            {KIND_OPTIONS.map((option) => (
              <option key={option} value={option}>
                {t(KIND_LABEL_KEY[option])}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-value`}>
            {t("exclusions.value")}
          </label>
          <TextInput
            id={`${headingId}-value`}
            value={value}
            required
            onChange={(event) => setValue(event.target.value)}
          />
        </div>
        {duplicate && (
          <p className="t-caption">{t("exclusions.alreadyPresent")}</p>
        )}
        {create.isError && (
          <p
            role="alert"
            className="t-caption"
            style={{ color: "var(--danger)" }}
          >
            {create.error.message}
          </p>
        )}
        <div className="actions">
          <Button
            small
            type="button"
            onClick={onClose}
            disabled={create.isPending}
          >
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="primary"
            type="submit"
            disabled={!ready || create.isPending}
          >
            {create.isPending ? t("create.saving") : t("exclusions.add")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function ExclusionRow({
  rule,
  onRemove,
}: Readonly<{ rule: CaptureExclusionRule; onRemove: () => void }>) {
  const t = useT();
  return (
    <li
      className="exclusion-row"
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        gap: "var(--space-2)",
      }}
    >
      <span>
        <strong>{t(KIND_LABEL_KEY[rule.kind])}</strong>{" "}
        <span className="t-mono">{rule.value}</span>
      </span>
      <Button small variant="ghost" onClick={onRemove}>
        {t("exclusions.remove")}
      </Button>
    </li>
  );
}

export function CaptureExclusionsCard() {
  const t = useT();
  const query = useExclusionRules();
  const [adding, setAdding] = useState(false);
  const [pendingRemove, setPendingRemove] =
    useState<CaptureExclusionRule | null>(null);
  const remove = useDeleteExclusionRule();

  return (
    <Card>
      <SectionHeader title={t("exclusions.title")} sub={t("exclusions.sub")} />
      <div style={{ marginBottom: "var(--space-3)" }}>
        <Button small variant="primary" onClick={() => setAdding(true)}>
          {t("exclusions.new")}
        </Button>
      </div>
      <QueryGate query={query}>
        {(rules) =>
          rules.length === 0 ? (
            <EmptyState>{t("exclusions.none")}</EmptyState>
          ) : (
            <ul
              className="exclusions-list"
              style={{
                display: "flex",
                flexDirection: "column",
                gap: "var(--space-2)",
              }}
            >
              {rules.map((rule) => (
                <ExclusionRow
                  key={rule.id}
                  rule={rule}
                  onRemove={() => setPendingRemove(rule)}
                />
              ))}
            </ul>
          )
        }
      </QueryGate>
      <AddExclusionModal
        open={adding}
        onClose={() => setAdding(false)}
        existing={query.data ?? []}
      />
      <ConfirmModal
        open={pendingRemove !== null}
        onClose={() => setPendingRemove(null)}
        title={t("exclusions.removeTitle")}
        confirmLabel={t("exclusions.remove")}
        confirmVariant="danger"
        pending={remove.isPending}
        error={remove.isError ? remove.error.message : null}
        onConfirm={() => {
          if (pendingRemove) {
            remove.mutate(pendingRemove.id, {
              onSuccess: () => setPendingRemove(null),
            });
          }
        }}
      >
        <p className="t-body">{t("exclusions.removeConfirm")}</p>
      </ConfirmModal>
    </Card>
  );
}
