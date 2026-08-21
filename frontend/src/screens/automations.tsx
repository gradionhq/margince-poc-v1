import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan, useCanWrite } from "../app/capability";
import {
  Badge,
  Button,
  Checkbox,
  EmptyState,
  Modal,
  OverflowMenu,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { Switch } from "../design-system/switch";
import { AutonomyDot } from "../design-system/trust";
import { useT } from "../i18n";
import { AutomationInspectors } from "./automationdetail";
import { DateFieldSelect } from "./automations.datefield";
import {
  problemMessageOf,
  QueryGate,
  type QueryLike,
  throwProblem,
  useMe,
} from "./common";
import "./automations.css";

// The automations editor (B-EP09.15): a management UI over the CLOSED
// catalog (E15/ADR-0035). The anti-DSL invariant of features/10 §1 holds by
// construction — every form field derives from the catalog entry's
// params_schema plus the instance name; there is no free-form rule body and
// no user-defined trigger anywhere on this surface, and a test pins that.
// Instances render from the Automation wire schema alone, so an
// agent-authored instance is indistinguishable from a catalog-authored one.

type CatalogEntry = components["schemas"]["AutomationCatalogEntry"];
type Automation = components["schemas"]["Automation"];

export type ParamField = {
  key: string;
  kind: "integer" | "string" | "boolean" | "date_field" | "enum";
  min?: number;
  max?: number;
  initial: string;
  // Set only for kind "enum" — the schema's own closed value list (e.g.
  // renewal_reminder's object property), rendered as a picker instead of
  // a free-text box so a typo can't silently name a value the backend
  // would refuse.
  options?: string[];
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// Catalog defaults and stored params are JSON scalars; anything non-scalar
// has no honest single-line rendering, so it collapses to empty.
function scalarText(value: unknown): string {
  if (value === undefined || value === null || typeof value === "object") {
    return "";
  }
  return String(value);
}

function paramKind(type: unknown): ParamField["kind"] | null {
  if (type === "integer" || type === "number") {
    return "integer";
  }
  if (type === "boolean") {
    return "boolean";
  }
  if (type === "string") {
    return "string";
  }
  return null;
}

// enumOptions reads a schema property's own closed value list, when it
// has one — a string-typed "enum" array is the schema's way of saying
// "pick one of these", which renders as a picker rather than free text
// so a typo can't silently name a value the backend would refuse.
function enumOptions(raw: Record<string, unknown>): string[] | undefined {
  if (!Array.isArray(raw.enum)) {
    return undefined;
  }
  const values = raw.enum.filter((v): v is string => typeof v === "string");
  return values.length > 0 ? values : undefined;
}

// The ONLY source of editable parameters: the catalog entry's JSON schema.
export function paramFields(schema: Record<string, unknown>): ParamField[] {
  const properties = isRecord(schema.properties) ? schema.properties : {};
  // renewal_reminder's date_field names a workspace's own cf_* column, not a
  // fixed value — but nothing in the JSON schema type system marks a string
  // as "this is a column reference". The schema DOES say which object owns
  // it (its sibling `object` property), and only a schema declaring BOTH has
  // enough context to resolve a column list, so that pairing — not the bare
  // key name, which some future automation could reuse for something
  // unrelated — is what selects the picker over a free-text box.
  const isDateFieldPicker =
    "object" in properties && "date_field" in properties;
  return Object.entries(properties).flatMap(([key, raw]) => {
    if (!isRecord(raw)) {
      return [];
    }
    const options = enumOptions(raw);
    const kind: ParamField["kind"] | null =
      key === "date_field" && isDateFieldPicker
        ? "date_field"
        : options
          ? "enum"
          : paramKind(raw.type);
    if (kind === null) {
      return [];
    }
    return [
      {
        key,
        kind,
        min: typeof raw.minimum === "number" ? raw.minimum : undefined,
        max: typeof raw.maximum === "number" ? raw.maximum : undefined,
        initial: scalarText(raw.default),
        options,
      },
    ];
  });
}

function paramsFromValues(
  fields: ParamField[],
  values: Record<string, string>,
): Record<string, unknown> {
  return Object.fromEntries(
    fields.map((field) => {
      const value = values[field.key] ?? field.initial;
      if (field.kind === "integer") {
        return [field.key, Number(value)];
      }
      if (field.kind === "boolean") {
        return [field.key, value === "true"];
      }
      return [field.key, value];
    }),
  );
}

// One schema-derived param's control, lifted out of AutomationForm so that
// function stays a shape a reader can hold at once as this grows a third
// input kind. A Checkbox carries its own label (the design system's own
// Checkbox/Switch split: this STATES an intent the Save button below then
// submits, so the field's usual heading span would just repeat it) — every
// other kind keeps the heading span the rest of the form uses.
function ParamFieldControl({
  field,
  formId,
  value,
  object,
  onChange,
}: Readonly<{
  field: ParamField;
  formId: string;
  value: string;
  object: string;
  onChange: (value: string) => void;
}>) {
  if (field.kind === "boolean") {
    return (
      <div className="field">
        <Checkbox
          label={field.key}
          checked={value === "true"}
          onChange={(event) =>
            onChange(event.target.checked ? "true" : "false")
          }
        />
      </div>
    );
  }
  return (
    <div className="field">
      <span className="t-label" id={`${formId}-${field.key}`}>
        {field.key}
      </span>
      {field.kind === "date_field" ? (
        <DateFieldSelect
          object={object}
          value={value}
          onChange={onChange}
          labelId={`${formId}-${field.key}`}
        />
      ) : field.kind === "enum" ? (
        <Select
          aria-labelledby={`${formId}-${field.key}`}
          options={(field.options ?? []).map((v) => ({ value: v, label: v }))}
          value={value}
          onChange={onChange}
        />
      ) : (
        <TextInput
          type={field.kind === "integer" ? "number" : "text"}
          aria-labelledby={`${formId}-${field.key}`}
          min={field.min}
          max={field.max}
          required
          value={value}
          onChange={(event) => onChange(event.target.value)}
        />
      )}
    </div>
  );
}

// Pick-a-template + fill-parameters (B-E15.7b1). Also serves the edit flow:
// initial values arrive from the instance instead of the schema defaults.
//
// It is the BODY of a dialog in both cases, never a panel that unfolds under a
// row: a name plus every parameter the schema declares is a form submitted
// together, and the settings page keeps a row an ANSWER by putting the form
// behind the verb. So it draws the dialog's own heading — the caller owns the
// id, since `Modal` needs it before this renders.
function AutomationForm({
  entry,
  titleId,
  initialName,
  initialParams,
  submitLabel,
  pending,
  onSubmit,
  onCancel,
}: Readonly<{
  entry: CatalogEntry;
  /** The id `Modal`'s `labelledBy` points at; this form's heading carries it. */
  titleId: string;
  initialName: string;
  initialParams?: Automation["params"];
  submitLabel: string;
  pending: boolean;
  onSubmit: (name: string, params: Record<string, unknown>) => void;
  onCancel: () => void;
}>) {
  const t = useT();
  const formId = useId();
  const fields = paramFields(entry.params_schema);
  const [name, setName] = useState(initialName);
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      fields.map((field) => {
        const configured = initialParams?.[field.key];
        return [
          field.key,
          configured === undefined ? field.initial : scalarText(configured),
        ];
      }),
    ),
  );

  return (
    <form
      className="form-stack"
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(name.trim() || entry.name, paramsFromValues(fields, values));
      }}
    >
      {/* The dialog covers the row that would otherwise have said which
          automation is open, so the heading says it instead. */}
      <h2 className="t-h3 modal-title" id={titleId}>
        {initialName}
      </h2>
      <p className="t-mono t-small">
        {entry.trigger} {"->"} {entry.action}
      </p>
      <div className="field">
        <span className="t-label" id={`${formId}-name`}>
          {t("auto.name")}
        </span>
        <TextInput
          aria-labelledby={`${formId}-name`}
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </div>
      {fields.map((field) => (
        <ParamFieldControl
          key={field.key}
          field={field}
          formId={formId}
          value={values[field.key] ?? field.initial}
          object={values.object ?? ""}
          onChange={(next) =>
            setValues((current) => ({ ...current, [field.key]: next }))
          }
        />
      ))}
      <div className="form-actions">
        {/* Cancel first, submit last: the house submit row reads left to right
            towards the primary action. Save STARTED the write, so it goes busy
            and keeps the focus the reader is standing on; Cancel started
            nothing and is simply not available while the write is out, since
            backing out of something already on its way to the server would say
            it was stopped. */}
        <Button small disabled={pending} onClick={onCancel}>
          {t("deals.cancel")}
        </Button>
        <Button type="submit" variant="primary" small pending={pending}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}

// One instance row, rendered from the Automation wire schema alone — no
// origin field exists on the wire, so authorship cannot change the render.
// The two inspector toggles, lifted out of the row so the row stays under the
// cognitive-complexity gate. They travel together: both are reads of the same
// automation, admitted by the same grant.
function InspectorToggles({
  runsOpen,
  previewOpen,
  onToggleRuns,
  onTogglePreview,
}: Readonly<{
  runsOpen: boolean;
  previewOpen: boolean;
  onToggleRuns: () => void;
  onTogglePreview: () => void;
}>) {
  const t = useT();
  return (
    <>
      <Button
        small
        variant={runsOpen ? "primary" : "ghost"}
        aria-expanded={runsOpen}
        onClick={onToggleRuns}
      >
        {t("auto.runs.open")}
      </Button>
      <Button
        small
        variant={previewOpen ? "primary" : "ghost"}
        aria-expanded={previewOpen}
        onClick={onTogglePreview}
      >
        {t("auto.preview.open")}
      </Button>
    </>
  );
}

// Deleting an automation drops the rule entirely — the records it watched stop
// being watched — so it asks first, the way every other destructive verb in
// settings does. It owns its own mutation and its own staged state rather than
// borrowing the row's: the question, the refusal and the write are one thing,
// and keeping them together is what lets the row stay a row.
//
// The refusal stays IN the dialog, which is where the reader still is. A row
// that reported it underneath would report it behind the thing covering it.
function DeleteAutomationAction({
  automation,
}: Readonly<{ automation: Automation }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [asking, setAsking] = useState(false);

  const remove = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/automations/{id}", {
        params: { path: { id: automation.id } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      setAsking(false);
      queryClient.invalidateQueries({ queryKey: ["automations"] });
    },
  });

  return (
    <>
      <Button
        small
        variant="danger"
        disabled={remove.isPending}
        onClick={() => setAsking(true)}
      >
        {t("auto.delete")}
      </Button>
      <ConfirmModal
        open={asking}
        onClose={() => {
          setAsking(false);
          remove.reset();
        }}
        title={t("auto.deleteTitle")}
        confirmLabel={t("auto.delete")}
        confirmVariant="danger"
        pending={remove.isPending}
        error={remove.isError ? problemMessageOf(remove.error, t) : null}
        onConfirm={() => remove.mutate()}
      >
        <p>{t("auto.deleteBody", { name: automation.name })}</p>
      </ConfirmModal>
    </>
  );
}

// Four affordances over three grants. The runs and preview inspectors are
// READS — automations_runs.go gates on automation:read — so they are not hidden
// behind the write grant the old role proxy happened to imply.
//
// Preview carries one gate this cannot anticipate: after resolving the instance
// through Get, it also demands read on the TARGET TABLE the recipe names, which
// varies per automation and is not something the /me snapshot describes. A
// reader without that table can still open the panel and be refused; the panel
// reports it. Predicting it here would mean encoding the catalog's table
// mapping in the client, which is the kind of server knowledge this change
// exists to stop duplicating.
// What a row PATCHes: the automation's definition, or just its on/off status.
type AutomationPatchBody = {
  name?: string;
  params?: Record<string, unknown>;
  status?: "enabled" | "paused";
};

// The whole write, row identity included. The `mutationFn` used to close over
// `automation.id` and `automation.version` instead, which is the pattern this
// tree has a rule against: the click handler belongs to the committed render,
// so anything it PASSES cannot be older than the control the reader pressed,
// while anything it CLOSES OVER can be. Here the stale value would be the
// `If-Match` version, and a write carrying one is refused as a concurrent edit
// — the reader is told someone else changed the automation when nobody did.
type AutomationPatch = {
  id: string;
  version: number | undefined;
  body: AutomationPatchBody;
};

// Which of a row's two writes a body describes. The request body is what tells
// them apart — only a status flip carries `status` — and one mutation serves
// both the enable switch and the edit dialog, so this is what keeps each of
// them from speaking for the other: a switch reporting a flip nobody made
// while the reader was pressing Save, or a refusal reported behind the dialog
// that is covering it.
function writeTarget(
  write: AutomationPatch | undefined,
): "status" | "definition" {
  return write?.body.status === undefined ? "definition" : "status";
}

// Which of a row's two writes is in flight, or neither.
function rowWriteInFlight(
  isPending: boolean,
  write: AutomationPatch | undefined,
): "none" | "status" | "definition" {
  if (!isPending) {
    return "none";
  }
  return writeTarget(write);
}

// Whether the automation is on, in the one place the row's answers sit.
//
// A Switch, because flipping it IS the write: the old pair was a button whose
// label named the NEXT state beside a badge naming the current one, so the row
// said "Pause" and "enabled" and left the reader to work out which of the two
// their click would produce. Without the update grant there is nothing to flip
// and the badge comes back — the state is a read this row still owes, and the
// card says once, above the list, why the control is not here.
function AutomationStatus({
  automation,
  canEdit,
  pending,
  onChange,
}: Readonly<{
  automation: Automation;
  canEdit: boolean;
  pending: boolean;
  onChange: (next: boolean) => void;
}>) {
  const t = useT();
  const enabled = automation.status === "enabled";
  if (!canEdit) {
    return (
      <Badge tone={enabled ? "success" : "warn"}>
        {enabled ? t("auto.statusEnabled") : t("auto.statusPaused")}
      </Badge>
    );
  }
  return (
    <Switch
      // Named for the automation it governs, like the row's menu beside it:
      // twenty switches all announcing "Enabled" tell a reader which control
      // they are on and nothing about which rule it belongs to. labelHidden
      // because the row already prints the name in view — the words are for the
      // announcement, not a second copy on screen.
      label={t("auto.enabledFor", { name: automation.name })}
      labelHidden
      checked={enabled}
      pending={pending}
      onChange={onChange}
    />
  );
}

// The definition editor, behind the row's Edit verb.
//
// A name plus every parameter the schema declares is a form submitted
// together, so it is a dialog rather than a panel that unfolds under the row —
// which is what stopped the list reading as a list. Its own refusal stays
// inside it, because the dialog is covering the row that would otherwise have
// reported it.
function AutomationEditor({
  automation,
  entry,
  pending,
  refusal,
  onSubmit,
  onClose,
}: Readonly<{
  automation: Automation;
  entry: CatalogEntry;
  pending: boolean;
  /** The server's own words for a refused save, or null while there is none. */
  refusal: string | null;
  onSubmit: (name: string, params: Record<string, unknown>) => void;
  onClose: () => void;
}>) {
  const t = useT();
  const titleId = useId();
  return (
    <Modal open onClose={onClose} labelledBy={titleId}>
      <AutomationForm
        entry={entry}
        titleId={titleId}
        initialName={automation.name}
        initialParams={automation.params}
        submitLabel={t("trust.save")}
        pending={pending}
        onSubmit={onSubmit}
        onCancel={onClose}
      />
      {refusal !== null && (
        <p className="t-caption auto-error" role="alert">
          {refusal}
        </p>
      )}
    </Modal>
  );
}

export function AutomationRow({
  automation,
  entry,
  canViewRuns,
  canEdit,
  canDelete,
}: Readonly<{
  automation: Automation;
  entry?: CatalogEntry;
  canViewRuns: boolean;
  canEdit: boolean;
  canDelete: boolean;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  // An open edit form whose grant was revoked would keep offering Save, and
  // every submission would be a guaranteed 403. Close it with the permission.
  useEffect(() => {
    if (!canEdit) {
      setEditing(false);
    }
  }, [canEdit]);
  // Two independent panels (run history + dry-run preview): each mounts lazily
  // only while open, and opening one never closes the other.
  const [runsOpen, setRunsOpen] = useState(false);
  const [previewOpen, setPreviewOpen] = useState(false);

  const patch = useMutation({
    mutationFn: async ({ id, version, body }: AutomationPatch) => {
      const { data, error } = await api.PATCH("/automations/{id}", {
        params: {
          path: { id },
          header: version === undefined ? {} : { "If-Match": String(version) },
        },
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      setEditing(false);
      queryClient.invalidateQueries({ queryKey: ["automations"] });
    },
  });

  const writeInFlight = rowWriteInFlight(patch.isPending, patch.variables);
  // A refusal is reported where the reader still is. The dialog covers the row,
  // so the definition's refusal belongs inside it and only the switch's lands
  // underneath.
  const refused = patch.isError ? writeTarget(patch.variables) : "none";
  const refusal = patch.isError ? problemMessageOf(patch.error, t) : null;
  // The row offers at least one verb worth folding away. With none of the three
  // grants there is nothing behind the control, and a menu that opens on an
  // empty panel is worse than no menu.
  const hasVerbs = (canEdit && entry !== undefined) || canViewRuns || canDelete;

  return (
    <li className="auto-row" data-automation={automation.id}>
      <div className="auto-row-head">
        {entry?.tier && (
          <AutonomyDot
            tier={entry.tier === "auto_execute" ? "auto" : "confirm"}
          />
        )}
        <strong>{automation.name}</strong>
        <span className="t-mono t-small">{automation.key}</span>
        <span className="t-mono t-small">
          {Object.entries(automation.params)
            .map(([key, value]) => `${key}=${scalarText(value)}`)
            .join(" ")}
        </span>
        <span className="auto-row-fill" />
        <AutomationStatus
          automation={automation}
          canEdit={canEdit}
          pending={writeInFlight === "status"}
          onChange={(next) =>
            patch.mutate({
              id: automation.id,
              version: automation.version,
              body: { status: next ? "enabled" : "paused" },
            })
          }
        />
        {/* Four verbs of equal visual weight, one of them irreversible, used to
            sit in a row separated from the rest only by a flex spacer. The menu
            is what the design system offers for exactly that: the row carries
            what the automation IS and whether it is on, and the things a reader
            occasionally does to it live one click away. */}
        {hasVerbs && (
          <OverflowMenu label={t("auto.rowActions", { name: automation.name })}>
            {canEdit && entry && (
              <Button small onClick={() => setEditing(true)}>
                {t("trust.edit")}
              </Button>
            )}
            {canViewRuns && (
              <InspectorToggles
                runsOpen={runsOpen}
                previewOpen={previewOpen}
                onToggleRuns={() => setRunsOpen((open) => !open)}
                onTogglePreview={() => setPreviewOpen((open) => !open)}
              />
            )}
            {canDelete && <DeleteAutomationAction automation={automation} />}
          </OverflowMenu>
        )}
      </div>
      <AutomationInspectors
        automationId={automation.id}
        runsOpen={runsOpen}
        previewOpen={previewOpen}
        canConfigure={canViewRuns}
      />
      {/* Mounted only while open, so each open re-seeds the fields from the
          automation as it now stands instead of reviving a draft typed before
          somebody else changed it. */}
      {editing && entry && (
        <AutomationEditor
          automation={automation}
          entry={entry}
          pending={writeInFlight === "definition"}
          refusal={refused === "definition" ? refusal : null}
          onSubmit={(name, params) =>
            patch.mutate({
              id: automation.id,
              version: automation.version,
              body: { name, params },
            })
          }
          onClose={() => setEditing(false)}
        />
      )}
      {/* A refused flip moves nothing on screen — so this line is the only
          report that it did not land, and it has to be spoken. The edit
          dialog's own refusal stays inside it, and so does the delete's. */}
      {refused === "status" && (
        <p className="t-caption auto-error" role="alert">
          {refusal}
        </p>
      )}
    </li>
  );
}

// Set-and-forget configuration, so it lives inside Settings → AI rather than
// on a nav destination of its own: this renders as one SECTION of that page.
// The page owns the `.wrap` reading column and the h1 naming the tab, so this
// contributes neither — a second `.wrap` would double the page padding and a
// second h1 would give the document two page titles.
export function AutomationsAdmin() {
  const t = useT();
  const queryClient = useQueryClient();
  const createTitleId = useId();
  const [template, setTemplate] = useState<CatalogEntry | null>(null);
  // Grants come from the session (/v1/me); until they arrive every predicate
  // is false, so the section shows no mutation affordance until one is confirmed.
  const me = useMe();
  const canViewRuns = useCan("automation", "read");
  const canCreate = useCanWrite("automation", "create");
  const canEdit = useCanWrite("automation", "update");
  const canDelete = useCanWrite("automation", "delete");
  // The create form is staged in `template`; it must not survive the grant that
  // opened it, or Create becomes a button that can only 403.
  useEffect(() => {
    if (!canCreate) {
      setTemplate(null);
    }
  }, [canCreate]);

  const catalog = useQuery({
    queryKey: ["automation-catalog"],
    queryFn: async () => {
      const { data, error } = await api.GET("/automations/catalog");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  // Gated on the grant the SERVER demands: AutomationStore.List calls
  // auth.Require(automation, read), so without it this query could only 403 —
  // a settled denial rendered as a failure with a Retry that cannot succeed.
  // The catalog above is deliberately NOT gated: ListAutomationCatalog reaches
  // no store and requires no object grant, so the starter library is readable
  // by anyone the session admits.
  const instances = useQuery({
    queryKey: ["automations"],
    enabled: canViewRuns,
    queryFn: async () => {
      const { data, error } = await api.GET("/automations", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const create = useMutation({
    mutationFn: async (input: {
      key: string;
      name: string;
      params: Record<string, unknown>;
    }) => {
      const { data, error } = await api.POST("/automations", {
        body: input,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      setTemplate(null);
      queryClient.invalidateQueries({ queryKey: ["automations"] });
    },
  });

  const entryFor = (key: string): CatalogEntry | undefined =>
    catalog.data?.data.find((entry) => entry.key === key);

  return (
    // ONE panel, and its body is the settings page's row language: two
    // decisions, each of which IS a list rather than an answer to a question
    // that would fit beside it, so both take the full width below their naming.
    // The old shape put them side by side in two columns with hard 240px/280px
    // floors — the tightest in settings — against about 308px of card interior
    // on a phone, and gave each a heading of its own on top of the panel's.
    //
    // `data-automations-admin` still marks the one addressable region, so a
    // reader — and the acceptance suite — can say "the automations surface"
    // rather than "the whole settings page".
    <Panel title={t("nav.automations")}>
      <PanelBody>
        <p className="settings-panel-sub">{t("auto.sub")}</p>
        {/* Bound to the grant the CONTROL asks for. It read "no create AND no
            edit AND no delete" while the row swaps its Switch for a Badge on
            `update` alone — so a seat holding create but not update lost the
            toggle with nothing on the page saying why, which is the one thing
            this line exists to prevent. */}
        {me.isSuccess && !canEdit && (
          <p className="t-caption auto-readonly">{t("auto.readOnly")}</p>
        )}
        <div data-automations-admin>
          <SettingList>
            {/* What is running comes first: the library below it is only ever
                read in order to add to this. */}
            <ConfiguredAutomationsRow
              instances={instances}
              me={me}
              entryFor={entryFor}
              canViewRuns={canViewRuns}
              canEdit={canEdit}
              canDelete={canDelete}
            />
            <StarterLibraryRow
              catalog={catalog}
              canCreate={canCreate}
              onUse={setTemplate}
            />
          </SettingList>
        </div>
        {/* The outcome lands on the CARD, because by the time it is true the
            dialog that produced it is gone. */}
        {create.isSuccess && (
          <p className="t-caption auto-outcome" role="status">
            {t("auto.createdPaused")}
          </p>
        )}
        {/* Name and parameters are one form submitted together, so they live
            behind the library's verb rather than unfolding under it. Mounted
            only while a template is staged, which is what re-seeds the fields
            from that template's own defaults on every open. */}
        {template && (
          <Modal
            open
            onClose={() => setTemplate(null)}
            labelledBy={createTitleId}
          >
            <AutomationForm
              key={template.key}
              entry={template}
              titleId={createTitleId}
              initialName={template.name}
              submitLabel={t("auto.create")}
              pending={create.isPending}
              onSubmit={(name, params) =>
                create.mutate({ key: template.key, name, params })
              }
              onCancel={() => setTemplate(null)}
            />
            {/* The refusal stays where the reader is: the dialog is still open
                over the card, so a line underneath it would report the failure
                behind the thing covering it. */}
            {create.isError && (
              <p className="t-caption auto-error" role="alert">
                {problemMessageOf(create.error, t)}
              </p>
            )}
          </Modal>
        )}
      </PanelBody>
    </Panel>
  );
}

// What is running, as the subject of one stacked row: a list of rules at the
// card's full width, never squeezed into the column an answer would sit in.
//
// Without the read grant the row keeps its place and says it is WITHHELD — not
// absent, and not empty: an empty instance list says this installation runs no
// automations, which is a claim about the workspace rather than about who may
// read it. Behind the /me probe, so it states a settled denial rather than the
// absence of an answer.
function ConfiguredAutomationsRow({
  instances,
  me,
  entryFor,
  canViewRuns,
  canEdit,
  canDelete,
}: Readonly<{
  instances: QueryLike<{ data: Automation[] }>;
  me: QueryLike<unknown>;
  entryFor: (key: string) => CatalogEntry | undefined;
  canViewRuns: boolean;
  canEdit: boolean;
  canDelete: boolean;
}>) {
  const t = useT();
  return (
    <SettingRow
      label={t("auto.instances")}
      layout="stack"
      control={
        canViewRuns ? (
          <QueryGate query={instances} empty={(page) => page.data.length === 0}>
            {(page) => (
              <ul className="auto-instances">
                {page.data.map((automation) => (
                  <AutomationRow
                    key={automation.id}
                    automation={automation}
                    entry={entryFor(automation.key)}
                    canViewRuns={canViewRuns}
                    canEdit={canEdit}
                    canDelete={canDelete}
                  />
                ))}
              </ul>
            )}
          </QueryGate>
        ) : (
          <QueryGate query={me}>
            {() => (
              <EmptyState>
                <p className="t-small">{t("auto.withheld")}</p>
              </EmptyState>
            )}
          </QueryGate>
        )
      }
    />
  );
}

// The closed catalog, as the subject of the card's other stacked row.
//
// It is a LIST and stays in the card: every seat may read it —
// ListAutomationCatalog reaches no store and asks for no object grant — so it
// is the per-entry verb that answers to the create grant, never the list.
// Authoring is what that verb opens.
function StarterLibraryRow({
  catalog,
  canCreate,
  onUse,
}: Readonly<{
  catalog: QueryLike<{ data: CatalogEntry[] }>;
  canCreate: boolean;
  onUse: (entry: CatalogEntry) => void;
}>) {
  const t = useT();
  return (
    <SettingRow
      label={t("auto.catalog")}
      description={t("auto.catalogSub")}
      layout="stack"
      control={
        <QueryGate query={catalog} empty={(page) => page.data.length === 0}>
          {(page) => (
            // A nested SettingList, so the interval between two entries and the
            // hairline that separates them are the row language's own. As a bare
            // `<ul>` each entry ran three text lines together with no rule
            // anywhere and no interval between the lines — a wall of names,
            // sentences and identifiers with a verb floating at the right.
            <SettingList testId="auto-catalog">
              {page.data.map((entry) => (
                <CatalogEntryItem
                  key={entry.key}
                  entry={entry}
                  canCreate={canCreate}
                  onUse={() => onUse(entry)}
                />
              ))}
            </SettingList>
          )}
        </QueryGate>
      }
    />
  );
}

// One entry of the closed catalog as ONE row: what it is on the left — the name,
// what it does, and the trigger/action pair in the wire's own words — and, for a
// seat holding create, the verb that turns it into a configured automation at
// the x every answer on this page sits at.
//
// The recipe joins the DESCRIPTION rather than standing as a third line of its
// own: it is what the entry does, said in identifiers instead of prose, so the
// two together are the naming and the row has one naming column and one answer.
function CatalogEntryItem({
  entry,
  canCreate,
  onUse,
}: Readonly<{
  entry: CatalogEntry;
  canCreate: boolean;
  onUse: () => void;
}>) {
  const t = useT();
  return (
    <SettingRow
      label={
        <span className="auto-catalog-name">
          {entry.tier && (
            <AutonomyDot
              tier={entry.tier === "auto_execute" ? "auto" : "confirm"}
            />
          )}
          {entry.name}
        </span>
      }
      description={
        <>
          {entry.description}
          <span className="t-mono auto-catalog-recipe">
            {entry.trigger} {"->"} {entry.action}
          </span>
        </>
      }
      control={
        canCreate ? (
          <Button small variant="ghost" onClick={onUse}>
            {t("auto.use")}
          </Button>
        ) : null
      }
    />
  );
}
