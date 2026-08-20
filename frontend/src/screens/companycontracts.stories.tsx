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
  // All four: Add needs `create`, Edit needs `update`, Archive needs `delete`,
  // and the list itself needs `read`. A story holding fewer draws a card with
  // verbs missing and is named for none of it.
  contract: ["read", "create", "update", "delete"],
} as const;

// One filed document, named by its file — which is what the row shows, and the
// only field the chip needs beyond its id and its size.
function paper(id: string, filename: string) {
  return {
    id,
    filename,
    entity_type: "organization",
    entity_id: "o-1",
    contract_id: "c-1",
    byte_size: 184_320,
    source: "upload",
    captured_by: "human:u-1",
    created_at: "2026-10-21T09:00:00Z",
  };
}

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

/** More filed paper than one page of the documents endpoint holds. The chips
 * the read reached are still offered, and what did not fit is counted under
 * them: a row that showed the first page alone would read as the whole file. */
export const PaperBeyondOnePage: Story = {
  render: () => {
    // The route map keys on the path and not the query, so the pages come out
    // in the order the row asks for them.
    let asked = 0;
    installFetchStub({
      "GET /organizations/o-1/contracts": () =>
        jsonResponse({ data: contracts, page }),
      "GET /me": meRoute({ ...FULL_GRANTS }),
      "GET /organizations/o-1/documents": () => {
        asked += 1;
        return asked === 1
          ? jsonResponse({
              data: [
                paper("a-1", "SM-2026-014.pdf"),
                paper("a-2", "SM-2026-014-nachtrag-1.pdf"),
              ],
              page: { has_more: true, next_cursor: "page-2" },
            })
          : jsonResponse({
              data: [paper("a-3", "x.pdf"), paper("a-4", "y.pdf")],
              page: { has_more: false, next_cursor: null },
            });
      },
    });
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
