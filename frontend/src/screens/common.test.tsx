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
import { LocaleProvider, translate } from "../i18n";
import {
  isConsentNotGranted,
  ProblemError,
  problemExistingId,
  problemFieldErrors,
  problemFieldErrorsOf,
  problemMessage,
  problemMessageOf,
  provenanceOf,
  QueryStates,
  throwProblem,
} from "./common";
import { CreateAction } from "./create";

const t = (key: Parameters<typeof translate>[1]) => translate("en", key);

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

describe("problemMessage", () => {
  it("translates an unsupported_by_sor WRITE refusal when given a translator", () => {
    expect(
      problemMessage(
        { code: "unsupported_by_sor", detail: "write not supported by SoR" },
        t,
      ),
    ).toBe(t("overlay.refused"));
  });

  it("translates an unsupported_in_overlay_mode READ refusal to its own, different copy", () => {
    const message = problemMessage(
      { code: "unsupported_in_overlay_mode", detail: "422 read gap" },
      t,
    );
    expect(message).toBe(t("overlay.filterUnsupported"));
    // The two refusal codes are different states (a refused write vs. a
    // refused filter/sort dial) — collapsing them onto one string would
    // print the write-specific "can't serve this write" for a filter a
    // caller never tried to write.
    expect(message).not.toBe(t("overlay.refused"));
  });

  it("keeps the server detail when no translator is given", () => {
    expect(
      problemMessage({
        code: "unsupported_by_sor",
        detail: "write not supported by SoR",
      }),
    ).toBe("write not supported by SoR");
    expect(
      problemMessage({
        code: "unsupported_in_overlay_mode",
        detail: "422 read gap",
      }),
    ).toBe("422 read gap");
  });

  it("keeps the server detail for an unrelated code even with a translator", () => {
    expect(
      problemMessage({ code: "version_skew", detail: "record changed" }, t),
    ).toBe("record changed");
  });
});

// The 422 shape httperr.Validation emits: the top-level code is always
// "validation_error", so the rule a caller keys on lives only here.
describe("problemFieldErrors", () => {
  it("reads the field, code, and message the server asserted", () => {
    expect(
      problemFieldErrors({
        code: "validation_error",
        detail: "reconnect your mailbox",
        details: {
          errors: [
            {
              field: "from",
              code: "mailbox_not_send_capable",
              message: "reconnect your mailbox",
            },
          ],
        },
      }),
    ).toEqual([
      {
        field: "from",
        code: "mailbox_not_send_capable",
        message: "reconnect your mailbox",
      },
    ]);
  });

  it("reads nothing out of a body that carries no field errors", () => {
    expect(problemFieldErrors({ code: "consent_not_granted" })).toEqual([]);
    expect(
      problemFieldErrors({
        code: "validation_error",
        details: { errors: "not a list" },
      }),
    ).toEqual([]);
    expect(problemFieldErrors(null)).toEqual([]);
    expect(problemFieldErrors("nope")).toEqual([]);
  });

  it("reads nothing out of a problem that is not a validation error", () => {
    // `details` is a free-form extension any problem may carry. A gateway or
    // dependency failure that happens to spell an `errors` array is not the
    // server asserting a rule about a submitted field, and reading it as one
    // would turn an unrelated fault into an actionable send refusal.
    expect(
      problemFieldErrors({
        code: "internal_error",
        details: {
          errors: [
            {
              field: "from",
              code: "mailbox_not_send_capable",
              message: "reconnect your mailbox",
            },
          ],
        },
      }),
    ).toEqual([]);
  });

  it("drops an entry that does not name a field, a code, and a message", () => {
    // A half-formed entry cannot be matched on, and filling its holes with
    // empty strings would let a caller key on a rule nobody asserted.
    expect(
      problemFieldErrors({
        code: "validation_error",
        details: {
          errors: [
            { field: "from", code: "mailbox_not_send_capable" },
            { code: "shared_unsubscribe_token", message: "one at a time" },
            null,
          ],
        },
      }),
    ).toEqual([]);
  });

  it("claims field errors only off a failure that carries a server problem", () => {
    const problem = {
      code: "validation_error",
      details: {
        errors: [{ field: "from", code: "not_send_capable", message: "fix" }],
      },
    };
    let thrown: unknown;
    try {
      throwProblem(problem);
    } catch (error) {
      thrown = error;
    }
    expect(problemFieldErrorsOf(thrown)).toEqual([
      { field: "from", code: "not_send_capable", message: "fix" },
    ]);
    expect(problemFieldErrorsOf(new Error("network down"))).toEqual([]);
    expect(problemFieldErrorsOf(problem)).toEqual([]);
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
    // A bare token with no kind prefix falls back to an agent label.
    expect(provenanceOf("capture")).toEqual({
      kind: "agent",
      agent: "capture",
    });
  });

  it("only calls a human 'you' when that human is the reader", () => {
    // "Typed by you" over a colleague's entry is a false statement about who
    // to ask, so the human branch carries the id and whether it is the
    // reader's — the tag decides the wording from that, not from the kind.
    expect(provenanceOf("human:abc", "abc")).toEqual({
      kind: "human",
      self: true,
      userId: "abc",
    });
    expect(provenanceOf("human:abc", "someone-else")).toEqual({
      kind: "human",
      self: false,
      userId: "abc",
    });
    // No session resolved yet: a caller that cannot say who is reading cannot
    // claim the reader typed it.
    expect(provenanceOf("human:abc")).toEqual({
      kind: "human",
      self: false,
      userId: "abc",
    });
  });

  it("reports an unrecorded source as unknown rather than as the reader's own typing", () => {
    // The old fallback made every unattributed row read as "typed by you" —
    // the one attribution nobody can check.
    expect(provenanceOf(undefined)).toEqual({ kind: "unknown" });
    expect(provenanceOf("")).toEqual({ kind: "unknown" });
  });
});

// The display side of the same rule: what a caught failure is allowed to say.
// A server problem states an honest cause the reader can act on; a rejected
// fetch or a bug in a handler states our internals in wording nobody wrote for
// a user, and the two are indistinguishable at the point of rendering unless
// the type is checked here.
describe("problemMessageOf", () => {
  it("shows the server's own detail when the failure carries a problem", () => {
    expect(
      problemMessageOf(new ProblemError({ detail: "email taken" }), t),
    ).toBe("email taken");
  });

  it("translates a refusal code the same way the raw-body reader does", () => {
    expect(
      problemMessageOf(new ProblemError({ code: "unsupported_by_sor" }), t),
    ).toBe(t("overlay.refused"));
  });

  it("never repeats the words of a bare Error", () => {
    const bug = new TypeError("Cannot read properties of undefined");
    expect(problemMessageOf(bug, t)).toBe(t("common.errorNoCause"));
    expect(problemMessageOf(bug, t)).not.toContain(bug.message);
  });

  it("never repeats the words of a thrown non-Error either", () => {
    for (const thrown of ["boom", { detail: "not a ProblemError" }, null]) {
      expect(problemMessageOf(thrown, t)).toBe(t("common.errorNoCause"));
    }
  });

  it("prefers the surface's own copy for a failure the server never described", () => {
    expect(
      problemMessageOf(
        new Error("Failed to fetch"),
        t,
        t("connectors.loadFailed"),
      ),
    ).toBe(t("connectors.loadFailed"));
    // A server problem still speaks for itself: the fallback is for the case
    // where there is nothing to say, not a way to overwrite the server.
    expect(
      problemMessageOf(
        new ProblemError({ detail: "budget exhausted" }),
        t,
        t("connectors.loadFailed"),
      ),
    ).toBe("budget exhausted");
  });
});

describe("QueryStates", () => {
  const failing = (error: unknown) => ({
    isPending: false,
    isError: true,
    error,
    refetch: () => undefined,
  });

  it("prints the server's detail for a failed query", () => {
    render(
      <QueryStates query={failing(new ProblemError({ detail: "no seat" }))}>
        {null}
      </QueryStates>,
    );
    expect(screen.getByText("no seat")).toBeTruthy();
  });

  it("prints the shared line, not the message, when the failure is not a problem", () => {
    render(
      <QueryStates query={failing(new TypeError("Failed to fetch"))}>
        {null}
      </QueryStates>,
    );
    expect(screen.getByText(t("common.errorNoCause"))).toBeTruthy();
    expect(screen.queryByText(/Failed to fetch/)).toBeNull();
  });
});
