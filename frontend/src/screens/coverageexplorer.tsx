import { useId, useMemo, useState } from "react";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  Modal,
  SearchField,
  Skeleton,
} from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { incompleteGraph } from "./company360";
import { type StrengthBucket, useOrganizationGraph } from "./connections";

// Comparing a chosen few colleagues against the account's contacts.
//
// The compact coverage on each contact row answers "who should make this call".
// This answers the other question — "where are we thin" — and it is the one that
// tempts a contact × every-colleague matrix. With a forty-person sales team that
// matrix is 40 columns wide and nobody reads it.
//
// So the reader CHOOSES the colleagues to compare, up to a handful, and the grid
// is that selection wide. Which is also the honest shape: comparing everyone is
// not a question anybody has, and a grid that answers it is a grid that answers
// nothing.
//
// Two things it will not do. It never shows message content — the cell says the
// band and the reader opens the contact for the rest, so the grid cannot become
// a way to read a colleague's mail. And a grid built from an incomplete graph
// says so, because "no connection" and "the read was capped before it got here"
// are different claims and only one of them means nobody has tried.

type Contact = components["schemas"]["Organization360Contact"];

// How many colleagues can stand in the grid at once. Beyond this the columns
// stop being scannable, which is the failure the whole surface exists to avoid.
const COLUMN_CAP = 8;

const BAND_LABELS: Record<StrengthBucket, MessageKey> = {
  strong: "co.routeIn.band.strong",
  moderate: "co.routeIn.band.some",
  weak: "co.routeIn.band.faint",
  none: "co.routeIn.band.unknown",
};

export function CoverageExplorer({
  orgId,
  contacts,
}: Readonly<{ orgId: string; contacts: readonly Contact[] }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const titleId = useId();
  return (
    <>
      <button
        type="button"
        className="link-button"
        onClick={() => setOpen(true)}
      >
        {t("acctCoverage.open")}
      </button>
      {open && (
        <Modal open onClose={() => setOpen(false)} labelledBy={titleId}>
          <h2 id={titleId} className="t-h2 modal-title">
            {t("acctCoverage.title")}
          </h2>
          <CoverageGrid orgId={orgId} contacts={contacts} />
        </Modal>
      )}
    </>
  );
}

// One colleague's edges to this account's contacts, keyed by person.
type ColleagueCoverage = {
  id: string;
  label: string;
  bands: Map<string, StrengthBucket>;
};

function CoverageGrid({
  orgId,
  contacts,
}: Readonly<{ orgId: string; contacts: readonly Contact[] }>) {
  const t = useT();
  // Read only when somebody opens the explorer: a graph query on every company
  // page load is what the on-demand route-in read already avoids.
  const query = useOrganizationGraph(orgId);
  const graph = Array.isArray(query.data?.nodes) ? query.data : undefined;
  const [contactFilter, setContactFilter] = useState("");
  const [selected, setSelected] = useState<readonly string[]>([]);

  const colleagues = useMemo(
    () => colleaguesFrom(graph, contacts),
    [graph, contacts],
  );

  if (query.isPending) {
    return <Skeleton width="100%" height={120} />;
  }
  // A failed read is unavailable, never "nobody is connected": the two call for
  // opposite next moves and only a read that succeeded can make the second claim.
  if (query.isError || !graph) {
    return <p className="co-restricted">{t("co.section.unavailable")}</p>;
  }
  if (colleagues.length === 0) {
    return (
      <EmptyState>
        {t(
          incompleteGraph(graph)
            ? "acctCoverage.noneButPartial"
            : "acctCoverage.noneAtAll",
        )}
      </EmptyState>
    );
  }

  const shown = selected.length > 0 ? selected : defaultSelection(colleagues);
  const rows = contacts.filter((contact) =>
    contact.full_name
      .toLowerCase()
      .includes(contactFilter.trim().toLowerCase()),
  );

  return (
    <div className="coverage-grid">
      <SearchField
        value={contactFilter}
        aria-label={t("acctCoverage.findContact")}
        placeholder={t("acctCoverage.findContact")}
        onChange={(event) => setContactFilter(event.target.value)}
      />
      {/* The columns are a choice, not a default view of everybody. Colleagues
          with no edge to this account are absent entirely rather than offered
          as empty columns. */}
      <div className="coverage-picker">
        {colleagues.map((colleague) => {
          const on = shown.includes(colleague.id);
          const full = shown.length >= COLUMN_CAP && !on;
          return (
            <Button
              key={colleague.id}
              small
              disabled={full}
              // aria-pressed, not a glyph: a toggle's state belongs in the
              // control's semantics, where a screen reader can hear it, rather
              // than in a character prepended to its label.
              aria-pressed={on}
              onClick={() =>
                setSelected(
                  on
                    ? shown.filter((id) => id !== colleague.id)
                    : [...shown, colleague.id],
                )
              }
            >
              {colleague.label}
            </Button>
          );
        })}
      </div>
      {shown.length >= COLUMN_CAP && (
        <p className="t-caption">
          {t("acctCoverage.columnCap", { cap: COLUMN_CAP })}
        </p>
      )}
      <table className="coverage-table">
        <thead>
          <tr>
            <th>{t("acctCoverage.contact")}</th>
            {shown.map((id) => (
              <th key={id}>
                {colleagues.find((colleague) => colleague.id === id)?.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((contact) => (
            <tr key={contact.person_id}>
              <th scope="row">{contact.full_name}</th>
              {shown.map((id) => {
                const colleague = colleagues.find((c) => c.id === id);
                const band = colleague?.bands.get(contact.person_id);
                return (
                  // The column header travels with the cell twice over: as
                  // data-label, which the narrow layout renders as visible
                  // text, and as an aria-label, which carries the same fact to
                  // a screen reader. The CSS turns rows into cards at 720px,
                  // and display:block strips the table roles a screen reader
                  // would otherwise navigate by — so each cell has to be
                  // self-describing rather than relying on a header row that
                  // no longer exists in the accessibility tree.
                  <td
                    key={id}
                    data-label={colleague?.label}
                    aria-label={colleague?.label}
                  >
                    {/* No band is UNTRIED, not zero. The cell says so in words
                        rather than leaving a blank a reader has to interpret. */}
                    {band ? (
                      <Badge tone={band === "strong" ? "success" : undefined}>
                        {t(BAND_LABELS[band])}
                      </Badge>
                    ) : (
                      <span className="t-caption">
                        {t("acctCoverage.untried")}
                      </span>
                    )}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && (
        <EmptyState>{t("acctCoverage.noMatch")}</EmptyState>
      )}
      {/* The graph caps its rings and withholds groups the caller may not read,
          so a grid built from it can be narrower than the account really is. A
          reader told "nobody covers this contact" would stop looking. */}
      {incompleteGraph(graph) && (
        <p className="t-caption">{t("acctCoverage.partial")}</p>
      )}
    </div>
  );
}

// colleaguesFrom inverts the graph's edges into one row per colleague.
//
// Only colleagues with at least one edge to a contact ON THIS ACCOUNT appear: an
// empty column is a name the reader has to rule out, and the default is to hide
// what has nothing to say.
function colleaguesFrom(
  graph: ReturnType<typeof useOrganizationGraph>["data"],
  contacts: readonly Contact[],
): ColleagueCoverage[] {
  if (!graph || !Array.isArray(graph.nodes)) {
    return [];
  }
  const onAccount = new Set(contacts.map((contact) => contact.person_id));
  const labels = new Map(graph.nodes.map((node) => [node.id, node.label]));
  const byColleague = new Map<string, ColleagueCoverage>();
  for (const edge of graph.edges) {
    if (edge.kind !== "in_contact_with" || !onAccount.has(edge.to)) {
      continue;
    }
    const label = labels.get(edge.from);
    // An edge naming a node the payload did not send is dropped rather than
    // shown as an identifier: the caps that trimmed the node list are the
    // reason, and a column the reader cannot put a name to is not a column.
    if (!label) {
      continue;
    }
    const entry = byColleague.get(edge.from) ?? {
      id: edge.from,
      label,
      bands: new Map<string, StrengthBucket>(),
    };
    if (edge.strength_bucket) {
      entry.bands.set(edge.to, edge.strength_bucket);
    }
    byColleague.set(edge.from, entry);
  }
  // Widest coverage first, then by name so equal colleagues come back in a
  // stable order rather than the payload's.
  return [...byColleague.values()].sort(
    (a, b) => b.bands.size - a.bands.size || a.label.localeCompare(b.label),
  );
}

// The grid opens on the colleagues who cover the most of this account, because
// an empty grid asks the reader to guess whom to compare before they have seen
// anything.
function defaultSelection(colleagues: readonly ColleagueCoverage[]): string[] {
  return colleagues.slice(0, Math.min(4, colleagues.length)).map((c) => c.id);
}
