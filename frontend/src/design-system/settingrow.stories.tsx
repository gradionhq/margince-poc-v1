// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { Button, Card, DataTable, Disclosure, TextInput } from "./atoms";
import { ChoiceList } from "./choicelist";
import { Select } from "./select";
import { SettingList, SettingRow } from "./settingrow";
import { Switch } from "./switch";

type RefusedDomain = Readonly<{ domain: string; by: string }>;

const REFUSED: RefusedDomain[] = [
  { domain: "gmail.com", by: "Capture sink" },
  { domain: "t-online.de", by: "Marek Janetzke" },
];

const meta: Meta<typeof SettingRow> = {
  title: "Design System/SettingRow",
  component: SettingRow,
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj<typeof SettingRow>;

/**
 * The four shapes a settings card is built from, in one card, because the whole
 * point of the pair is that they line up: a reader auditing this page finds
 * every answer at the same x.
 */
function Catalog() {
  const [enrich, setEnrich] = useState(true);
  const [digest, setDigest] = useState(false);
  const [locale, setLocale] = useState("en");
  const [profile, setProfile] = useState("balanced");
  return (
    <Card title="Capture" sub="What happens to mail that arrives.">
      <SettingList>
        <SettingRow
          label="Auto-enrich captured companies"
          description="Looks a company up the first time it is captured, and fills what the mail did not carry."
          control={
            <Switch
              label="Auto-enrich captured companies"
              labelHidden
              checked={enrich}
              onChange={setEnrich}
            />
          }
        />
        <SettingRow
          label="Weekly digest"
          description="One mail on Monday with what moved."
          control={
            <Switch
              label="Weekly digest"
              labelHidden
              checked={digest}
              onChange={setDigest}
            />
          }
        />
        <SettingRow
          label="Language"
          description="The language this installation speaks to you in."
          control={(control) => (
            <Select
              {...control}
              className="settingrow-measure"
              options={[
                { value: "en", label: "English" },
                { value: "de", label: "Deutsch" },
              ]}
              value={locale}
              onChange={setLocale}
            />
          )}
        />
        <SettingRow
          label="Reply-to address"
          value="marek@gradion.com"
          description="Where a reply to a captured thread is sent."
          control={<Button variant="ghost">Edit</Button>}
        />
        <SettingRow
          label="Extraction profile"
          description="How much the model may infer from a thread it has not seen before."
          layout="stack"
          control={
            <ChoiceList
              legend="Extraction profile"
              hideLegend
              value={profile}
              onChange={setProfile}
              choices={[
                {
                  value: "strict",
                  label: "Strict",
                  description: "Only what the thread states outright.",
                },
                {
                  value: "balanced",
                  label: "Balanced",
                  description: "Infers a role or a company from context.",
                },
              ]}
            />
          }
        />
        <SettingRow
          label="Refused domains"
          description="Domains this installation would not turn into a company, and who decided."
          layout="stack"
          control={
            <DataTable
              label={"Refused domains"}
              columns={[
                {
                  key: "domain",
                  header: "Domain",
                  render: (row: RefusedDomain) => row.domain,
                },
                {
                  key: "by",
                  header: "Decided by",
                  render: (row: RefusedDomain) => row.by,
                },
              ]}
              rows={REFUSED}
              rowKey={(row) => row.domain}
            />
          }
        />
        <Disclosure summary="Advanced">
          <SettingList>
            <SettingRow
              label="Retry a refused capture"
              description="Runs the sink again over mail it dropped in the last 24 hours."
              control={<Button variant="ghost">Run…</Button>}
            />
            <SettingRow
              label="Sink concurrency"
              description="How many mailboxes the sink reads at once."
              control={(control) => (
                <TextInput
                  {...control}
                  className="settingrow-measure"
                  type="number"
                  defaultValue={4}
                />
              )}
            />
          </SettingList>
        </Disclosure>
      </SettingList>
    </Card>
  );
}

export const Catalogue: Story = { render: () => <Catalog /> };
