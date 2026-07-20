import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { Button, Modal, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import { ArchiveAction } from "./archive";
import { ProblemError, throwProblem } from "./common";
import { EditAction } from "./edit";
import { useRoster } from "./entityref";

// The quota target write surface: create (owner-XOR-team side picker), edit
// (reassign within the fixed side), and archive. Split out of quotas.tsx so
// the view file stays focused on the read/attainment surface. Every write is
// human-only and cookie-authed (RD-WIRE-*); the server's owner-XOR-team
// contract is branched to a targeted message, never swallowed.

type Quota = components["schemas"]["Quota"];
type User = components["schemas"]["User"];
type Team = components["schemas"]["Team"];

type Side = "owner" | "team";

// Human-typed integer euros → minor units (mirrors the mockup's parseTgt):
// strip every non-digit, then scale by the 2-decimal minor unit. The target
// is whole-euro, human-set (RD-PARAM-3) — never a fractional or computed value.
export function parseEuroMinor(input: string): number {
  const digits = input.replace(/[^\d]/g, "");
  return digits ? Number.parseInt(digits, 10) * 100 : 0;
}

// A 422 whose owner-XOR-team contract failed — either the top-level code names
// it, or one of details.errors[] does (createQuota/updateQuota return the
// distinct owner_xor_team_required code, not a generic per-field code). Branched
// to a targeted field message rather than the raw server detail.
export function isOwnerXorTeam(problem: unknown): boolean {
  if (!problem || typeof problem !== "object") return false;
  const record = problem as Record<string, unknown>;
  if (record.code === "owner_xor_team_required") return true;
  const details = record.details;
  if (details && typeof details === "object") {
    const errors = (details as Record<string, unknown>).errors;
    if (Array.isArray(errors)) {
      return errors.some(
        (entry) =>
          entry !== null &&
          typeof entry === "object" &&
          (entry as Record<string, unknown>).code === "owner_xor_team_required",
      );
    }
  }
  return false;
}

// A roster entry's display label, narrowing the User|Team union by a field
// only one side carries (no unchecked cast).
function subjectLabel(entry: User | Team): string {
  return "display_name" in entry ? entry.display_name : entry.name;
}

// Create is bespoke — the generic RecordFormBody can't express the
// owner-XOR-team radio (exactly one side non-null on the wire). It still runs
// through the shared error-surfacing (ProblemError → problemMessage) and
// invalidates the ["quotas"] list like the shared create choreography does,
// but skips its navigate({screen,id}): a quota has no 360 route to land on.
function SetTargetModal({
  open,
  onClose,
  onCreated,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  onCreated?: (id: string) => void;
}>) {
  const t = useT();
  const headingId = useId();
  const formId = useId();
  const queryClient = useQueryClient();
  const users = useRoster("user", open);
  const teams = useRoster("team", open);
  const [side, setSide] = useState<Side>("owner");
  const [subjectId, setSubjectId] = useState("");
  const [periodStart, setPeriodStart] = useState("");
  const [periodEnd, setPeriodEnd] = useState("");
  const [amount, setAmount] = useState("");
  const [currency, setCurrency] = useState("EUR");
  // Only the closed→open transition resets the form — a background roster
  // refetch must not wipe what the user is mid-typing.
  const wasOpen = useRef(false);

  useEffect(() => {
    if (open && !wasOpen.current) {
      setSide("owner");
      setSubjectId("");
      setPeriodStart("");
      setPeriodEnd("");
      setAmount("");
      setCurrency("EUR");
    }
    wasOpen.current = open;
  }, [open]);

  const mutation = useMutation({
    mutationFn: async (): Promise<Quota> => {
      const { data, error } = await api.POST("/quotas", {
        params: { header: { "Idempotency-Key": crypto.randomUUID() } },
        body: {
          owner_id: side === "owner" ? subjectId : null,
          team_id: side === "team" ? subjectId : null,
          period_start: periodStart,
          period_end: periodEnd,
          target_minor: parseEuroMinor(amount),
          currency: currency.trim(),
        },
      });
      if (error) throwProblem(error);
      return data;
    },
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ["quotas"] });
      onCreated?.(created.id);
      onClose();
    },
  });

  const errorMessage =
    mutation.error instanceof ProblemError
      ? isOwnerXorTeam(mutation.error.problem)
        ? t("quotas.err.ownerXorTeam")
        : mutation.error.message
      : mutation.error instanceof Error
        ? mutation.error.message
        : null;

  const roster = side === "owner" ? users : teams;
  const canSubmit =
    subjectId !== "" &&
    periodStart !== "" &&
    periodEnd !== "" &&
    parseEuroMinor(amount) > 0 &&
    currency.trim() !== "";

  function pickSide(next: Side) {
    setSide(next);
    // The two rosters share no ids — a subject chosen for the old side can't
    // stand in for the new one.
    setSubjectId("");
  }

  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2
        id={headingId}
        className="t-h2"
        style={{ marginBottom: "var(--space-3)" }}
      >
        {t("quotas.target.new")}
      </h2>
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          mutation.mutate();
        }}
      >
        <fieldset className="field quota-side">
          <legend className="t-label">{t("quotas.side.label")}</legend>
          <div className="quota-side-choices">
            <label className="quota-side-choice">
              <input
                type="radio"
                name={`${formId}-side`}
                checked={side === "owner"}
                onChange={() => pickSide("owner")}
              />
              {t("quotas.side.owner")}
            </label>
            <label className="quota-side-choice">
              <input
                type="radio"
                name={`${formId}-side`}
                checked={side === "team"}
                onChange={() => pickSide("team")}
              />
              {t("quotas.side.team")}
            </label>
          </div>
        </fieldset>

        <div className="field">
          <label className="t-label" htmlFor={`${formId}-subject`}>
            {side === "owner" ? t("quotas.owner") : t("quotas.team")} *
          </label>
          <select
            id={`${formId}-subject`}
            className="input"
            value={subjectId}
            required
            onChange={(event) => setSubjectId(event.target.value)}
          >
            <option value="" />
            {(roster.data ?? []).map((entry) => (
              <option key={entry.id} value={entry.id}>
                {subjectLabel(entry)}
              </option>
            ))}
          </select>
        </div>

        <div className="field">
          <label className="t-label" htmlFor={`${formId}-start`}>
            {t("quotas.periodStart")} *
          </label>
          <TextInput
            id={`${formId}-start`}
            type="date"
            value={periodStart}
            required
            onChange={(event) => setPeriodStart(event.target.value)}
          />
        </div>

        <div className="field">
          <label className="t-label" htmlFor={`${formId}-end`}>
            {t("quotas.periodEnd")} *
          </label>
          <TextInput
            id={`${formId}-end`}
            type="date"
            value={periodEnd}
            required
            onChange={(event) => setPeriodEnd(event.target.value)}
          />
        </div>

        <div className="field">
          <label className="t-label" htmlFor={`${formId}-amount`}>
            {t("quotas.amount")} *
          </label>
          <TextInput
            id={`${formId}-amount`}
            type="text"
            inputMode="numeric"
            value={amount}
            required
            onChange={(event) => setAmount(event.target.value)}
          />
        </div>

        <div className="field">
          <label className="t-label" htmlFor={`${formId}-currency`}>
            {t("quotas.currency")} *
          </label>
          <TextInput
            id={`${formId}-currency`}
            type="text"
            value={currency}
            required
            onChange={(event) => setCurrency(event.target.value)}
          />
        </div>

        {errorMessage && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {errorMessage}
          </p>
        )}
        <div className="actions">
          <Button small type="button" onClick={onClose}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="primary"
            type="submit"
            disabled={mutation.isPending || !canSubmit}
            data-testid="quota-create-submit"
          >
            {mutation.isPending ? t("create.saving") : t("quotas.target.save")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// The create affordance: a primary trigger plus the bespoke modal above.
export function SetTargetAction({
  label,
  onCreated,
}: Readonly<{ label: string; onCreated?: (id: string) => void }>) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button
        small
        variant="primary"
        onClick={() => setOpen(true)}
        data-testid="quota-create"
      >
        <Plus aria-hidden style={{ width: 14, height: 14 }} /> {label}
      </Button>
      <SetTargetModal
        open={open}
        onClose={() => setOpen(false)}
        onCreated={onCreated}
      />
    </>
  );
}

// Edit reuses the shared EditAction choreography (If-Match, 409 version_skew
// surfacing). The owner/team side is fixed — a merge-PATCH can't clear a side
// (omitted and null are the same wire shape) — so only period, target, and
// currency are editable; switching side is archive-and-recreate.
export function EditTargetAction({
  label,
  quota,
}: Readonly<{ label: string; quota: Quota }>) {
  return (
    <EditAction<Quota>
      label={label}
      fields={[
        {
          key: "period_start",
          label: "quotas.periodStart",
          type: "date",
          required: true,
        },
        {
          key: "period_end",
          label: "quotas.periodEnd",
          type: "date",
          required: true,
        },
        {
          key: "amount",
          label: "quotas.amount",
          type: "text",
          required: true,
          // The record carries integer minor units; the field edits whole
          // euros, so echo minor→euro on prefill and parse euro→minor on save.
          toInput: (raw) =>
            raw == null || raw === ""
              ? ""
              : String(Math.round(Number(raw) / 100)),
        },
        {
          key: "currency",
          label: "quotas.currency",
          type: "text",
          required: true,
        },
      ]}
      record={{
        id: quota.id,
        version: quota.version,
        period_start: quota.period_start,
        period_end: quota.period_end,
        amount: quota.target_minor,
        currency: quota.currency,
      }}
      update={async (values) => {
        const { data, error } = await api.PATCH("/quotas/{id}", {
          params: { path: { id: quota.id }, ...ifMatch(quota.version) },
          body: {
            period_start: String(values.period_start),
            period_end: String(values.period_end),
            target_minor: parseEuroMinor(String(values.amount ?? "")),
            currency: String(values.currency),
          },
        });
        if (error) throwProblem(error);
        return data;
      }}
      invalidate="quotas"
      // Recompute this quota's attainment after the target changes — the
      // attainment query is keyed ["quota-attainment", id].
      recordKey="quota-attainment"
    />
  );
}

// Archive reuses the shared confirm-first ArchiveAction; on success the
// ["quotas"] list refetches and the archived quota drops out.
export function ArchiveQuotaAction({
  quota,
  onArchived,
}: Readonly<{ quota: Quota; onArchived: () => void }>) {
  const t = useT();
  return (
    <ArchiveAction<Quota>
      label={t("quotas.archive.title")}
      confirmText={t("quotas.archive.confirm")}
      archive={async () => {
        const { data, error } = await api.DELETE("/quotas/{id}", {
          params: { path: { id: quota.id } },
        });
        if (error) throwProblem(error);
        return data;
      }}
      invalidate="quotas"
      recordKey="quota-attainment"
      onArchived={onArchived}
    />
  );
}
