// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../app/mefixture";
import { ContractForm, SignedFileField } from "./contractform";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// Recording an agreement, and reaching the paper already filed against it.
//
// THE FOUR STATES OF THE SIGNED-DOCUMENT FIELD ARE THE POINT OF THIS FILE.
// A field that renders a bare drop zone says "there is no paper on file", and
// that sentence is only true in one of them. It shipped wrong once: an empty
// list stood in for a read that was still running, one that failed, and one a
// grant refused, so the field confidently reported an absence it had no way to
// know about. Each story below is one of those states, because that is the
// difference a screenshot can show and a passing test cannot.

const meta: Meta = {
  title: "Records/Company 360/Contract form",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const CONTRACT = {
  id: "c-1",
  organization_id: "o-1",
  title: "valantic GmbH — Rahmenvertrag",
  contract_number: "V-5253-VALA",
  value_minor: 17_740_000,
  currency: "EUR",
  value_basis: "annualized_12m",
  starts_on: "2025-10-23",
  ends_on: "2026-10-23",
  notice_period_days: 90,
  signed_on: "2025-10-09",
  status: "active",
  under_contract: true,
  auto_renew: false,
  source: "manual",
  captured_by: "human:u-1",
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const PAPER = {
  id: "a-9",
  filename: "V-5253-VALA.pdf",
  title: "valantic GmbH — Rahmenvertrag",
  category: "contract",
  doc_state: "current",
  pinned: false,
  created_at: "2026-01-02T09:00:00Z",
  entity_type: "organization",
  entity_id: "o-1",
  contract_id: "c-1",
  source: "upload",
  captured_by: "human:u-1",
};

// Every story shares the same seat; only the documents route differs, so the
// state on screen is attributable to the read and to nothing else.
const SESSION = () =>
  jsonResponse(
    meFixture({
      allow: {
        contract: ["read", "create", "update", "delete"],
        activity: ["read"],
      },
    }),
  );

const DOCUMENTS = "GET /organizations/o-1/documents";

// The FIELD alone, not the whole modal.
//
// The form is taller than the 1024x720 capture viewport, so a story of the
// full modal cuts off above the document field — the screenshot would be
// green and would show none of what these stories exist to show. Rendering
// the field on its own is what makes the state visible, which is the entire
// value of a story over a passing test.
function field() {
  return (
    <StoryProviders>
      <SignedFileField orgId="o-1" contractID="c-1" onPick={() => {}} />
    </StoryProviders>
  );
}

// READY — the paper is filed, named, and downloadable. The drop zone below it
// says "add another", because an agreement can carry an amendment beside its
// original.
export const WithFiledPaper: Story = {
  render: () => {
    installFetchStub({
      "GET /me": SESSION,
      [DOCUMENTS]: () => jsonResponse({ data: [PAPER] }),
    });
    return field();
  },
};

// EMPTY — the read came back and this agreement genuinely has no document. The
// only state in which "Drop a file here" is a true sentence.
export const NoPaperOnFile: Story = {
  render: () => {
    installFetchStub({
      "GET /me": SESSION,
      [DOCUMENTS]: () => jsonResponse({ data: [] }),
    });
    return field();
  },
};

// WITHHELD — reading documents carries its own grant. This reader does not
// hold it, so the contract may well have paper and the field must not imply
// otherwise. No retry is offered: the refusal would repeat identically.
export const DocumentsWithheld: Story = {
  render: () => {
    installFetchStub({
      "GET /me": SESSION,
      [DOCUMENTS]: () =>
        jsonResponse({ status: 403, code: "permission_denied" }, 403),
    });
    return field();
  },
};

// FAILED — the read broke rather than refused, so a retry is a real action and
// the field offers one.
export const DocumentsUnreadable: Story = {
  render: () => {
    installFetchStub({
      "GET /me": SESSION,
      [DOCUMENTS]: () => jsonResponse({ status: 500, code: "internal" }, 500),
    });
    return field();
  },
};

// LOADING — a promise that never settles, which is what the reader sees on a
// slow connection. It must not look like an answer.
export const DocumentsLoading: Story = {
  render: () => {
    installFetchStub({
      "GET /me": SESSION,
      [DOCUMENTS]: () => new Promise<Response>(() => {}),
    });
    return field();
  },
};

// A contract being CREATED has no id, so it asks for no documents at all and
// the field is honestly empty from the first frame.
export const CreatingANewAgreement: Story = {
  render: () => {
    installFetchStub({ "GET /me": SESSION });
    return (
      <StoryProviders>
        <SignedFileField orgId="o-1" onPick={() => {}} />
      </StoryProviders>
    );
  },
};

// The whole modal on an existing agreement, so the form's own layout has a
// story too. Its document field is below the capture fold — which is why the
// state stories above render the field on its own rather than relying on this.
export const TheWholeForm: Story = {
  render: () => {
    installFetchStub({
      "GET /me": SESSION,
      [DOCUMENTS]: () => jsonResponse({ data: [PAPER] }),
    });
    return (
      <StoryProviders>
        <ContractForm
          orgId="o-1"
          contract={CONTRACT as never}
          open
          onClose={() => {}}
        />
      </StoryProviders>
    );
  },
};
