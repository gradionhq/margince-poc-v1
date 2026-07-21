/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  canManageCustomFields,
  isConsentNotGranted,
  problemExistingId,
  provenanceOf,
  throwProblem,
} from "./common";
import { CreateAction } from "./create";

// Dedupe "view existing record" foundation (P-16): a create that collides on
// a duplicate_email/duplicate_domain gets its RFC-7807 body preserved
// (ProblemError) instead of collapsed to a string, so the form can surface a
// link straight to the record it collided with.

afterEach(() => {
  cleanup();
  window.location.hash = "";
});

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("problemExistingId", () => {
  it("extracts existing_id + code from a duplicate problem", () => {
    expect(
      problemExistingId({
        code: "duplicate_email",
        details: { existing_id: "01ABC" },
      }),
    ).toEqual({ id: "01ABC", code: "duplicate_email" });
  });

  it("returns null when there is no existing_id", () => {
    expect(
      problemExistingId({ code: "duplicate_email", details: {} }),
    ).toBeNull();
    expect(problemExistingId({ title: "nope" })).toBeNull();
    expect(problemExistingId(null)).toBeNull();
  });
});

describe("CreateAction dedupe link", () => {
  it("renders a view-existing link on a duplicate ProblemError and navigates on click", async () => {
    render(
      <CreateAction
        label="New contact"
        fields={[
          { key: "full_name", label: "create.fullName", required: true },
        ]}
        create={() =>
          throwProblem({
            code: "duplicate_email",
            details: { existing_id: "01ABC" },
          })
        }
        invalidate="people"
        screen="contacts"
        resolveExisting={(_code, id) => ({ screen: "contacts", id })}
      />,
    );
    await userEvent.click(screen.getByText("New contact"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Peter Neu");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() =>
      expect(screen.getByText("View existing record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByText("View existing record"));
    await waitFor(() => expect(window.location.hash).toBe("#/contacts/01ABC"));
  });
});

describe("isConsentNotGranted", () => {
  it("detects the consent gate 409 code", () => {
    expect(isConsentNotGranted({ code: "consent_not_granted" })).toBe(true);
    expect(isConsentNotGranted({ code: "version_skew" })).toBe(false);
    expect(isConsentNotGranted(null)).toBe(false);
    expect(isConsentNotGranted("nope")).toBe(false);
  });
});

describe("canManageCustomFields", () => {
  it("admits admin and ops, refuses everyone else", () => {
    expect(canManageCustomFields(["admin"])).toBe(true);
    expect(canManageCustomFields(["ops"])).toBe(true);
    expect(canManageCustomFields(["manager"])).toBe(false);
    expect(canManageCustomFields(["rep"])).toBe(false);
    expect(canManageCustomFields([])).toBe(false);
    expect(canManageCustomFields(undefined)).toBe(false);
  });
});

describe("provenanceOf", () => {
  it("maps captured_by to a kind without doubling the prefix", () => {
    // An agent id keeps the bare name — never the old "agent: agent:<id>".
    expect(provenanceOf("agent:capture")).toEqual({
      kind: "agent",
      agent: "capture",
    });
    // A connector reads as a connector, not an agent.
    expect(provenanceOf("connector:gmail")).toEqual({
      kind: "connector",
      connector: "gmail",
    });
    // Human (and absent) provenance.
    expect(provenanceOf("human:abc")).toEqual({ kind: "human" });
    expect(provenanceOf(undefined)).toEqual({ kind: "human" });
    // A bare token with no kind prefix falls back to an agent label.
    expect(provenanceOf("capture")).toEqual({
      kind: "agent",
      agent: "capture",
    });
  });
});
