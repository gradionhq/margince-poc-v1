import type { Meta, StoryObj } from "@storybook/react-vite";
import { type ReactNode, useState } from "react";
import { Button } from "./atoms";
import type { ListChip } from "./listsurface";
import { ListTable } from "./listtable";

// The list surface every record screen renders into: header, controls, rows and
// footer as one block. The query dials are CONTROLLED and server-backed in the
// product, so each story owns the state a screen would hold and shows what the
// surface does with it. Presentation — which columns are shown, how tight the
// rows are, how wide a column is — is the surface's own business and needs no
// wiring here.

const meta: Meta = {
  title: "Design System/ListTable",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

type Company = {
  id: string;
  name: string;
  industry: string;
  size: string;
  owner: string;
  region: string;
  valueMinor: number;
};

const INDUSTRIES = ["Manufacturing", "Logistics", "Healthcare", "SaaS"];
const REGIONS = ["DACH", "Nordics", "Benelux"];

function companies(count: number): Company[] {
  return Array.from({ length: count }, (_, index) => ({
    id: `c${index + 1}`,
    name: `Company ${String(index + 1).padStart(2, "0")}`,
    industry: INDUSTRIES[index % INDUSTRIES.length] ?? "SaaS",
    size: index % 3 === 0 ? "201-500" : "11-50",
    owner: index % 2 === 0 ? "Lars" : "Mia",
    region: REGIONS[index % REGIONS.length] ?? "DACH",
    valueMinor: (index + 1) * 12_500,
  }));
}

const columns = [
  {
    key: "name",
    header: "Company",
    // fixed keeps it out of the column picker and pins it left once the table
    // scrolls sideways.
    fixed: true,
    sort: "display_name",
    cell: (row: Company) => <strong>{row.name}</strong>,
  },
  { key: "industry", header: "Industry", cell: (row: Company) => row.industry },
  { key: "size", header: "Size", cell: (row: Company) => row.size },
  { key: "owner", header: "Owner", cell: (row: Company) => row.owner },
  { key: "region", header: "Region", cell: (row: Company) => row.region },
  {
    key: "value",
    header: "Pipeline",
    numeric: true,
    sort: "amount_minor",
    cell: (row: Company) =>
      `€${(row.valueMinor / 100).toLocaleString("en-US")}`,
  },
];

const chips: readonly ListChip[] = [
  {
    key: "industry",
    label: "Industry",
    allLabel: "All industries",
    options: INDUSTRIES.map((value) => ({ value, label: value })),
  },
  {
    key: "owner",
    label: "Owner",
    allLabel: "Any owner",
    options: [
      { value: "Lars", label: "Lars" },
      { value: "Mia", label: "Mia" },
    ],
  },
];

/**
 * A screen's worth of state around the surface: the search term, the sort
 * string the server would receive, the chosen filters and the view. Filtering
 * happens here only so the stories show real rows change — in the product the
 * server answers all four.
 */
function Surface({
  rows,
  ...rest
}: Readonly<{
  rows: Company[];
  pending?: boolean;
  problem?: ReactNode;
  hasMore?: boolean;
  caption?: string;
  note?: string;
}>) {
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState("display_name");
  const [chosen, setChosen] = useState<Record<string, string>>({});
  const [view, setView] = useState(0);

  const needle = search.trim().toLowerCase();
  // Only the two attributes the chips offer, read by name rather than by
  // indexing the row with a widened key.
  const attribute = (row: Company, key: string) =>
    key === "industry" ? row.industry : key === "owner" ? row.owner : "";
  const shown = rows.filter(
    (row) =>
      (!needle || row.name.toLowerCase().includes(needle)) &&
      Object.entries(chosen).every(
        ([key, value]) => !value || attribute(row, key) === value,
      ) &&
      (view === 0 || row.region === "DACH"),
  );

  return (
    <ListTable<Company>
      rows={shown}
      columns={columns}
      rowKey={(row) => row.id}
      unit="companies"
      action={<Button small>New company</Button>}
      search={{ value: search, onChange: setSearch }}
      sort={{ value: sort, onChange: setSort }}
      chips={chips}
      chosen={chosen}
      onChipChange={(key, value) =>
        setChosen((prev) => ({ ...prev, [key]: value }))
      }
      archived={{ checked: false, onChange: () => undefined }}
      views={[{ label: "All" }, { label: "DACH" }]}
      activeView={view}
      onViewChange={setView}
      {...rest}
    />
  );
}

// Six columns over one page: sortable headers, filter chips behind the Filter
// button, the column picker, the density toggle and the range count.
export const Default: Story = {
  render: () => <Surface rows={companies(12)} />,
};

// More rows than a page holds, so the footer's pager has pages to walk. The
// count reads as a range, and the page resets whenever the set narrows.
export const Paged: Story = {
  render: () => <Surface rows={companies(60)} />,
};

// hasMore is what a keyset cursor reports: there is no total and no arbitrary
// page to jump to, so Next stays enabled on the last loaded page and fetches
// the next one instead of the pager inventing a page count.
export const MoreToFetch: Story = {
  render: () => <Surface rows={companies(30)} hasMore />,
};

// The first page is in flight. The header, the dials and the primary action
// belong to the screen rather than to the response, so they stay put and only
// the body reports that rows are coming.
export const Pending: Story = {
  render: () => <Surface rows={[]} pending />,
};

// The read failed. Same rule as pending: the surface stays, the body carries
// the reason and whatever retry the caller supplies.
export const Failed: Story = {
  render: () => (
    <Surface
      rows={[]}
      problem={
        <>
          <p>Couldn't load this view.</p>
          <Button small>Retry</Button>
        </>
      }
    />
  ),
};

// Nothing exists yet, which is not the same as nothing matching: this copy does
// not offer to clear filters, because there are none to clear. Search or filter
// the Default story down to nothing to see the other empty state.
export const Empty: Story = {
  render: () => <Surface rows={[]} />,
};

// A caption says what a list IS when it needs saying, and a note says why a
// dial is missing — over a read-only mirror the sort and filter dials are gone
// because the source refuses them.
export const CaptionAndNote: Story = {
  render: () => (
    <Surface
      rows={companies(6)}
      caption="Companies the workspace has captured, newest first."
      note="Sorting and filters read through the source system"
    />
  ),
};

// Narrow enough that six columns do not fit. The identity column stays pinned
// to the left edge and casts one continuous shade over the columns sliding
// under it; drag a header's trailing edge to resize a column, which widens the
// table rather than squeezing its neighbours. Under 720px the rows become
// cards instead — resize the preview to see that.
export const PinnedWhileScrolling: Story = {
  render: () => (
    <div style={{ maxWidth: 720 }}>
      <Surface rows={companies(8)} />
    </div>
  ),
};
