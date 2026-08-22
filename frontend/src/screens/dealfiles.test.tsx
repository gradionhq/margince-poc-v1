/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { DealFiles } from "./dealfiles";

// The deal's Files area as a rep meets it: a captured file says which message
// it came with and offers Hide, an upload offers Delete, and a hide lands on
// the deal's own hide route rather than touching the file.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type DealDocument = components["schemas"]["DealDocument"];

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

function upload(): DealDocument {
  return {
    hidden: false,
    attachment: {
      id: "att-up",
      entity_type: "deal",
      entity_id: "deal-1",
      filename: "pricing.pdf",
      category: "offer",
      source: "upload",
      captured_by: "human:u1",
      created_at: "2026-08-20T09:00:00Z",
    },
  } as DealDocument;
}

function captured(hidden = false): DealDocument {
  return {
    hidden,
    attachment: {
      id: "att-mail",
      entity_type: "activity",
      entity_id: "act-1",
      filename: "MSA-redline.docx",
      category: "email_attachment",
      source: "gmail",
      captured_by: "human:u1",
      created_at: "2026-08-21T09:00:00Z",
    },
    origin: {
      activity_id: "act-1",
      kind: "email",
      subject: "Re: MSA",
      occurred_at: "2026-08-21T08:55:00Z",
      counterparty_email: "laura@buyer.example",
    },
  } as DealDocument;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function me() {
  return {
    user: { id: "u1" },
    authorization: {
      seat_type: "full",
      objects: {
        deal: { create: true, read: true, update: true, delete: true },
      },
    },
  };
}

function stubApi(docs: DealDocument[]): { calls: Request[] } {
  const calls: Request[] = [];
  vi.stubGlobal("fetch", (input: Request) => {
    if (input.method !== "GET") {
      calls.push(input.clone());
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    const path = new URL(input.url).pathname;
    if (path.endsWith("/me")) {
      return Promise.resolve(jsonResponse(me()));
    }
    return Promise.resolve(jsonResponse({ data: docs, page: {} }));
  });
  return { calls };
}

it("tells a captured file from an upload and says where it came from", async () => {
  stubApi([upload(), captured()]);
  render(<DealFiles dealId="deal-1" />);

  expect(await screen.findByText("MSA-redline.docx")).toBeInTheDocument();
  expect(
    screen.getByText(/Attachment of a message from laura@buyer.example/),
  ).toBeInTheDocument();
  expect(screen.getByText(/Uploaded/)).toBeInTheDocument();
});

it("hides a captured file through the deal's own hide route, never the file", async () => {
  const { calls } = stubApi([captured()]);
  const user = userEvent.setup();
  render(<DealFiles dealId="deal-1" />);

  await user.click(
    await screen.findByRole("button", { name: /Actions for MSA-redline/ }),
  );
  await user.click(screen.getByRole("button", { name: "Hide from this deal" }));
  // The confirm says what stays: the message, the activity, the library.
  expect(await screen.findByText(/stay on the activity/)).toBeInTheDocument();
  const confirm = screen.getByRole("dialog");
  await user.click(
    within(confirm).getByRole("button", { name: "Hide from this deal" }),
  );

  await waitFor(() => expect(calls).toHaveLength(1));
  expect(calls[0].method).toBe("PUT");
  expect(new URL(calls[0].url).pathname).toBe(
    "/v1/deals/deal-1/documents/att-mail/hide",
  );
});

it("offers Delete on an upload and no Hide", async () => {
  stubApi([upload()]);
  const user = userEvent.setup();
  render(<DealFiles dealId="deal-1" />);

  await user.click(
    await screen.findByRole("button", { name: /Actions for pricing/ }),
  );
  expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Hide from this deal" }),
  ).not.toBeInTheDocument();
});
