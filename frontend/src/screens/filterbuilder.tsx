// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The predicate builder: the visual half of the filter tree
// (AC-filters-and-views-3/4).
//
// Every edit goes through segmentpredicate's immutable operations and every
// choice offered comes from the server's vocabulary. That pairing is the whole
// design: the tree module knows what a filter IS, the vocabulary knows what a
// filter MAY SAY, and this file knows only how to draw the two. It invents no
// field, no operator, and no value type of its own — which is why a clause it
// builds cannot be one the engine refuses as unknown.
//
// Rendering is recursive because the tree is. A group draws its children and its
// own join toggle; a child is either a clause row or another group, and the same
// component handles both depths, so nesting is not a special case to maintain.

import { useId } from "react";
import { Badge, Button, SegmentedControl } from "../design-system/atoms";
import { DateInput } from "../design-system/dateinput";
import { Select, type SelectOption } from "../design-system/select";
import { TokenInput } from "../design-system/tokeninput";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import "./filterbuilder.css";
import { RosterPartialNote } from "./entityref";
import { fieldLabel, groupFields, type VocabularyField } from "./filterdata";
import {
  boundedReference,
  type Reference,
  useReferenceOptions,
} from "./filterreference";
import {
  addToGroup,
  type FilterOp,
  type Group,
  isGroup,
  type Leaf,
  type LeafValue,
  type Node,
  newGroup,
  newLeaf,
  removeNode,
  replaceNode,
  toggleJoin,
} from "./segmentpredicate";

/**
 * Which message names each operator, for a field whose comparisons read as dates.
 *
 * Keyed by reading rather than by symbol: `gte` is "on or after" a date and "at
 * least" a quantity, and one label serving both would send a reader looking for a
 * calendar on a score.
 */
const OPERATOR_KEY: Record<FilterOp, MessageKey> = {
  eq: "filters.op.eq",
  neq: "filters.op.neq",
  gt: "filters.op.afterDate",
  gte: "filters.op.onOrAfterDate",
  lt: "filters.op.beforeDate",
  lte: "filters.op.onOrBeforeDate",
  in: "filters.op.in",
  contains: "filters.op.contains",
  exists: "filters.op.exists",
};

/** The four whose reading changes on a quantity. */
const NUMERIC_OPERATOR_KEY: Partial<Record<FilterOp, MessageKey>> = {
  gt: "filters.op.moreThan",
  gte: "filters.op.atLeast",
  lt: "filters.op.lessThan",
  lte: "filters.op.atMost",
};

function operatorKey(op: FilterOp, type: VocabularyField["type"]): MessageKey {
  if (type === "number" || type === "currency") {
    return NUMERIC_OPERATOR_KEY[op] ?? OPERATOR_KEY[op];
  }
  return OPERATOR_KEY[op];
}

/**
 * The value a clause starts with when its field or operator changes.
 *
 * It is deliberately the EMPTY one for its shape rather than a guess: an operand
 * this screen invented would be a filter the human did not write, and the count
 * would move before they had said anything. `isComplete` then holds the preview
 * back until they fill it in, which is the whole reason an empty value is safe to
 * put in the tree.
 *
 * `exists` is the exception, because its operand is a boolean and every boolean
 * is complete — so it starts at `true` ("has a value"), the reading its label
 * gives.
 */
function emptyValueFor(op: FilterOp): LeafValue {
  if (op === "exists") {
    return true;
  }
  return op === "in" ? [] : "";
}

export type FilterBuilderProps = Readonly<{
  tree: Node;
  onChange: (next: Node) => void;
  fields: readonly VocabularyField[];
}>;

/** The builder's root: one group, drawn with the recursive renderer below. */
export function FilterBuilder({ tree, onChange, fields }: FilterBuilderProps) {
  if (!isGroup(tree)) {
    // The root is always a group — a bare leaf has nowhere to put the join
    // toggle, and every operation in segmentpredicate assumes a group root.
    return null;
  }
  return (
    <div className="filter-builder">
      <GroupNode
        group={tree}
        fields={fields}
        depth={1}
        onChange={onChange}
        tree={tree}
      />
    </div>
  );
}

type NodeProps = Readonly<{
  fields: readonly VocabularyField[];
  depth: number;
  /** The whole tree, because every edit is expressed against its root. */
  tree: Node;
  onChange: (next: Node) => void;
}>;

/**
 * One group: its join toggle, its children, and the two ways to grow it.
 *
 * Depth is carried only to stop offering "add a group" past the engine's nesting
 * bound — a builder that let a human nest a fifth level would be building a tree
 * the server refuses with `filter_too_deep`, which is a refusal the UI can see
 * coming.
 */
function GroupNode({
  group,
  fields,
  depth,
  tree,
  onChange,
}: NodeProps & Readonly<{ group: Group }>) {
  const t = useT();
  const canNest = depth < MAX_GROUP_DEPTH;
  return (
    <div className="filter-group" data-depth={depth}>
      <div className="filter-group-head">
        <SegmentedControl
          options={["and", "or"] as const}
          value={group.join}
          onChange={() => onChange(toggleJoin(tree, group.id))}
          labels={{ and: t("filters.joinAll"), or: t("filters.joinAny") }}
          label={t("filters.joinLabel")}
        />
        {depth > 1 && (
          <Button
            variant="ghost"
            small
            onClick={() => onChange(removeNode(tree, group.id))}
          >
            {t("filters.removeGroup")}
          </Button>
        )}
      </div>

      <div className="filter-group-children">
        {group.children.map((child) =>
          isGroup(child) ? (
            <GroupNode
              key={child.id}
              group={child}
              fields={fields}
              depth={depth + 1}
              tree={tree}
              onChange={onChange}
            />
          ) : (
            <ClauseRow
              key={child.id}
              leafID={child.id}
              field={child.field}
              op={child.op}
              value={child.value}
              fields={fields}
              tree={tree}
              onChange={onChange}
            />
          ),
        )}
        {group.children.length === 0 && (
          <p className="filter-group-empty">{t("filters.emptyGroup")}</p>
        )}
      </div>

      <div className="filter-group-actions">
        <Button
          variant="ghost"
          small
          onClick={() =>
            onChange(addToGroup(tree, group.id, firstClause(fields)))
          }
        >
          {t("filters.addClause")}
        </Button>
        {canNest && (
          <Button
            variant="ghost"
            small
            onClick={() =>
              onChange(
                addToGroup(
                  tree,
                  group.id,
                  newGroup(group.join === "and" ? "or" : "and"),
                ),
              )
            }
          >
            {t("filters.addGroup")}
          </Button>
        )}
      </div>
    </div>
  );
}

/**
 * The engine's own nesting bound. Named here rather than imported because the
 * tree module does not enforce it — the server does, and this is the UI's copy of
 * a number whose authority is `storekit.PredicateMaxDepth`. It exists to stop
 * OFFERING what would be refused; the refusal itself remains the server's.
 */
const MAX_GROUP_DEPTH = 4;

/** A new clause starts on the first field a picker would offer. */
function firstClause(fields: readonly VocabularyField[]): Node {
  const first = fields[0];
  if (!first) {
    // A resource with no filterable field cannot happen through this screen —
    // the vocabulary read 404s rather than answering an empty set — but the tree
    // must still be a valid node if it ever did.
    return newLeaf("", "eq", "");
  }
  const op = (first.operators[0] ?? "eq") as FilterOp;
  return newLeaf(first.name, op, emptyValueFor(op));
}

// Deliberately NOT NodeProps: a clause has no children, so depth would be a
// prop it accepts and ignores — and a prop nothing reads is one the next person
// has to check before trusting.
type ClauseRowProps = Readonly<{
  leafID: string;
  field: string;
  op: FilterOp;
  value: LeafValue;
  fields: readonly VocabularyField[];
  tree: Node;
  onChange: (next: Node) => void;
}>;

/**
 * One clause: field, operator, value, remove.
 *
 * The three selects are chained by the vocabulary rather than independent. Change
 * the field and the operator list changes with its type; change either and the
 * value control changes shape. Keeping them chained is what stops a reader
 * assembling `created_at contains "x"` and learning from a 422 that dates have no
 * substring.
 */
function ClauseRow({
  leafID,
  field,
  op,
  value,
  fields,
  tree,
  onChange,
}: ClauseRowProps) {
  const t = useT();
  const chosen = fields.find((f) => f.name === field);
  const { core, custom } = groupFields(fields);
  const fieldOptions: SelectOption[] = [
    ...core.map((f) => ({ value: f.name, label: fieldLabel(f) })),
    ...custom.map((f) => ({ value: f.name, label: fieldLabel(f) })),
  ];
  const operatorOptions: SelectOption[] = (chosen?.operators ?? []).map(
    (candidate) => ({
      value: candidate,
      label: t(operatorKey(candidate as FilterOp, chosen?.type ?? "text")),
    }),
  );

  // Every edit below rewrites the leaf IN PLACE, keeping its id.
  //
  // That is not cosmetic. React keys a clause row on its node's id, so minting a
  // fresh leaf per keystroke remounts the row and the caret goes with it — a
  // human typing "gold" would get "g" and lose focus. Spreading the node that
  // was found keeps the row alive through its own value changing.
  const edit = (change: (found: Leaf) => Leaf) =>
    onChange(
      replaceNode(tree, leafID, (found) =>
        isGroup(found) ? found : change(found),
      ) ?? tree,
    );

  const retype = (nextField: string) => {
    const next = fields.find((f) => f.name === nextField);
    // The operator may not survive the move — a date has no `contains` — so it
    // falls back to the new field's first admitted one rather than staying and
    // being refused.
    const keptOp = next?.operators.includes(op)
      ? op
      : ((next?.operators[0] ?? "eq") as FilterOp);
    edit((found) => ({
      ...found,
      field: nextField,
      op: keptOp as FilterOp,
      value: emptyValueFor(keptOp as FilterOp),
    }));
  };

  return (
    <div className="filter-clause">
      <Select
        options={fieldOptions}
        value={field}
        onChange={retype}
        aria-label={t("filters.field")}
        placeholder={t("filters.choosePlaceholder")}
      />
      {chosen?.custom && (
        <Badge tone="accent">{t("filters.customBadge")}</Badge>
      )}
      <Select
        options={operatorOptions}
        value={op}
        onChange={(nextOp) =>
          edit((found) => ({
            ...found,
            op: nextOp as FilterOp,
            value: emptyValueFor(nextOp as FilterOp),
          }))
        }
        aria-label={t("filters.operator")}
      />
      <ValueControl
        type={chosen?.type ?? "text"}
        references={chosen?.references}
        options={chosen?.options}
        op={op}
        value={value}
        onChange={(nextValue) =>
          edit((found) => ({ ...found, value: nextValue }))
        }
      />
      <Button
        variant="ghost"
        small
        iconOnly
        aria-label={t("filters.removeClause", {
          field: fieldLabel(
            chosen ?? {
              name: field,
              type: "text",
              operators: [],
              custom: false,
            },
          ),
        })}
        onClick={() => onChange(removeNode(tree, leafID))}
      >
        ×
      </Button>
    </div>
  );
}

/**
 * The value half of a clause, chosen by operator first and field type second.
 *
 * Operator first because `in` takes a list whatever the field's type is, and
 * `exists` takes no value a human types at all — it is answered by the operator
 * they picked. Only the scalar comparisons defer to the type.
 */
function ValueControl({
  type,
  references,
  options,
  op,
  value,
  onChange,
}: Readonly<{
  type: VocabularyField["type"];
  /** What an id field's values point at, when the vocabulary named one. */
  references: Reference | undefined;
  /** A picklist's allowed values, when the vocabulary carried them. */
  options: readonly string[] | undefined;
  op: FilterOp;
  value: LeafValue;
  onChange: (next: LeafValue) => void;
}>) {
  const t = useT();
  if (op === "exists") {
    // `exists` carries its own boolean, and the two readings ARE the question
    // rather than an operand: "has a value" or "is empty".
    return (
      <BooleanValue
        value={value}
        onChange={onChange}
        labels={{ true: t("filters.hasValue"), false: t("filters.isEmpty") }}
        label={t("filters.existsLabel")}
      />
    );
  }
  if (op === "in") {
    return (
      <TokenInput
        values={Array.isArray(value) ? value.map(String) : []}
        onChange={(next) => onChange(next)}
        aria-label={t("filters.values")}
        placeholder={t("filters.addValue")}
      />
    );
  }
  if (boundedReference(references)) {
    return (
      <RecordValue
        reference={references}
        type={type}
        value={value}
        onChange={onChange}
      />
    );
  }
  if (options !== undefined && options.length > 0) {
    // A closed set is picked, not typed. Typing it invites the failure this whole
    // surface exists to prevent: a value outside the set compiles, matches
    // nothing, and reads as a settled answer rather than as a mistake.
    //
    // The label IS the value. These are the wire words a workspace admin chose,
    // or a contract enum; inventing display copy here would be a second
    // vocabulary for a reader to reconcile against what the record page shows.
    return (
      <Select
        options={options.map((option) => ({ value: option, label: option }))}
        value={typeof value === "string" ? value : ""}
        onChange={onChange}
        placeholder={t("filters.pickValue")}
        aria-label={t("filters.value")}
      />
    );
  }
  return <ScalarValue type={type} value={value} onChange={onChange} />;
}

/**
 * An id comparison as the RECORD it names, not its uuid.
 *
 * Only for a target the options module can enumerate. An organization reference
 * falls through to the plain box, because a workspace's accounts are as many as
 * its customers and a dropdown cannot hold them — the async picker that case
 * needs is its own change, and a half-filled list would be worse than a box.
 *
 * A stored id whose record is gone still shows: Select renders its placeholder
 * for a value matching no option, so the clause reads as needing attention
 * rather than silently appearing empty.
 */
function RecordValue({
  reference,
  type,
  value,
  onChange,
}: Readonly<{
  reference: Reference | undefined;
  type: VocabularyField["type"];
  value: LeafValue;
  onChange: (next: LeafValue) => void;
}>) {
  const t = useT();
  const noteId = useId();
  const { options, loading, failed, partial } = useReferenceOptions(reference);
  // A read that failed falls back to the plain box rather than to an empty
  // dropdown. An empty list would tell the reader this workspace has no such
  // records — a confident answer to a question that never got one — and would
  // leave them unable to write the clause at all.
  if (failed) {
    return <ScalarValue type={type} value={value} onChange={onChange} />;
  }
  // The caveat stacks UNDER the picker inside the clause's own value column
  // rather than becoming another item in the clause row: as a row item it would
  // sit between this control and the button that removes the clause, and it is
  // where the row wraps on a narrow viewport.
  return (
    <div className="filter-value">
      <Select
        options={options}
        value={typeof value === "string" ? value : ""}
        onChange={onChange}
        disabled={loading}
        placeholder={
          loading ? t("filters.loadingRecords") : t("filters.pickRecord")
        }
        aria-label={t("filters.value")}
        aria-describedby={partial ? noteId : undefined}
      />
      {/* A clause written against a list that stopped short of the workspace is
          a clause about the wrong set, and nothing else on this row would say
          so. */}
      <RosterPartialNote partial={partial} id={noteId} />
    </div>
  );
}

/** The two-way choice that `exists` and a boolean field both need. */
function BooleanValue({
  value,
  onChange,
  labels,
  label,
}: Readonly<{
  value: LeafValue;
  onChange: (next: LeafValue) => void;
  labels: Record<"true" | "false", string>;
  label: string;
}>) {
  return (
    <SegmentedControl
      options={["true", "false"] as const}
      value={value === false ? "false" : "true"}
      onChange={(next) => onChange(next === "true")}
      labels={labels}
      label={label}
    />
  );
}

/**
 * One scalar operand, drawn as its field's type.
 *
 * A date gets the date control because the engine's date operand and that
 * control's value are the same YYYY-MM-DD string, so a value round-trips with no
 * parse step for anyone to get wrong.
 */
function ScalarValue({
  type,
  value,
  onChange,
}: Readonly<{
  type: VocabularyField["type"];
  value: LeafValue;
  onChange: (next: LeafValue) => void;
}>) {
  const t = useT();
  if (type === "date") {
    const iso = typeof value === "string" ? value : "";
    return (
      <DateInput
        value={iso as "" | `${number}-${number}-${number}`}
        onChange={(event) => onChange(event.target.value)}
        aria-label={t("filters.value")}
      />
    );
  }
  if (type === "boolean") {
    return (
      <BooleanValue
        value={value}
        onChange={onChange}
        labels={{ true: t("filters.yes"), false: t("filters.no") }}
        label={t("filters.value")}
      />
    );
  }
  const numeric = type === "number" || type === "currency";
  return (
    <input
      className="input"
      value={
        typeof value === "string" || typeof value === "number"
          ? String(value)
          : ""
      }
      onChange={(event) =>
        onChange(
          numeric ? numberOrText(event.target.value) : event.target.value,
        )
      }
      aria-label={t("filters.value")}
      inputMode={numeric ? "decimal" : undefined}
    />
  );
}

/**
 * A numeric field's operand as a number when it is one, and as the raw text
 * otherwise.
 *
 * Half-typed input is the reason: "-" and "1." are neither valid numbers nor
 * mistakes, and coercing them to 0 or NaN would either move the count or refuse a
 * clause somebody is still typing. Leaving the text alone lets `isComplete` hold
 * the preview until the value is a number the engine will accept.
 */
function numberOrText(raw: string): LeafValue {
  if (raw === "") {
    return "";
  }
  const parsed = Number(raw);
  return Number.isFinite(parsed) ? parsed : raw;
}
