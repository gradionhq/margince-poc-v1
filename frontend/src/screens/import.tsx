// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Upload } from "lucide-react";
import { useRef } from "react";
import { useCan } from "../app/capability";
import { Button, Card, SegmentedControl } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { type MessageKey, useT } from "../i18n";
import { problemMessageOf } from "./common";
import { useImportFlow } from "./importflow";
import { ImportMappingTable } from "./importmapping";
import type {
  ImportObject,
  ImportProfile,
  ImportReport,
  ImportRun,
} from "./importtypes";
import { identifyingFieldFor } from "./importtypes";
import "./import.css";

// Bringing a customer's file into the estate (S-E11.6): upload it, see what its
// columns actually hold, map them, read what the import WILL do, then commit.
//
// The dry run is the point of the whole screen. An import is the least
// reversible write in the product — thousands of rows across several entity
// types in one act — so nothing is written until a human has read a report of
// what will happen and pressed the button again.
//
// It lives beside the other operator-run bulk actions rather than in a nav
// entry of its own, and the flow expands inside the card as each step answers.

export function ImportCard() {
  const t = useT();
  const mayImport = useCan("import_run", "create");
  const fileInput = useRef<HTMLInputElement>(null);
  const flow = useImportFlow();
  const { profile, mapping, run, report, upload, validate, commit } = flow;

  // Gated on the grant the STORE demands, not on the admin role: `import_run`
  // is seeded to admin AND ops, so asking for the role would hide the card
  // from an ops seat the server would have accepted. A rep sees no card rather
  // than a card that 403s.
  if (!mayImport) {
    return null;
  }

  // The mapping table is on screen while a file is profiled and no report has
  // been produced for it yet — the one window in which the human is choosing
  // destinations.
  const showMapping = profile !== null && report === null;
  const busy = upload.isPending || validate.isPending || commit.isPending;
  const committed = run?.status === "complete" || run?.status === "failed";

  return (
    <Card title={t("import.title")} sub={t("import.sub")}>
      <div className="import">
        {/* The control carries its own group label, so it needs no Field
            around it — a second label would announce the same words twice. */}
        <SegmentedControl
          options={["lead", "organization"] as const}
          value={flow.object}
          onChange={flow.chooseObject}
          label={t("import.objectLabel")}
          labels={{
            lead: t("import.object.lead"),
            organization: t("import.object.organization"),
          }}
        />
        <p className="import__hint">{t(`import.objectHint.${flow.object}`)}</p>

        <input
          ref={fileInput}
          type="file"
          accept=".csv,text/csv"
          /* The design system's own visually-hidden class, not a copy of it:
             the file input stays reachable by label and keyboard while the
             Button beside it is what a reader actually sees and presses. */
          className="sr-only"
          aria-label={t("import.fileLabel")}
          onChange={(event) => {
            const file = event.target.files?.[0];
            if (file) {
              upload.mutate(file);
            }
          }}
        />
        <Button
          variant="ghost"
          disabled={busy}
          onClick={() => fileInput.current?.click()}
        >
          <Upload size={16} aria-hidden />
          <span>
            {profile ? t("import.chooseAnother") : t("import.choose")}
          </span>
        </Button>

        {upload.error ? (
          <Callout tone="danger">{problemMessageOf(upload.error, t)}</Callout>
        ) : null}

        {showMapping ? (
          <ImportMappingStep
            profile={profile}
            mapping={mapping}
            object={flow.object}
            busy={busy}
            pending={validate.isPending}
            error={validate.error}
            onChange={flow.setTarget}
            onValidate={() =>
              validate.mutate({ object: flow.object, profile, mapping })
            }
          />
        ) : null}

        {report && run ? (
          <ImportOutcome
            report={report}
            run={run}
            committed={committed}
            busy={busy}
            onCommit={() => commit.mutate(run)}
            onRestart={flow.restart}
            error={commit.error}
          />
        ) : null}
      </div>
    </Card>
  );
}

// ImportOutcome draws one report — the dry run's prediction before approval and
// the run's own outcome after it, in the same shape, so a human comparing the
// two is comparing like with like.
function ImportOutcome({
  report,
  run,
  committed,
  busy,
  onCommit,
  onRestart,
  error,
}: Readonly<{
  report: ImportReport;
  run: ImportRun;
  committed: boolean;
  busy: boolean;
  onCommit: () => void;
  onRestart: () => void;
  error: unknown;
}>) {
  const t = useT();
  const d = report.disposition;
  const resumable = run.status === "failed";

  return (
    <div className="import__outcome">
      <h4 className="import__outcomeTitle">
        {committed ? t("import.outcomeTitle") : t("import.previewTitle")}
      </h4>
      <dl className="import__counts">
        <Count label={t("import.count.created")} value={d.created} />
        <Count label={t("import.count.updated")} value={d.updated} />
        <Count label={t("import.count.unchanged")} value={d.unchanged} />
        <Count label={t("import.count.skipped")} value={d.skipped} />
      </dl>
      <p className="import__hint">
        {t("import.rowsRead")
          .replace("{rows}", String(report.rows_read))
          .replace("{column}", report.source_key_used)}
      </p>

      {report.issues.length > 0 ? (
        <>
          <Callout tone="warn">{t("import.issuesLead")}</Callout>
          <ul className="import__issues">
            {report.issues.map((issue) => (
              <li key={`${issue.line}-${issue.reason}`}>
                {t("import.issueLine").replace("{line}", String(issue.line))}{" "}
                {issue.reason}
              </li>
            ))}
          </ul>
        </>
      ) : null}

      {resumable ? (
        <Callout tone="danger">
          {t("import.failed").replace("{checkpoint}", String(run.checkpoint))}
        </Callout>
      ) : null}

      {committed && !resumable ? (
        <Callout tone="success">{t("import.done")}</Callout>
      ) : (
        <Button variant="primary" disabled={busy} onClick={onCommit}>
          {busy
            ? t("import.importing")
            : resumable
              ? t("import.resume")
              : commitLabel(t, d.created + d.updated)}
        </Button>
      )}
      {error ? (
        <Callout tone="danger">{problemMessageOf(error, t)}</Callout>
      ) : null}
      {committed && !resumable ? (
        <Button variant="ghost" onClick={onRestart}>
          {t("import.another")}
        </Button>
      ) : null}
    </div>
  );
}

// commitLabel counts the rows the commit will write. One row is "1 row": the
// button is the last thing a human reads before the least reversible write in
// the product, and "1 rows" reads like a machine wrote it.
function commitLabel(t: (key: MessageKey) => string, rows: number): string {
  const key: MessageKey =
    rows === 1 ? "import.commit.one" : "import.commit.other";
  return t(key).replace("{rows}", String(rows));
}

function Count({ label, value }: Readonly<{ label: string; value: number }>) {
  return (
    <div className="import__count">
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

// columnFor answers which column was mapped onto a field — the one the run will
// identify rows by.
function columnFor(mapping: Record<string, string>, field: string): string {
  for (const [column, target] of Object.entries(mapping)) {
    if (target === field) {
      return column;
    }
  }
  return "";
}

// ImportMappingStep is the window in which a human chooses destinations: the
// table, what will identify a row, and the button that asks the server what
// the import would do.
//
// The identifier rule lives here rather than in the card because it is the one
// thing this step will not let pass — a mapping that identifies no row cannot
// be re-imported or undone, and the server would refuse it anyway.
function ImportMappingStep({
  profile,
  mapping,
  object,
  busy,
  pending,
  error,
  onChange,
  onValidate,
}: Readonly<{
  profile: ImportProfile;
  mapping: Record<string, string>;
  object: ImportObject;
  busy: boolean;
  pending: boolean;
  error: unknown;
  onChange: (column: string, target: string) => void;
  onValidate: () => void;
}>) {
  const t = useT();
  const identifying = identifyingFieldFor(object);
  const identifiedBy = columnFor(mapping, identifying);

  return (
    <>
      <ImportMappingTable
        profile={profile}
        mapping={mapping}
        onChange={onChange}
      />
      {identifiedBy ? (
        <p className="import__hint">
          {t("import.identifiedBy")
            .replace("{column}", identifiedBy)
            .replace("{field}", identifying)}
        </p>
      ) : (
        <Callout tone="warn">
          {t("import.needsIdentifier").replace("{field}", identifying)}
        </Callout>
      )}
      <Button
        variant="primary"
        disabled={busy || !identifiedBy}
        onClick={onValidate}
      >
        {pending ? t("import.validating") : t("import.validate")}
      </Button>
      {error ? (
        <Callout tone="danger">{problemMessageOf(error, t)}</Callout>
      ) : null}
    </>
  );
}
