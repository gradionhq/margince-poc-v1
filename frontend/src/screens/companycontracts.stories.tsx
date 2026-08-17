// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { CompanyContractsCard } from "./companycontracts";
import { ContractForm } from "./contractform";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The agreements on an account, and the form that records one.
//
// The form's signed-file field is a FileDropzone (design-system), the same
// control the Add a document dialog uses — the two used to be one screen-local
// copy whose stylesheet only the company page loaded.

const meta: Meta = {
  title: "Records/Company 360/Contracts",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Contract = components["schemas"]["Contract"];

const page = { has_more: false, next_cursor: null };

const contracts = [
  {
    id: "c-1",
    title: "Pallet pooling framework",
    contract_number: "SM-2026-014",
    status: "active",
    value_minor: 14850000,
    currency: "EUR",
    starts_on: "2026-11-01",
    ends_on: "2027-10-31",
    signed_on: "2026-10-20",
  } as unknown as Contract,
];

const FULL_GRANTS = {
  contract: ["read", "create", "update", "delete"],
} as const;

function routes(data: Contract[]) {
  installFetchStub({
    "GET /organizations/o-1/contracts": () => jsonResponse({ data, page }),
    "GET /me": meRoute({ ...FULL_GRANTS }),
  });
}

/** The card with one agreement on it. */
export const Populated: Story = {
  render: () => {
    routes(contracts);
    return (
      <StoryProviders>
        <div style={{ maxWidth: 720 }}>
          <CompanyContractsCard orgId="o-1" />
        </div>
      </StoryProviders>
    );
  },
};

/** No agreement on record — which is a fact about the account, not a failed
 * read, and the two must not look the same. */
export const Empty: Story = {
  render: () => {
    routes([]);
    return (
      <StoryProviders>
        <div style={{ maxWidth: 720 }}>
          <CompanyContractsCard orgId="o-1" />
        </div>
      </StoryProviders>
    );
  },
};

/** The form itself, open, so the signed-file dropzone is visible without
 * driving the card to reach it. */
export const FormOpen: Story = {
  render: () => {
    routes(contracts);
    return (
      <StoryProviders>
        <ContractForm orgId="o-1" open onClose={() => {}} />
      </StoryProviders>
    );
  },
};
