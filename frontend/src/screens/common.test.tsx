/** @vitest-environment jsdom */
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
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider, translate } from "../i18n";
import {
  isConsentNotGranted,
  logUnexpectedError,
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
  // Unconditionally, not at the end of the case that installed a spy: a case
  // that fails its assertion never reaches its own teardown, and a leaked
  // console spy silences the rest of the file — which looks exactly like a
  // suite that passes.
  vi.restoreAllMocks();
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

  // The sentinel string is what an object-RBAC denial and a read-share denial
  // both arrive with, and it names neither the authority the reader holds nor
  // what would widen it. Catalog copy replaces it.
  it("translates a permission_denied refusal instead of echoing the sentinel", () => {
    const message = problemMessage(
      { code: "permission_denied", detail: "permission denied" },
      t,
    );
    expect(message).toBe(t("common.permissionDenied"));
    expect(message).not.toBe("permission denied");
  });

  // One code, two authorities, and no wire field separating them: whatever the
  // server put in `detail`, the reader gets the copy that is true for both. A
  // branch that fell through to the detail for one of them would print a
  // sentence written for the other refusal.
  it("answers both denials with the same copy, whatever the server detailed", () => {
    const objectDenial = problemMessage(
      { code: "permission_denied", detail: "permission denied" },
      t,
    );
    const rowDenial = problemMessage(
      { code: "permission_denied", detail: "person:update denied" },
      t,
    );
    expect(rowDenial).toBe(objectDenial);
  });

  // The gate's own wrapping, which is the other real producer of this code. Its
  // detail names the admission spec and the resolver state — the authority
  // model's internals, not a sentence for a reader — so the copy stands in for
  // this one too, and none of it reaches the screen.
  it("stands in for the admission gate's detail, and shows none of it", () => {
    const message = problemMessage(
      {
        code: "permission_denied",
        detail:
          "gate: deal.advance: no authority resolver composed: permission denied",
      },
      t,
    );
    expect(message).toBe(t("common.permissionDenied"));
    expect(message).not.toContain("resolver");
  });

  // The wrapped form of the same sentinel, which `auth.Require` sends for every
  // object-RBAC denial. The prefix is the RBAC object and verb: internals no
  // client may be shown, and no more use to a reader than the sentinel trailing
  // them — so this is the same nothing, spelled longer.
  it("stands in for a wrapped sentinel too, and never shows the object and verb", () => {
    const message = problemMessage(
      { code: "permission_denied", detail: "person.update: permission denied" },
      t,
    );
    expect(message).toBe(t("common.permissionDenied"));
    expect(message).not.toContain("person.update");
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
        code: "permission_denied",
        detail: "permission denied",
      }),
    ).toBe("permission denied");
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
    // A background job reads as the system, not as an agent: the four kinds the
    // contract enumerates are four different answers to "who do I ask", and
    // routing the unrecognised ones into the agent arm made a scheduled sweep
    // announce itself as an AI.
    expect(provenanceOf("system:person_auto_enrich")).toEqual({
      kind: "system",
      job: "person_auto_enrich",
    });
    // A kind this app cannot read names no actor rather than the wrong one.
    expect(provenanceOf("capture")).toEqual({ kind: "unknown" });
  });

  it("says a job ran without naming one when the wire names none", () => {
    // The privacy-retention sweep stamps a bare `system`, so the kind is all
    // there is; the tag still has to say it was the system and not a person.
    expect(provenanceOf("system")).toEqual({ kind: "system", job: undefined });
  });

  it("never carries a passport uuid into an agent tag", () => {
    // A passport call stamps `agent:<passport_id>`, and there is no name behind
    // it on this side — the design system holds no record lookups. So the kind
    // travels and the id does not: the tag renders "Automated by an agent"
    // rather than "Automated by 0192abcd-…".
    expect(provenanceOf("agent:0192abcd-1111-4111-8111-111111111111")).toEqual({
      kind: "agent",
      agent: undefined,
    });
    // Upper case is the same id. Nothing mints it that way today, and a matcher
    // that only reads one casing would let the other through as a label.
    expect(provenanceOf("agent:0192ABCD-1111-4111-8111-111111111111")).toEqual({
      kind: "agent",
      agent: undefined,
    });
    // A named tool is not opaque and keeps its name: this is a rule about ids
    // that name nothing, not a refusal to attribute agents at all.
    expect(provenanceOf("agent:document-extractor")).toEqual({
      kind: "agent",
      agent: "document-extractor",
    });
  });

  it("names the connector, and never the member's uuid, whatever the id's shape", () => {
    // `connector:<system>:<user-uuid>`: the system is the connector, the uuid
    // is the member whose grant it ran under and belongs in no tag.
    expect(
      provenanceOf("connector:gmail:11111111-1111-4111-8111-111111111111"),
    ).toEqual({ kind: "connector", connector: "gmail" });
    // `connector:ext:<unit>[:<user-uuid>]`: the UNIT is the connector. Parsed
    // with a 2-limit split this read the literal word "ext" — identically for
    // every unit, so dispact-connector and zalo-oa were indistinguishable.
    expect(
      provenanceOf(
        "connector:ext:dispact-connector:11111111-1111-4111-8111-111111111111",
      ),
    ).toEqual({ kind: "connector", connector: "dispact-connector" });
    expect(provenanceOf("connector:ext:zalo-oa")).toEqual({
      kind: "connector",
      connector: "zalo-oa",
    });
    // Nothing after the kind at all: the kind is still the honest label, which
    // is what the old parse fell back to as well.
    expect(provenanceOf("connector")).toEqual({
      kind: "connector",
      connector: "connector",
    });
    // The extension marker with no unit behind it names nothing that ran, so it
    // falls back to the kind rather than labelling the tag with a segment of the
    // grammar. Nothing mints this shape; it costs one fallback to not print it.
    expect(provenanceOf("connector:ext")).toEqual({
      kind: "connector",
      connector: "connector",
    });
  });

  it("keeps the whole remainder for a human id, and for a named agent", () => {
    // An extra segment used to be discarded silently, which for a human id is a
    // partial match against the reader's own — the one comparison that must be
    // exact. It is safe to keep whole because it is compared and never printed.
    expect(provenanceOf("human:abc:stale-tail", "abc")).toEqual({
      kind: "human",
      self: false,
      userId: "abc:stale-tail",
    });
    // An agent id that is a name all the way through is a name a reader can act
    // on, however many segments it has.
    expect(provenanceOf("agent:ext:notes:summarise")).toEqual({
      kind: "agent",
      agent: "ext:notes:summarise",
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

  it("answers a problem body carrying no reader text with the shared line", () => {
    // A proxy 502, or a refusal the server sent no body with, reaches the
    // client as a ProblemError all the same. "request failed" is the message
    // that error carries into a stack trace — a developer's words, in one
    // language, and not a sentence anybody wrote for a reader.
    for (const body of [undefined, {}, "<html>502 Bad Gateway</html>", 42]) {
      expect(problemMessageOf(new ProblemError(body), t)).toBe(
        t("common.errorNoCause"),
      );
    }
  });

  it("keeps the server's words whenever the body carries any", () => {
    // The line above must not become a way to lose a real detail: a body with
    // a detail, or with only a title, still speaks for itself. A blank detail
    // is not words — it would render an error state with nothing in it — so
    // the title behind it is what the reader gets.
    expect(
      problemMessageOf(new ProblemError({ title: "Seat expired" }), t),
    ).toBe("Seat expired");
    expect(
      problemMessageOf(
        new ProblemError({ detail: "  ", title: "Seat expired" }),
        t,
      ),
    ).toBe("Seat expired");
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

// The other half of that rule. Deciding a failure is not fit to show is only
// honest if the failure still exists somewhere an operator can read.
describe("logUnexpectedError", () => {
  it("keeps a failure the reader is only shown generic copy for", () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    const bug = new TypeError("Cannot read properties of undefined");

    logUnexpectedError(bug);

    // The value itself, once: a message string would drop the stack that is
    // the only reason anyone opens the console for this.
    expect(logged).toHaveBeenCalledTimes(1);
    expect(logged).toHaveBeenCalledWith(bug);
  });

  it("stays silent for a failure the reader can already read", () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    logUnexpectedError(new ProblemError({ detail: "no seat" }));

    expect(logged).not.toHaveBeenCalled();
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

  // Every query-backed card in the product renders its failure through here, so
  // a failure that reaches nobody who cannot see the screen reaches nobody on
  // most of the product at once.
  it("announces a failed load, headline and cause in one region", () => {
    render(
      <QueryStates query={failing(new ProblemError({ detail: "no seat" }))}>
        {null}
      </QueryStates>,
    );
    const announced = screen.getByRole("alert");
    // Both, in the one region: a reader who hears only "Couldn't load this
    // view" has been told less than the screen says.
    expect(announced.textContent).toContain(t("common.error"));
    expect(announced.textContent).toContain("no seat");
    // Retry is a control to reach, not a sentence to hear.
    expect(
      within(announced).queryByRole("button", { name: t("common.retry") }),
    ).toBeNull();
    expect(
      screen.getByRole("button", { name: t("common.retry") }),
    ).toBeTruthy();
  });

  it("reports a load in progress as busy, since the shimmer bars say nothing", () => {
    render(
      <QueryStates
        query={{
          isPending: true,
          isError: false,
          error: null,
          refetch: () => undefined,
        }}
      >
        {null}
      </QueryStates>,
    );
    const loading = screen.getByRole("status");
    expect(loading.getAttribute("aria-busy")).toBe("true");
    // And nothing claims to have failed while it is still loading.
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
