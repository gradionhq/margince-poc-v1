// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import type { components } from "../api/schema";
import { MAX_SELECTED_FACTS } from "./onboarding";
import { FactsCard, FactTable, useFactSelection } from "./onboarding-facts";
import { StoryProviders } from "./story-utils";

// The fact surface for the render gate (G-10). Both views are prop-driven, so a
// story is a fixture plus the state a wizard would own: the card over a spread
// that hits all four wire categories, the card with nothing read yet, the full
// table, and the table at the contract's 100-key ceiling — the one state where
// the rows stop accepting a press and have to say why.

type CompanySiteReadFact = components["schemas"]["CompanySiteReadFact"];

const SPREAD: CompanySiteReadFact[] = [
  {
    category: "company",
    field: "founded_year",
    value: "Founded 2011",
    value_key: "company:founded_year:2011",
    evidence_snippet: "Founded in Hamburg in 2011 by two forwarders.",
    evidence_url: "https://acme.test/about",
    confidence: 0.96,
  },
  {
    category: "company",
    field: "employee_range",
    value: "40-60 people",
    value_key: "company:employee_range:40-60",
    evidence_snippet: "A team of just over fifty across two offices.",
    evidence_url: "https://acme.test/about/team",
    confidence: 0.74,
  },
  {
    category: "offering",
    field: "service",
    value: "Managed Kubernetes",
    value_key: "offering:service:k8s",
    evidence_snippet: "We run and patch Kubernetes for logistics operators.",
    evidence_url: "https://acme.test/services/kubernetes",
    confidence: 0.91,
  },
  {
    category: "offering",
    field: "service",
    value: "24/7 support desk",
    value_key: "offering:service:support",
    evidence_snippet:
      "The desk answers around the clock, in German and English.",
    evidence_url: "https://acme.test/services/support",
    confidence: 0.83,
  },
  {
    category: "offering",
    field: "product",
    value: "Freight visibility portal",
    value_key: "offering:product:portal",
    evidence_snippet: "One portal for every shipment in flight.",
    evidence_url: "https://acme.test/products/portal",
    confidence: 0.69,
  },
  {
    category: "market",
    field: "served_industry",
    value: "Logistics",
    value_key: "market:served_industry:logistics",
    evidence_snippet: "Trusted by freight forwarders across the EU.",
    evidence_url: "https://acme.test/industries",
    confidence: 0.64,
  },
  {
    category: "market",
    field: "geography",
    value: "DACH and Benelux",
    value_key: "market:geography:dach-benelux",
    evidence_snippet: "Offices in Hamburg and Rotterdam.",
    evidence_url: "https://acme.test/contact",
    confidence: 0.58,
  },
  {
    category: "signal",
    field: "quantified_outcome",
    value: "Cut deploy time by 40%",
    value_key: "signal:quantified_outcome:deploys",
    evidence_snippet: "Deploys went from two hours to seventy minutes.",
    evidence_url: "https://acme.test/cases/freight",
    confidence: 0.36,
  },
  {
    category: "signal",
    field: "named_customer",
    value: "Nordwind Spedition",
    value_key: "signal:named_customer:nordwind",
    evidence_snippet: "Nordwind Spedition moved its fleet onto the portal.",
    evidence_url: "https://acme.test/cases/nordwind",
    confidence: 0.42,
  },
];

// More facts than the ceiling takes, so the cap story is a real state rather
// than a mocked flag.
const OVER_CAP: CompanySiteReadFact[] = Array.from(
  { length: 130 },
  (_, index) => ({
    category: (["company", "offering", "market", "signal"] as const)[index % 4],
    field: "capability",
    value: `Capability ${index + 1}`,
    value_key: `offering:capability:${index}`,
    evidence_snippet: `Listed on the capability page, item ${index + 1}.`,
    evidence_url: `https://acme.test/capabilities#c${index}`,
    confidence: 0.95 - (index % 20) / 25,
  }),
);

const AT_CAP = OVER_CAP.slice(0, MAX_SELECTED_FACTS).map(
  (item) => item.value_key,
);

function CardDemo({
  facts,
  initial = [],
}: Readonly<{ facts: CompanySiteReadFact[]; initial?: string[] }>) {
  const [keys, setKeys] = useState<readonly string[]>(initial);
  const selection = useFactSelection(facts, keys, setKeys);
  return <FactsCard facts={facts} selection={selection} locale="en" />;
}

function TableDemo({
  facts,
  initial = [],
}: Readonly<{ facts: CompanySiteReadFact[]; initial?: string[] }>) {
  const [keys, setKeys] = useState<readonly string[]>(initial);
  const selection = useFactSelection(facts, keys, setKeys);
  return (
    <FactTable
      facts={facts}
      selection={selection}
      locale="en"
      onClose={() => {}}
    />
  );
}

const meta: Meta<typeof FactsCard> = {
  title: "screens/onboarding-facts",
  component: FactsCard,
};
export default meta;
type Story = StoryObj<typeof FactsCard>;

export const CardFullSpread: Story = {
  render: () => (
    <StoryProviders>
      <CardDemo
        facts={SPREAD}
        initial={["company:founded_year:2011", "offering:service:k8s"]}
      />
    </StoryProviders>
  ),
};

export const CardEmpty: Story = {
  render: () => (
    <StoryProviders>
      <CardDemo facts={[]} />
    </StoryProviders>
  ),
};

export const TableOpen: Story = {
  render: () => (
    <StoryProviders>
      <TableDemo
        facts={SPREAD}
        initial={["market:served_industry:logistics"]}
      />
    </StoryProviders>
  ),
};

export const TableAtCap: Story = {
  render: () => (
    <StoryProviders>
      <TableDemo facts={OVER_CAP} initial={AT_CAP} />
    </StoryProviders>
  ),
};
