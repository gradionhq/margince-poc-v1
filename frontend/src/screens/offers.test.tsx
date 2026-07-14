/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { OfferScreen } from "./offers";

// Task 2.3: the offer 360 skeleton — header (offer_number/revision/status/
// back-to-deal), read-only server-truth totals, and a draft-only header edit
// affordance (absent from the DOM entirely once the offer leaves draft, not
// merely disabled — AC honesty rule).

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.location.hash = "";
});

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const result = rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
  return { ...result, client };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const baseOffer = {
  id: "o-1",
  workspace_id: "w",
  deal_id: "d-1",
  offer_number: "ANG-2026-0007",
  revision: 2,
  status: "draft" as const,
  currency: "EUR",
  buyer_org_id: null,
  valid_until: "2026-08-01",
  intro_text: null,
  terms_text: null,
  net_minor: 100000,
  tax_minor: 19000,
  gross_minor: 119000,
  template_id: null,
  line_items: [],
  source: "manual",
  captured_by: "human:u1",
  version: 3,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

function stubOffer(offer: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.includes("/offers/")) {
        return jsonResponse(offer);
      }
      return jsonResponse({ data: [], page: { has_more: false } });
    }),
  );
}

const existingLine = {
  id: "li-1",
  position: 1,
  product_id: null,
  description: "Consulting hours",
  unit: "hour",
  quantity: 10,
  unit_price_minor: 10000,
  discount_pct: 0,
  tax_rate: 19,
  line_net_minor: 100000,
  line_tax_minor: 19000,
  line_total_minor: 119000,
  evidence: null,
  price_grounded: true,
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

// A method-aware backend: GETs read the offer fixture, mutations
// (POST/PATCH/DELETE against .../line-items...) are served from the
// caller-supplied response and recorded so a test can assert on the exact
// request the editor sent.
function stubOfferWithMutations(
  offer: Record<string, unknown>,
  mutationResponse: Record<string, unknown>,
  mutations: { method: string; url: string; body: unknown }[],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = String(request ? request.url : input);
      const method = request ? request.method : (init?.method ?? "GET");
      if (
        url.includes("/line-items") &&
        (method === "POST" || method === "PATCH" || method === "DELETE")
      ) {
        const body =
          method === "DELETE"
            ? null
            : request
              ? await request.json()
              : JSON.parse(String(init?.body));
        mutations.push({ method, url, body });
        return jsonResponse(mutationResponse, method === "POST" ? 201 : 200);
      }
      if (url.includes("/offers/")) {
        return jsonResponse(offer);
      }
      return jsonResponse({ data: [], page: { has_more: false } });
    }),
  );
}

// A method-aware backend for the lifecycle actions (send/accept/reject):
// GETs read the offer fixture, a POST against .../send|accept|reject is
// served from the caller-supplied response (success or an RFC-7807 problem)
// and recorded so a test can assert on the exact request + headers sent.
function stubOfferWithLifecycle(
  offer: Record<string, unknown>,
  action: "send" | "accept" | "reject",
  response: { body: Record<string, unknown>; status: number },
  calls: { url: string; body: unknown; ifMatch: string | null }[],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = String(request ? request.url : input);
      const method = request ? request.method : (init?.method ?? "GET");
      if (url.includes(`/offers/o-1/${action}`) && method === "POST") {
        const rawBody = request ? await request.text() : (init?.body ?? null);
        const body = rawBody ? JSON.parse(String(rawBody)) : null;
        const headers = request ? request.headers : new Headers(init?.headers);
        calls.push({ url, body, ifMatch: headers.get("If-Match") });
        return jsonResponse(response.body, response.status);
      }
      if (url.includes("/offers/")) {
        return jsonResponse(offer);
      }
      return jsonResponse({ data: [], page: { has_more: false } });
    }),
  );
}

// A method-aware backend for the header edit modal: GETs read the offer
// fixture, a PATCH against .../offers/o-1 is served from the caller-supplied
// response and recorded so a test can assert on the exact request sent.
function stubOfferWithHeaderPatch(
  offer: Record<string, unknown>,
  response: { body: Record<string, unknown>; status: number },
  calls: { url: string; body: unknown; ifMatch: string | null }[],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = String(request ? request.url : input);
      const method = request ? request.method : (init?.method ?? "GET");
      if (url.endsWith("/offers/o-1") && method === "PATCH") {
        const rawBody = request ? await request.text() : (init?.body ?? null);
        const body = rawBody ? JSON.parse(String(rawBody)) : null;
        const headers = request ? request.headers : new Headers(init?.headers);
        calls.push({ url, body, ifMatch: headers.get("If-Match") });
        return jsonResponse(response.body, response.status);
      }
      if (url.includes("/offers/")) {
        return jsonResponse(offer);
      }
      return jsonResponse({ data: [], page: { has_more: false } });
    }),
  );
}

describe("OfferScreen", () => {
  it("renders the header, status badge, and read-only totals", async () => {
    stubOffer(baseOffer);
    render(<OfferScreen id="o-1" />);
    expect(await screen.findByText("ANG-2026-0007")).toBeTruthy();
    expect(screen.getByText("Revision 2")).toBeTruthy();
    expect(screen.getByText("draft")).toBeTruthy();
    // 100000 minor EUR net, 19000 tax, 119000 gross (en-GB Intl formatting).
    expect(screen.getByText("€1,000.00")).toBeTruthy();
    expect(screen.getByText("€190.00")).toBeTruthy();
    expect(screen.getByText("€1,190.00")).toBeTruthy();
  });

  it("shows the draft-only edit affordance when the offer is a draft", async () => {
    stubOffer(baseOffer);
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.getByTestId("edit-offer-header")).toBeTruthy();
  });

  it("omits the edit affordance entirely once the offer is sent", async () => {
    stubOffer({ ...baseOffer, status: "sent" });
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.queryByTestId("edit-offer-header")).toBeNull();
  });
});

describe("OfferLineEditor (OP-7/OP-13)", () => {
  it("mounts the line editor only while the offer is a draft", async () => {
    stubOffer({ ...baseOffer, line_items: [existingLine] });
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.getByTestId("offer-line-editor")).toBeTruthy();
  });

  it("omits the line editor entirely once the offer leaves draft", async () => {
    stubOffer({
      ...baseOffer,
      status: "sent",
      line_items: [existingLine],
    });
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.queryByTestId("offer-line-editor")).toBeNull();
  });

  it("refreshes totals from the add-line response, never a client-computed sum", async () => {
    // The naive client-side sum of the existing line's gross (119000) plus a
    // plausible new line would land somewhere near 119000 + a few thousand.
    // The stub deliberately returns a wildly different gross_minor so the
    // test only passes if the UI is reading the mutation's own response
    // rather than deriving a total from local arithmetic.
    const updatedOffer = {
      ...baseOffer,
      line_items: [
        existingLine,
        {
          ...existingLine,
          id: "li-2",
          position: 2,
          description: "Onboarding",
          quantity: 1,
          unit_price_minor: 5000,
          line_net_minor: 5000,
          line_tax_minor: 950,
          line_total_minor: 5950,
          version: 1,
        },
      ],
      net_minor: 999999,
      tax_minor: 111,
      gross_minor: 1000110,
    };
    const mutations: { method: string; url: string; body: unknown }[] = [];
    stubOfferWithMutations(
      { ...baseOffer, line_items: [existingLine] },
      updatedOffer,
      mutations,
    );
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    await userEvent.type(
      screen.getByTestId("new-line-description"),
      "Onboarding",
    );
    await userEvent.type(screen.getByTestId("new-line-quantity"), "1");
    // A controlled MoneyInput reformats to "0.00" after every keystroke, so
    // typing char-by-char fights its own re-render; set the final value in
    // one go instead (same convention tasks.test.tsx uses for date inputs).
    fireEvent.change(screen.getByTestId("new-line-unit-price"), {
      target: { value: "50.00" },
    });
    await userEvent.click(screen.getByTestId("add-line"));

    await waitFor(() => expect(mutations).toHaveLength(1));
    expect(mutations[0].method).toBe("POST");
    expect(mutations[0].url).toContain("/offers/o-1/line-items");
    expect(mutations[0].body).toMatchObject({
      description: "Onboarding",
      quantity: 1,
      unit_price_minor: 5000,
    });

    // €10,001.10 is the stub's own gross_minor (1000110), formatted — not a
    // value the client could reach by summing 1190.00 + 50.00.
    expect(await screen.findByText("€10,001.10")).toBeTruthy();
    expect(screen.queryByText("€1,240.00")).toBeNull();
  });

  it("removes a line using the delete response's recomputed totals", async () => {
    const afterRemove = {
      ...baseOffer,
      line_items: [],
      net_minor: 0,
      tax_minor: 0,
      gross_minor: 0,
    };
    const mutations: { method: string; url: string; body: unknown }[] = [];
    stubOfferWithMutations(
      { ...baseOffer, line_items: [existingLine] },
      afterRemove,
      mutations,
    );
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    await userEvent.click(screen.getByTestId("remove-line-li-1"));

    await waitFor(() => expect(mutations).toHaveLength(1));
    expect(mutations[0].method).toBe("DELETE");
    expect(mutations[0].url).toContain("/offers/o-1/line-items/li-1");
    expect((await screen.findAllByText("€0.00")).length).toBe(3);
  });

  it("edits a line on blur and refreshes totals from the PATCH response", async () => {
    const afterEdit = {
      ...baseOffer,
      line_items: [{ ...existingLine, quantity: 20, version: 2 }],
      net_minor: 200000,
      tax_minor: 38000,
      gross_minor: 238000,
    };
    const mutations: { method: string; url: string; body: unknown }[] = [];
    stubOfferWithMutations(
      { ...baseOffer, line_items: [existingLine] },
      afterEdit,
      mutations,
    );
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    const qtyInput = screen.getByTestId("line-quantity-li-1");
    await userEvent.clear(qtyInput);
    await userEvent.type(qtyInput, "20");
    qtyInput.blur();

    await waitFor(() => expect(mutations).toHaveLength(1));
    expect(mutations[0].method).toBe("PATCH");
    expect(mutations[0].url).toContain("/offers/o-1/line-items/li-1");
    expect(mutations[0].body).toMatchObject({ quantity: 20 });

    expect(await screen.findByText("€2,380.00")).toBeTruthy();
  });

  it("renders an unpriced (ungrounded) line as a placeholder, never €0.00 as a real price", async () => {
    const unpriced = {
      ...existingLine,
      id: "li-3",
      unit_price_minor: 0,
      line_net_minor: 0,
      line_tax_minor: 0,
      line_total_minor: 0,
      price_grounded: false,
    };
    stubOffer({ ...baseOffer, line_items: [unpriced] });
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.getAllByText("unpriced — excluded from total").length).toBe(
      2,
    );
    expect(screen.queryByText("€0.00")).toBeNull();
  });
});

// A method-aware backend for the regenerate action (Task 4.1, OP-11): GETs
// read the offer fixture, a POST against .../regenerate is served from the
// caller-supplied response (success or an RFC-7807 problem) and recorded so
// a test can assert on the exact request sent.
function stubOfferWithRegenerate(
  offer: Record<string, unknown>,
  response: { body: Record<string, unknown>; status: number },
  calls: { url: string }[],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = String(request ? request.url : input);
      const method = request ? request.method : (init?.method ?? "GET");
      if (url.includes("/offers/o-1/regenerate") && method === "POST") {
        calls.push({ url });
        return jsonResponse(response.body, response.status);
      }
      if (url.includes("/offers/")) {
        return jsonResponse(offer);
      }
      return jsonResponse({ data: [], page: { has_more: false } });
    }),
  );
}

describe("AI disclosure/diff banner (OP-11)", () => {
  it("renders the Art. 50 disclosure and diff summary when ai_generated is true", async () => {
    stubOffer({
      ...baseOffer,
      status: "sent",
      ai_generated: true,
      ai_disclosure: "This offer revision was drafted with AI assistance.",
      diff_from_previous: {
        added: [{ ...existingLine, id: "li-added", description: "Onboarding" }],
        removed: [
          { ...existingLine, id: "li-removed", description: "Legacy setup" },
        ],
        changed: [
          {
            before: { ...existingLine, description: "Consulting hours" },
            after: {
              ...existingLine,
              description: "Consulting hours (revised)",
            },
          },
        ],
      },
    });
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    expect(
      screen.getByText("This offer revision was drafted with AI assistance."),
    ).toBeTruthy();
    expect(screen.getByText("1 line(s) added")).toBeTruthy();
    expect(screen.getByText("1 line(s) removed")).toBeTruthy();
    expect(screen.getByText("1 line(s) changed")).toBeTruthy();
    expect(screen.getByText("Onboarding")).toBeTruthy();
    expect(screen.getByText("Legacy setup")).toBeTruthy();
    expect(screen.getByText("Consulting hours (revised)")).toBeTruthy();
  });

  it("omits the disclosure banner entirely when ai_generated is false (mechanical regenerate)", async () => {
    stubOffer({
      ...baseOffer,
      status: "sent",
      ai_generated: false,
      ai_disclosure: null,
      diff_from_previous: null,
    });
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.queryByTestId("ai-disclosure-banner")).toBeNull();
  });
});

describe("regenerate action (OP-11)", () => {
  it("regenerates a sent offer, seeds the new draft's cache, and navigates to it", async () => {
    const calls: { url: string }[] = [];
    const newDraft = {
      ...baseOffer,
      id: "o-2",
      revision: 3,
      status: "draft",
      ai_generated: true,
      ai_disclosure: "This offer revision was drafted with AI assistance.",
      diff_from_previous: { added: [], removed: [], changed: [] },
    };
    stubOfferWithRegenerate(
      { ...baseOffer, status: "sent" },
      { body: newDraft, status: 201 },
      calls,
    );
    const { client } = render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    await userEvent.click(screen.getByTestId("regenerate-offer"));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain("/offers/o-1/regenerate");
    expect(client.getQueryData(["offer", "o-2"])).toEqual(newDraft);
    expect(globalThis.location.hash).toBe("#/offers/o-2");
    // The new draft is a new offer row on the same deal — the deal's
    // offers panel (deals.tsx) must be told to refetch, same as
    // AcceptOfferAction does after its own new-row-shaped change.
    expect(invalidateSpy).toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: ["deal-offers", newDraft.deal_id] }),
    );
  });

  it("renders a 422 detail verbatim when regenerate is rejected (e.g. a stale sent offer)", async () => {
    const calls: { url: string }[] = [];
    stubOfferWithRegenerate(
      { ...baseOffer, status: "sent" },
      {
        body: {
          title: "Unprocessable",
          detail: "offer is not in sent status",
          code: "offer_not_sent",
        },
        status: 422,
      },
      calls,
    );
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    await userEvent.click(screen.getByTestId("regenerate-offer"));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(await screen.findByText("offer is not in sent status")).toBeTruthy();
  });
});

// A method-aware backend for the render action (Task 4.2, OP-12): GETs read
// the offer fixture, a POST against .../render is served from the
// caller-supplied response (success, an honest 501, or an RFC-7807 problem)
// and recorded so a test can assert on the exact request sent.
function stubOfferWithRender(
  offer: Record<string, unknown>,
  response: { body: Record<string, unknown>; status: number },
  calls: { url: string }[],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = String(request ? request.url : input);
      const method = request ? request.method : (init?.method ?? "GET");
      if (url.includes("/offers/o-1/render") && method === "POST") {
        calls.push({ url });
        return jsonResponse(response.body, response.status);
      }
      if (url.includes("/offers/")) {
        return jsonResponse(offer);
      }
      return jsonResponse({ data: [], page: { has_more: false } });
    }),
  );
}

describe("render PDF action (OP-12)", () => {
  it("renders the PDF and shows a download link from the 200 response", async () => {
    const calls: { url: string }[] = [];
    const rendered = {
      ...baseOffer,
      // An opaque blobstore key (offer_render.go's actual shape), never a
      // browsable URL — the link's href is derived from offer.id instead.
      pdf_asset_ref: "ws-1/offers/o-1/1/render-uuid.pdf",
    };
    stubOfferWithRender(baseOffer, { body: rendered, status: 200 }, calls);
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    await userEvent.click(screen.getByTestId("render-pdf"));

    await waitFor(() => expect(calls).toHaveLength(1));
    const link = await screen.findByTestId("pdf-link");
    // The link targets the downloadOfferPdf endpoint keyed off the offer's
    // own id — pdf_asset_ref is an opaque blobstore key, never a browsable
    // URL, so its presence only gates whether the link renders at all.
    expect(link.getAttribute("href")).toBe(
      `${globalThis.location.origin}/v1/offers/o-1/pdf`,
    );
    expect(screen.queryByTestId("pdf-unavailable")).toBeNull();
  });

  it("shows an honest inline state on a 501 (no blobstore wired), never the generic error path", async () => {
    const calls: { url: string }[] = [];
    stubOfferWithRender(
      baseOffer,
      {
        body: { title: "Not Implemented", detail: "blobstore not wired" },
        status: 501,
      },
      calls,
    );
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    await userEvent.click(screen.getByTestId("render-pdf"));

    await waitFor(() => expect(calls).toHaveLength(1));
    const unavailable = await screen.findByTestId("pdf-unavailable");
    expect(unavailable).toBeTruthy();
    // Calm, informational copy — not the red error-banner path every other
    // action's mutation.isError branch renders.
    expect(unavailable.style.color).not.toBe("var(--danger)");
    expect(screen.queryByText("blobstore not wired")).toBeNull();
    expect(screen.queryByTestId("pdf-link")).toBeNull();
  });

  it("renders a 422 detail verbatim when render fails validation", async () => {
    const calls: { url: string }[] = [];
    stubOfferWithRender(
      baseOffer,
      {
        body: {
          title: "Unprocessable",
          detail: "offer has no line items to render",
          code: "offer_empty",
        },
        status: 422,
      },
      calls,
    );
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    await userEvent.click(screen.getByTestId("render-pdf"));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(
      await screen.findByText("offer has no line items to render"),
    ).toBeTruthy();
    expect(screen.queryByTestId("pdf-unavailable")).toBeNull();
  });
});

describe("offer lifecycle actions (OP-8/OP-9/OP-10)", () => {
  it("shows send only while draft, and accept/reject only once sent", async () => {
    stubOffer(baseOffer);
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.getByTestId("send-offer")).toBeTruthy();
    expect(screen.queryByTestId("accept-offer")).toBeNull();
    expect(screen.queryByTestId("reject-offer")).toBeNull();
  });

  it("omits send and shows accept/reject once the offer is sent", async () => {
    stubOffer({ ...baseOffer, status: "sent" });
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.queryByTestId("send-offer")).toBeNull();
    expect(screen.getByTestId("accept-offer")).toBeTruthy();
    expect(screen.getByTestId("reject-offer")).toBeTruthy();
  });

  it("omits every lifecycle action once the offer is accepted or rejected", async () => {
    stubOffer({ ...baseOffer, status: "accepted" });
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    expect(screen.queryByTestId("send-offer")).toBeNull();
    expect(screen.queryByTestId("accept-offer")).toBeNull();
    expect(screen.queryByTestId("reject-offer")).toBeNull();
  });

  it("sends the offer with the current version as If-Match after confirming", async () => {
    const calls: { url: string; body: unknown; ifMatch: string | null }[] = [];
    stubOfferWithLifecycle(
      baseOffer,
      "send",
      {
        body: { ...baseOffer, status: "sent" },
        status: 200,
      },
      calls,
    );
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    await userEvent.click(screen.getByTestId("send-offer"));
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain("/offers/o-1/send");
    expect(calls[0].ifMatch).toBe("3");
    expect(await screen.findByText("sent")).toBeTruthy();
    // Once sent, the send action and the draft-only affordances disappear.
    expect(screen.queryByTestId("send-offer")).toBeNull();
    expect(screen.queryByTestId("edit-offer-header")).toBeNull();
    expect(screen.queryByTestId("offer-line-editor")).toBeNull();
  });

  it("renders a 422 detail verbatim when send is rejected (e.g. fx_rate_unavailable)", async () => {
    const calls: { url: string; body: unknown; ifMatch: string | null }[] = [];
    stubOfferWithLifecycle(
      baseOffer,
      "send",
      {
        body: {
          title: "Unprocessable",
          detail: "no FX rate available for this currency pair",
          code: "fx_rate_unavailable",
        },
        status: 422,
      },
      calls,
    );
    render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");

    await userEvent.click(screen.getByTestId("send-offer"));
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(
      await screen.findByText("no FX rate available for this currency pair"),
    ).toBeTruthy();
  });

  it("accepts the offer and invalidates the deal + deal-offers queries so the deal screen resyncs", async () => {
    const calls: { url: string; body: unknown; ifMatch: string | null }[] = [];
    stubOfferWithLifecycle(
      { ...baseOffer, status: "sent" },
      "accept",
      { body: { ...baseOffer, status: "accepted" }, status: 200 },
      calls,
    );
    const { client } = render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    await userEvent.click(screen.getByTestId("accept-offer"));
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain("/offers/o-1/accept");
    expect(calls[0].ifMatch).toBe("3");
    expect(await screen.findByText("accepted")).toBeTruthy();
    expect(invalidateSpy).toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: ["deal", "d-1"] }),
    );
    expect(invalidateSpy).toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: ["deal-offers", "d-1"] }),
    );
  });

  it("rejects the offer with an optional reason and never touches the deal queries", async () => {
    const calls: { url: string; body: unknown; ifMatch: string | null }[] = [];
    stubOfferWithLifecycle(
      { ...baseOffer, status: "sent" },
      "reject",
      { body: { ...baseOffer, status: "rejected" }, status: 200 },
      calls,
    );
    const { client } = render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    await userEvent.click(screen.getByTestId("reject-offer"));
    await userEvent.type(screen.getByTestId("reject-reason"), "budget cut");
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain("/offers/o-1/reject");
    expect(calls[0].body).toMatchObject({ reason: "budget cut" });
    expect(await screen.findByText("rejected")).toBeTruthy();
    expect(invalidateSpy).not.toHaveBeenCalled();
  });
});

describe("edit offer header modal", () => {
  it("lets the user change the currency and applies the response directly (no refetch)", async () => {
    const calls: { url: string; body: unknown; ifMatch: string | null }[] = [];
    stubOfferWithHeaderPatch(
      baseOffer,
      { body: { ...baseOffer, currency: "USD", version: 4 }, status: 200 },
      calls,
    );
    const { client } = render(<OfferScreen id="o-1" />);
    await screen.findByText("ANG-2026-0007");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    await userEvent.click(screen.getByTestId("edit-offer-header"));
    await userEvent.selectOptions(screen.getByLabelText("Currency"), "USD");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].ifMatch).toBe("3");
    expect(calls[0].body).toMatchObject({ currency: "USD" });
    // 100000 minor net formatted in the new currency confirms the mutation
    // response was applied via setQueryData, not a follow-up GET.
    expect(await screen.findByText("US$1,000.00")).toBeTruthy();
    expect(invalidateSpy).not.toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: ["offer", "o-1"] }),
    );
  });
});
