// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Upload } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { useCan, useCanWrite } from "../app/capability";
import {
  Button,
  EmptyState,
  Modal,
  SegmentedControl,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatDateTime } from "../format/format";
import { viewerZone } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, useMe } from "./common";
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
// It lives beside the other operator-run bulk actions rather than in a nav entry
// of its own. On the settings page it is one row, because an import is an ACT
// rather than an answer this installation holds: the row states what the act is
// and carries the verb, and the steps that make up the act — object, file,
// mapping, dry run, commit, undo — belong to the dialog that verb opens.

export function ImportCard() {
  const t = useT();
  // The flow does not only create: the dry run parks the run (update), the
  // approval moves it (update), and every step reads it back (read). A role
  // edited to create-without-update would otherwise see the card and be
  // refused at the first button. useCanWrite folds the seat ceiling, which a
  // read-seat admin would otherwise hit as a clamped POST.
  const mayCreate = useCanWrite("import_run", "create");
  const mayAdvance = useCan("import_run", "update");
  const mayImport = mayCreate && mayAdvance;
  const me = useMe();
  const flow = useImportFlow();
  const headingId = useId();
  const [open, setOpen] = useState(false);
  // An operator who does not know their last import stopped half-way cannot
  // finish it. The flow reads a parked run back on mount, and inside a dialog
  // that run is behind a button nobody knows to press — so the dialog opens
  // itself when a run resumes.
  //
  // Guarded by a ref rather than driven by `resumed` alone, so it happens ONCE:
  // a reader who has looked at the recovered run and closed the dialog is not
  // fought by the next render.
  const openedForResumedRun = useRef(false);
  useEffect(() => {
    if (flow.resumed && !openedForResumedRun.current) {
      openedForResumedRun.current = true;
      setOpen(true);
    }
  }, [flow.resumed]);

  // Gated on the grant the STORE demands, not on the admin role: `import_run`
  // is seeded to admin AND ops, so asking for the role would hide the card
  // from an ops seat the server would have accepted.
  //
  // Withheld rather than absent, and gated on the PROBE rather than its absence.
  // Maintenance opens on `isAdmin || embedding_reindex:read`, so a seat holding
  // only the reindex grant reaches this page — and a card that simply is not
  // there tells them this installation cannot import, which is a claim about the
  // product rather than about their authority. Every capability hook also fails
  // closed while /me is in flight, so branching before it answers would flash
  // the notice at the admin who holds the grant.
  if (me.isSuccess && !mayImport) {
    return (
      <Panel title={t("import.title")}>
        <PanelBody>
          <EmptyState>
            <p className="t-small">{t("import.withheld")}</p>
          </EmptyState>
        </PanelBody>
      </Panel>
    );
  }
  if (!mayImport) {
    return null;
  }

  return (
    <Panel title={t("import.title")}>
      <PanelBody>
        <SettingList>
          <SettingRow
            label={t("import.startLabel")}
            description={t("import.sub")}
            control={
              <Button small variant="ghost" onClick={() => setOpen(true)}>
                {t("import.start")}
              </Button>
            }
          />
        </SettingList>
        {/* Wide: the mapping table is four columns of a file nobody chose the
            width of, and squeezing it costs the fill rates that decide the
            mapping. It keeps its own horizontal scroll inside the dialog. */}
        <Modal
          open={open}
          onClose={() => setOpen(false)}
          labelledBy={headingId}
          size="wide"
        >
          <h2 id={headingId} className="t-h2 modal-title">
            {t("import.title")}
          </h2>
          <ImportWizard flow={flow} onClose={() => setOpen(false)} />
        </Modal>
      </PanelBody>
    </Panel>
  );
}

// ImportWizard is the act itself, in the order it is performed: what the rows
// are, which file, where each column goes, what the run WILL do, and only then
// the commit — with the undo that follows it.
//
// It takes the flow rather than owning one: the flow is what recovers a parked
// run at mount, and a state machine that only existed while a dialog was open
// would forget an interrupted import the moment the dialog closed.
function ImportWizard({
  flow,
  onClose,
}: Readonly<{
  flow: ReturnType<typeof useImportFlow>;
  onClose: () => void;
}>) {
  const t = useT();
  const fileInput = useRef<HTMLInputElement>(null);
  const {
    profile,
    mapping,
    run,
    report,
    resumed,
    upload,
    validate,
    commit,
    undo,
  } = flow;

  // The mapping table is on screen while a file is profiled and no report has
  // been produced for it yet — the one window in which the human is choosing
  // destinations.
  const showMapping = profile !== null && report === null;
  const busy =
    upload.isPending ||
    validate.isPending ||
    commit.isPending ||
    undo.isPending;
  const committed =
    run?.status === "complete" ||
    run?.status === "failed" ||
    run?.status === "undoing" ||
    run?.status === "undone";

  return (
    <div className="import">
      {/* The control carries its own group label, so it needs no Field
          around it — a second label would announce the same words twice. */}
      <SegmentedControl
        options={["lead", "organization"] as const}
        value={flow.object}
        onChange={busy ? () => undefined : flow.chooseObject}
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
        // Cleared after every pick: a browser fires no change event when the
        // SAME path is chosen again, and the natural next move after reading
        // "Line 3 is empty" is to fix that line in that file and choose it
        // once more. Without this the click does nothing, the old report
        // stays on screen, and the commit button writes the FIRST upload's
        // bytes.
        onChange={(event) => {
          const file = event.target.files?.[0];
          event.target.value = "";
          if (file) {
            upload.mutate(file);
          }
        }}
        // Out of the tab order: it is invisible, so a keyboard user landing
        // on it has a focus stop they cannot see. The Button beside it is the
        // keyboard path, and the label keeps the input reachable by name.
        tabIndex={-1}
      />
      <Button
        small
        variant="ghost"
        disabled={busy}
        onClick={() => fileInput.current?.click()}
      >
        <Upload size={16} aria-hidden />
        <span>{profile ? t("import.chooseAnother") : t("import.choose")}</span>
      </Button>

      {upload.error ? (
        <Callout tone="danger" live="alert">
          {problemMessageOf(upload.error, t)}
        </Callout>
      ) : null}

      {showMapping ? (
        <ImportMappingStep
          profile={profile}
          mapping={mapping}
          object={flow.object}
          busy={busy}
          locked={validate.isPending}
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
          resumed={resumed}
          busy={busy}
          onCommit={() => commit.mutate(run)}
          onUndo={() => undo.mutate(run)}
          onRestart={flow.restart}
          error={commit.error}
          undoError={undo.error}
          undoBusy={undo.isPending}
        />
      ) : null}

      {/* Closing puts the act down; it does not abandon it. The flow outlives
          the dialog, so a reader who steps away comes back to the same step. */}
      <Button small onClick={onClose}>
        {t("common.close")}
      </Button>
    </div>
  );
}

// ImportOutcome draws one report — the dry run's prediction before approval and
// the run's own outcome after it, in the same shape, so a human comparing the
// two is comparing like with like.
function ImportOutcome({
  report,
  run,
  committed,
  resumed,
  busy,
  onCommit,
  onUndo,
  onRestart,
  error,
  undoError,
  undoBusy,
}: Readonly<{
  report: ImportReport;
  run: ImportRun;
  committed: boolean;
  // True when this run was read back on mount rather than produced by the
  // reader's last press. It changes nothing about what the card offers — only
  // whether the card says where the run came from.
  resumed: boolean;
  busy: boolean;
  onCommit: () => void;
  onUndo: () => void;
  onRestart: () => void;
  error: unknown;
  undoError: unknown;
  undoBusy: boolean;
}>) {
  const t = useT();
  // The run's own timestamp is rendered in the reader's locale, not the
  // browser's default: everything else on this card already follows the catalog.
  const { locale } = useLocale();
  const d = report.disposition;
  const resumable = run.status === "failed";
  // undoing doubles as "reversing" and "a reversal that stopped part-way" —
  // pressing the same button again resumes it, the same shape `resumable`
  // already gives the forward commit.
  const undoInterrupted = run.status === "undoing";
  const undone = run.status === "undone";
  // Only a run nobody has touched an undo on yet offers to start one — once
  // it is undoing or undone, the button below speaks to THAT state instead.
  const undoable = run.status === "complete";

  return (
    <div className="import__outcome">
      <h3 className="import__outcomeTitle">
        {committed ? t("import.outcomeTitle") : t("import.previewTitle")}
      </h3>
      {/* A run the reader did not just cause, shown as though they had, reads as
          an import that ran by itself — so the card says when it happened. */}
      {resumed ? (
        <Callout tone="info">
          {t("import.resumedRun", {
            when: formatDateTime(run.created_at, locale, viewerZone()),
          })}
        </Callout>
      ) : null}
      <dl className="import__counts">
        <Count label={t("import.count.created")} value={d.created} />
        <Count label={t("import.count.updated")} value={d.updated} />
        <Count label={t("import.count.unchanged")} value={d.unchanged} />
        <Count label={t("import.count.skipped")} value={d.skipped} />
      </dl>
      <p className="import__hint">
        {t("import.rowsRead", {
          rows: report.rows_read,
          column: report.source_key_used,
        })}
      </p>

      {report.issues.length > 0 ? (
        <>
          <Callout tone="warn" live="status">
            {t("import.issuesLead")}
          </Callout>
          <ul className="import__issues">
            {report.issues.map((issue) => (
              <li key={`${issue.line}-${issue.reason}`}>
                {t("import.issueLine", { line: issue.line })} {issue.reason}
              </li>
            ))}
          </ul>
        </>
      ) : null}

      {resumable ? (
        <Callout tone="danger" live="alert">
          {t("import.failed", { checkpoint: run.checkpoint })}
        </Callout>
      ) : null}

      {committed && !resumable && !undoInterrupted && !undone ? (
        <Callout tone="success" live="status">
          {t("import.done")}
        </Callout>
      ) : null}

      {!committed ? (
        <Button small variant="primary" disabled={busy} onClick={onCommit}>
          {busy ? t("import.importing") : commitLabel(t, d.created + d.updated)}
        </Button>
      ) : null}

      {resumable ? (
        <Button small variant="primary" disabled={busy} onClick={onCommit}>
          {busy ? t("import.importing") : t("import.resume")}
        </Button>
      ) : null}
      {error ? (
        <Callout tone="danger" live="alert">
          {problemMessageOf(error, t)}
        </Callout>
      ) : null}

      <UndoSection
        report={report}
        undoable={undoable}
        undoInterrupted={undoInterrupted}
        undone={undone}
        busy={busy}
        undoBusy={undoBusy}
        undoError={undoError}
        onUndo={onUndo}
      />

      {committed && !resumable ? (
        <Button small variant="ghost" onClick={onRestart}>
          {t("import.another")}
        </Button>
      ) : null}
    </div>
  );
}

// UndoSection is the whole reversal affordance, pulled out of ImportOutcome
// to keep that function's branching readable: the interrupted callout, the
// outcome once undone, the button (which doubles as "start" and "continue"),
// and its own error.
function UndoSection({
  report,
  undoable,
  undoInterrupted,
  undone,
  busy,
  undoBusy,
  undoError,
  onUndo,
}: Readonly<{
  report: ImportReport;
  undoable: boolean;
  undoInterrupted: boolean;
  undone: boolean;
  busy: boolean;
  undoBusy: boolean;
  undoError: unknown;
  onUndo: () => void;
}>) {
  const t = useT();
  return (
    <>
      {undoInterrupted ? (
        <Callout tone="warn" live="status">
          {t("import.undoInterrupted")}
        </Callout>
      ) : null}

      {undone ? <UndoOutcome undo={report.undo} /> : null}

      {undoable || undoInterrupted ? (
        <Button small variant="ghost" disabled={busy} onClick={onUndo}>
          {undoBusy
            ? t("import.undoing")
            : undoInterrupted
              ? t("import.continueUndo")
              : undoLabel(t, report.disposition.created)}
        </Button>
      ) : null}
      {undoError ? (
        <Callout tone="danger" live="alert">
          {problemMessageOf(undoError, t)}
        </Callout>
      ) : null}
    </>
  );
}

// UndoOutcome shows what a reversal did: how many rows it reversed, the
// "kept — you edited these" list (A93 — a human-edited row is disclosed by
// name, never silently rewritten back over what they typed), and any row
// that could not be reversed at all, named with why rather than dropped.
//
// undo can be absent even though the run is undone: the commit itself
// already finished server-side by the time the follow-up report read
// answers, so a failed read must still say the import was undone rather
// than rendering nothing.
function UndoOutcome({ undo }: Readonly<{ undo: ImportReport["undo"] }>) {
  const t = useT();
  return (
    <div className="import__undoOutcome">
      <Callout tone="success" live="status">
        {t("import.undone")}
      </Callout>
      {undo ? (
        <>
          <p className="import__hint">
            {t(
              undo.reversed_count === 1
                ? "import.undoReversed.one"
                : "import.undoReversed.other",
              { rows: undo.reversed_count },
            )}
          </p>
          {undo.kept.length > 0 ? (
            <>
              <p className="import__hint">{t("import.undoKeptLead")}</p>
              <ul className="import__issues">
                {undo.kept.map((row) => (
                  <li key={`${row.object}-${row.id}`}>
                    {t(`import.object.${row.object}`)} — {row.id}
                  </li>
                ))}
              </ul>
            </>
          ) : null}
          {undo.errored.length > 0 ? (
            <>
              <Callout tone="warn" live="status">
                {t("import.undoErroredLead")}
              </Callout>
              <ul className="import__issues">
                {undo.errored.map((row) => (
                  <li key={`${row.object}-${row.id}`}>
                    {t(`import.object.${row.object}`)} — {row.id}: {row.reason}
                  </li>
                ))}
              </ul>
            </>
          ) : null}
        </>
      ) : null}
    </div>
  );
}

// commitLabel counts the rows the commit will write. One row is "1 row": the
// button is the last thing a human reads before the least reversible write in
// the product, and "1 rows" reads like a machine wrote it.
function commitLabel(
  t: (key: MessageKey, params?: Record<string, string | number>) => string,
  rows: number,
): string {
  const key: MessageKey =
    rows === 1 ? "import.commit.one" : "import.commit.other";
  return t(key, { rows });
}

// undoLabel names the count on the undo button for the same reason
// commitLabel does: it is the last thing a human reads before undo
// archives every row this run created.
function undoLabel(
  t: (key: MessageKey, params?: Record<string, string | number>) => string,
  rows: number,
): string {
  const key: MessageKey = rows === 1 ? "import.undo.one" : "import.undo.other";
  return t(key, { rows });
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
  locked,
  pending,
  error,
  onChange,
  onValidate,
}: Readonly<{
  profile: ImportProfile;
  mapping: Record<string, string>;
  object: ImportObject;
  busy: boolean;
  // Set while the validation this mapping was sent with is in flight. A change
  // accepted now could not reach that request, so the table would show a
  // destination the run does not have.
  locked: boolean;
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
        locked={locked}
        onChange={onChange}
      />
      {identifiedBy ? (
        <p className="import__hint">
          {t("import.identifiedBy", {
            column: identifiedBy,
            field: identifying,
          })}
        </p>
      ) : (
        <Callout tone="warn">
          {t("import.needsIdentifier", { field: identifying })}
        </Callout>
      )}
      <Button
        small
        variant="primary"
        disabled={busy || !identifiedBy}
        onClick={onValidate}
      >
        {pending ? t("import.validating") : t("import.validate")}
      </Button>
      {error ? (
        <Callout tone="danger" live="alert">
          {problemMessageOf(error, t)}
        </Callout>
      ) : null}
    </>
  );
}
