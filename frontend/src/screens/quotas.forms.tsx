import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { Button, Field, Modal, Radio, TextInput } from "../design-system/atoms";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import { ArchiveAction } from "./archive";
import { ProblemError, problemMessageOf, throwProblem } from "./common";
import { EditAction } from "./edit";
import { useRoster, useRosterPartial, useRosterPartialHint } from "./entityref";
import "./quotas.css";
import { toMajorUnits, toMinorUnits } from "../format/minorunits";

// The quota target write surface: create (owner-XOR-team side picker), edit
// (reassign within the fixed side), and archive. Split out of quotas.tsx so
// the view file stays focused on the read/attainment surface. Every write is
// human-only and cookie-authed (RD-WIRE-*); the server's owner-XOR-team
// contract is branched to a targeted message, never swallowed.

type Quota = components["schemas"]["Quota"];
type User = components["schemas"]["User"];
type Team = components["schemas"]["Team"];

type Side = "owner" | "team";

// A human-typed whole amount → minor units, at the scale the quota's own
// currency carries. The target is whole-unit and human-set (RD-PARAM-3) —
// never fractional or computed — so every non-digit is stripped first.
//
// It used to be parseEuroMinor and it scaled by a hard-coded hundred, which
// the name at least admitted. The form has always had a currency field, so a
// dong target typed as 500,000,000 was stored as fifty billion and read back
// as five hundred million — wrong in the record and right on the screen, which
// is the pair that survives review.
export function parseTargetMinor(input: string, currency: string): number {
  const digits = input.replace(/[^\d]/g, "");
  return digits ? toMinorUnits(Number.parseInt(digits, 10), currency) : 0;
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
          target_minor: parseTargetMinor(amount, currency.trim().toUpperCase()),
          // Currency is a 3-letter uppercase code on the wire (^[A-Z]{3}$);
          // normalise a lowercase entry rather than bounce it off the server.
          currency: currency.trim().toUpperCase(),
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

  const ownerXorTeam =
    mutation.error instanceof ProblemError &&
    isOwnerXorTeam(mutation.error.problem);
  const errorMessage = !mutation.isError
    ? null
    : ownerXorTeam
      ? t("quotas.err.ownerXorTeam")
      : problemMessageOf(mutation.error, t);

  const roster = side === "owner" ? users : teams;
  // Read for BOTH sides on every render — a hook count that followed `side`
  // would change as the reader switched the radio.
  const usersPartial = useRosterPartial("user", open);
  const teamsPartial = useRosterPartial("team", open);
  const rosterPartialHint = useRosterPartialHint(
    side === "owner" ? usersPartial : teamsPartial,
  );
  const canSubmit =
    subjectId !== "" &&
    periodStart !== "" &&
    periodEnd !== "" &&
    parseTargetMinor(amount, currency.trim().toUpperCase()) > 0 &&
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
            <Radio
              name={`${formId}-side`}
              checked={side === "owner"}
              onChange={() => pickSide("owner")}
              label={t("quotas.side.owner")}
            />
            <Radio
              name={`${formId}-side`}
              checked={side === "team"}
              onChange={() => pickSide("team")}
              label={t("quotas.side.team")}
            />
          </div>
        </fieldset>

        <Field
          label={side === "owner" ? t("quotas.owner") : t("quotas.team")}
          required
          // The side's own roster, and whether it is all of it. A quota is
          // written against ONE subject, so a subject this dialog never read
          // is a quota nobody can create — said here rather than left to look
          // like a workspace with fewer people in it than it has.
          //
          // The words come from the roster rather than being spelled here. A
          // Field hint is the Field's own paragraph, wired into the control's
          // `aria-describedby`, so `RosterPartialNote` cannot be nested inside
          // it — but a hand-written second wording of the same caveat would
          // drift the moment the note's own changed.
          hint={rosterPartialHint}
        >
          {(control) => (
            <Select
              {...control}
              value={subjectId}
              onChange={setSubjectId}
              options={[
                {
                  value: "",
                  label:
                    side === "owner"
                      ? t("quotas.pickOwner")
                      : t("quotas.pickTeam"),
                  disabled: true,
                },
                ...(roster.data ?? []).map((entry) => ({
                  value: entry.id,
                  label: subjectLabel(entry),
                })),
              ]}
            />
          )}
        </Field>

        <Field label={t("quotas.periodStart")} required>
          {(control) => (
            <TextInput
              {...control}
              type="date"
              value={periodStart}
              onChange={(event) => setPeriodStart(event.target.value)}
            />
          )}
        </Field>

        <Field label={t("quotas.periodEnd")} required>
          {(control) => (
            <TextInput
              {...control}
              type="date"
              value={periodEnd}
              onChange={(event) => setPeriodEnd(event.target.value)}
            />
          )}
        </Field>

        <Field label={t("quotas.amount")} required>
          {(control) => (
            <TextInput
              {...control}
              type="text"
              inputMode="numeric"
              value={amount}
              placeholder={t("quotas.amountHint")}
              onChange={(event) => setAmount(event.target.value)}
            />
          )}
        </Field>

        <Field label={t("quotas.currency")} required>
          {(control) => (
            <TextInput
              {...control}
              type="text"
              value={currency}
              maxLength={3}
              onChange={(event) => setCurrency(event.target.value)}
            />
          )}
        </Field>

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
  const t = useT();
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
          placeholder: t("quotas.amountHint"),
          // The record carries integer minor units; the field edits whole
          // units of the quota's own currency, so echo minor→major on prefill
          // and parse major→minor on save, both at that currency's scale.
          toInput: (raw) =>
            raw == null || raw === ""
              ? ""
              : String(Math.round(toMajorUnits(Number(raw), quota.currency))),
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
          params: {
            path: { id: quota.id },
            ...ifMatch(requireVersion(quota.version)),
          },
          body: {
            period_start: String(values.period_start),
            period_end: String(values.period_end),
            target_minor: parseTargetMinor(
              String(values.amount ?? ""),
              String(values.currency).trim().toUpperCase(),
            ),
            currency: String(values.currency).trim().toUpperCase(),
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
