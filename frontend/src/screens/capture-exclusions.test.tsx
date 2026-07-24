/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { CaptureExclusionsCard } from "./capture-exclusions";
import { installFetchStub, jsonResponse } from "./story-utils";

// The privacy control for RC-2's personal-mail exclusions: previously live
// (mail syncing, its own capture.skipped event) but with no reachable UI.
// A matching message produces zero CRM rows — nothing captured then
// hidden, simply never captured.

function render(ui: ReactNode) {
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
});

describe("CaptureExclusionsCard", () => {
  it("lists rules with plain-language kinds", async () => {
    installFetchStub({
      "GET /capture/exclusions": () =>
        jsonResponse({
          data: [
            {
              id: "e1",
              kind: "sender_domain",
              value: "family.example",
              created_at: "2026-07-01T00:00:00Z",
            },
          ],
        }),
    });
    render(<CaptureExclusionsCard />);
    expect(await screen.findByText(/mail from this domain/i)).toBeTruthy();
    expect(await screen.findByText("family.example")).toBeTruthy();
  });

  it("renders recipient_domain and label rules in plain language too", async () => {
    installFetchStub({
      "GET /capture/exclusions": () =>
        jsonResponse({
          data: [
            { id: "e1", kind: "recipient_domain", value: "vendor.example" },
            { id: "e2", kind: "label", value: "Personal" },
          ],
        }),
    });
    render(<CaptureExclusionsCard />);
    expect(await screen.findByText(/mail to this domain/i)).toBeTruthy();
    expect(await screen.findByText(/mail with this label/i)).toBeTruthy();
  });

  it("shows an empty state when no rules exist", async () => {
    installFetchStub({
      "GET /capture/exclusions": () => jsonResponse({ data: [] }),
    });
    render(<CaptureExclusionsCard />);
    expect(await screen.findByText(/no exclusion rules/i)).toBeTruthy();
  });

  it("prevents a duplicate client-side — the server Create is idempotent, not a 422", async () => {
    // exclusions.go:78-104 uses ON CONFLICT DO UPDATE RETURNING: a duplicate is
    // a 2xx returning the existing row, never an error. So the UI must disable
    // the confirm for a rule already in the loaded list rather than expect a 422.
    installFetchStub({
      "GET /capture/exclusions": () =>
        jsonResponse({
          data: [
            {
              id: "e1",
              kind: "sender_domain",
              value: "family.example",
              created_at: "2026-07-01T00:00:00Z",
            },
          ],
        }),
    });
    render(<CaptureExclusionsCard />);

    // Open the create modal (kind defaults to sender_domain already).
    await userEvent.click(await screen.findByRole("button", { name: /new/i }));
    // Type the SAME (kind, value) pair the loaded list already has.
    await userEvent.type(
      await screen.findByRole("textbox", { name: /value/i }),
      "family.example",
    );

    const submit = (await screen.findByRole("button", {
      name: /^add$/i,
    })) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    expect(await screen.findByText(/already/i)).toBeTruthy();
  });

  it("leaves the confirm enabled for a value not already in the list", async () => {
    installFetchStub({
      "GET /capture/exclusions": () =>
        jsonResponse({
          data: [{ id: "e1", kind: "sender_domain", value: "family.example" }],
        }),
    });
    render(<CaptureExclusionsCard />);

    await userEvent.click(await screen.findByRole("button", { name: /new/i }));
    await userEvent.type(
      await screen.findByRole("textbox", { name: /value/i }),
      "other.example",
    );

    const submit = (await screen.findByRole("button", {
      name: /^add$/i,
    })) as HTMLButtonElement;
    expect(submit.disabled).toBe(false);
  });

  it("confirms before removing a rule", async () => {
    installFetchStub({
      "GET /capture/exclusions": () =>
        jsonResponse({
          data: [{ id: "e1", kind: "label", value: "Personal" }],
        }),
    });
    render(<CaptureExclusionsCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /remove/i }),
    );
    expect(await screen.findByText(/stop being excluded/i)).toBeTruthy();
  });
});
