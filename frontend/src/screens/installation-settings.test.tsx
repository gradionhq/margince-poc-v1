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
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { InstallationSettingsCard } from "./installation-settings";

// Settings → Installation: the organization's name, reporting zone and base
// currency. Every role READS them as three rows showing what is set; only
// installation_settings:update opens the dialog that changes them, so the verb
// is refused with a reason (never hidden) for everyone else.
//
// The case worth proving beyond that is the base currency's lock: once deals
// have converted against it the server reports it frozen WITH a reason, and
// both the row and the field must carry that reason — otherwise an operator
// types a value they cannot save and learns why only from a 422.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const SETTINGS_EDITOR: GrantSpec = { installation_settings: ["update"] };

type BackendOptions = {
  locked?: boolean;
  lockedReason?: string;
  /** What the server refuses the PATCH with, as an RFC-7807 validation body. */
  refuse?: { field: string; code: string; message: string }[];
};

// backendFor answers /me with the given grants and /installation/settings with
// the given state, capturing any PATCH body so the wire shape is assertable.
function backendFor(allow: GrantSpec, opts: BackendOptions = {}) {
  let state = {
    name: "Brandt Automotive",
    timezone: "Europe/Berlin",
    base_currency: "EUR",
    base_currency_locked: opts.locked ?? false,
    ...(opts.lockedReason
      ? { base_currency_locked_reason: opts.lockedReason }
      : {}),
  };
  let capturedPatch: unknown = null;
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const req =
        input instanceof Request ? input : new Request(String(input), init);
      const url = new URL(req.url, "http://localhost");
      if (url.pathname.endsWith("/me")) {
        return jsonResponse(meFixture({ allow }));
      }
      if (url.pathname.endsWith("/installation/settings")) {
        if (req.method === "PATCH") {
          capturedPatch = await req.json();
          if (opts.refuse) {
            // The shape httperr.Validation actually emits: one top-level code
            // for every 422, and the rule that fired named per field.
            return jsonResponse(
              {
                code: "validation_error",
                detail: "The installation could not be saved.",
                details: { errors: opts.refuse },
              },
              422,
            );
          }
          state = { ...state, ...(capturedPatch as object) };
        }
        return jsonResponse(state);
      }
      throw new Error(`unexpected request: ${req.method} ${url.pathname}`);
    },
  );
  return { fetchMock, patch: () => capturedPatch };
}

function render(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider>{node}</LocaleProvider>
    </QueryClientProvider>,
  );
}

/**
 * Opens the profile dialog from one row's own Edit verb, and answers with the
 * dialog.
 *
 * Through the row rather than by rendering the dialog directly: the verb IS the
 * only way in, and a test that reached past it would prove nothing about
 * whether a reader can.
 */
async function openFrom(
  user: ReturnType<typeof userEvent.setup>,
  fact: RegExp,
): Promise<HTMLElement> {
  const edit = await screen.findByRole("button", { name: fact });
  await user.click(edit);
  return screen.getByRole("dialog");
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("InstallationSettingsCard", () => {
  it("shows the installation's values to a role that cannot change them, and refuses the change with a reason", async () => {
    const { fetchMock } = backendFor({});
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    // Read by every role: a rep reading amounts benefits from knowing which
    // currency they are in, and hiding the facts would buy nothing the server
    // does not already enforce.
    expect(await screen.findByText("Brandt Automotive")).toBeTruthy();
    expect(screen.getByText("Europe/Berlin")).toBeTruthy();
    expect(screen.getByText("EUR")).toBeTruthy();

    // The VERB is what a permission denies, and it says why through
    // aria-describedby — so the sentence reaches a reader who lands on the
    // refused control, not only one who happens past the paragraph.
    const reason = screen.getByText(
      "Only an admin or ops can change these settings.",
    );
    expect(reason.id).not.toBe("");
    for (const fact of [
      /edit organization name/i,
      /edit reporting timezone/i,
      /edit base currency/i,
    ]) {
      const edit = screen.getByRole("button", { name: fact });
      expect((edit as HTMLButtonElement).disabled).toBe(true);
      expect(edit.getAttribute("aria-describedby") ?? "").toContain(reason.id);
    }

    // No form to submit: it lives behind a verb nobody here may press.
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull();
  });

  it("sends only the fields that changed", async () => {
    const user = userEvent.setup();
    const { fetchMock, patch } = backendFor(SETTINGS_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    const dialog = await openFrom(user, /edit organization name/i);
    const name = within(dialog).getByLabelText(/organization name/i);
    await user.clear(name);
    await user.type(name, "Brandt Group");
    await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

    // The currency and zone were never touched, so they must not appear in the
    // patch: re-sending an unchanged base currency would ask the server to
    // write a value that may be frozen, for a field the operator never edited.
    await waitFor(() => expect(patch()).toEqual({ name: "Brandt Group" }));
  });

  // The row's Edit is beside a fact, so it has to land on that fact. Three
  // verbs opening one dialog that always focuses its first field would make
  // two of the three lie about what they lead to.
  it("focuses the field whose row was edited", async () => {
    const user = userEvent.setup();
    const { fetchMock } = backendFor(SETTINGS_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    const dialog = await openFrom(user, /edit reporting timezone/i);
    await waitFor(() =>
      expect(document.activeElement).toBe(
        within(dialog).getByLabelText(/reporting timezone/i),
      ),
    );
  });

  it("renders the base currency read-only with the server's reason once it is locked", async () => {
    const user = userEvent.setup();
    const reason =
      "3 deal(s) have already frozen a conversion rate against it, so changing the base would re-mean every roll-up built on them";
    const { fetchMock } = backendFor(SETTINGS_EDITOR, {
      locked: true,
      lockedReason: reason,
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    // The server's own sentence, not a generic "locked" — it names how much
    // history is at stake, which is what tells the operator why. On the ROW, so
    // it is read before the dialog is opened over a field nobody can type in.
    expect(await screen.findByText(reason)).not.toBeNull();

    const dialog = await openFrom(user, /edit base currency/i);
    const currency = within(dialog).getByLabelText(
      /base currency/i,
    ) as HTMLInputElement;
    expect(currency.disabled).toBe(true);
    // The same sentence inside the dialog, where the refused field is.
    expect(within(dialog).getByText(reason)).not.toBeNull();
    // The editor can still change everything else.
    const name = within(dialog).getByLabelText(
      /organization name/i,
    ) as HTMLInputElement;
    expect(name.disabled).toBe(false);
  });

  // Three facts, ONE record, one save. The server takes ONE sparse PATCH, so
  // the three fields are submitted together: a save per row would promise three
  // independent writes that do not exist. The card is therefore three ANSWERS
  // and one verb, and the form lives in the dialog that verb opens — which is
  // what keeps every row scannable and the commit next to the fields it writes.
  it("reads as three rows on the card and edits as one form with one save", async () => {
    const user = userEvent.setup();
    const { fetchMock, patch } = backendFor(SETTINGS_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    // "Installation", not "Organization": this surface sits under a nav group
    // heading that already reads Organization, and a card repeating its own
    // heading names nothing.
    const panel = (
      await screen.findByRole("heading", { name: /^installation$/i })
    ).closest("section");
    if (!panel) {
      throw new Error("the Installation heading is not inside a card");
    }
    // No input on the card itself: a row is an answer, and the three of them
    // line up in one column for a reader auditing the installation.
    expect(panel.querySelectorAll("input")).toHaveLength(0);
    for (const value of ["Brandt Automotive", "Europe/Berlin", "EUR"]) {
      expect(within(panel).getByText(value)).toBeTruthy();
    }

    const dialog = await openFrom(user, /edit organization name/i);
    expect(within(dialog).getByLabelText(/organization name/i)).toBeTruthy();
    expect(within(dialog).getByLabelText(/reporting timezone/i)).toBeTruthy();
    expect(within(dialog).getByLabelText(/base currency/i)).toBeTruthy();

    // The currency keeps a heading of its own, one level down: the lock rule is
    // its own subject and an unheaded field in a list of fields does not say so.
    const currency = within(dialog).getByRole("heading", {
      name: /^currency$/i,
    });
    expect(currency.tagName).toBe("H3");

    // One button, and it is on the surface that holds the fields.
    const saves = screen.getAllByRole("button", { name: /^save$/i });
    expect(saves).toHaveLength(1);
    expect(dialog.contains(saves[0])).toBe(true);

    const name = within(dialog).getByLabelText(/organization name/i);
    await user.clear(name);
    await user.type(name, "Brandt Group");
    const zone = within(dialog).getByLabelText(/reporting timezone/i);
    await user.clear(zone);
    await user.type(zone, "Europe/Vilnius");
    await user.click(saves[0]);

    await waitFor(() =>
      expect(patch()).toEqual({
        name: "Brandt Group",
        timezone: "Europe/Vilnius",
      }),
    );
    // The write landing closes the form and puts the new answers on the rows,
    // which is the only visible confirmation a sparse patch can give.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await screen.findByText("Brandt Group")).toBeTruthy();
  });

  // An editable field must LOOK editable. `.input` is what carries the border,
  // fill and padding that separate a control from the static text beside it, so
  // a field that renders without it reads as a caption: the value is there, the
  // affordance is not, and an operator concludes the setting is read-only. The
  // class is the observable proof the control came from the design system
  // rather than being hand-rolled again.
  it("renders every field as a design-system input", async () => {
    const user = userEvent.setup();
    const { fetchMock } = backendFor(SETTINGS_EDITOR);
    vi.stubGlobal("fetch", fetchMock);

    render(<InstallationSettingsCard />);

    const dialog = await openFrom(user, /edit organization name/i);
    for (const label of [
      /organization name/i,
      /reporting timezone/i,
      /base currency/i,
    ]) {
      const control = within(dialog).getByLabelText(label);
      expect(control.className.split(/\s+/)).toContain("input");
    }
  });

  it("puts a refused value's reason on the field it is about", async () => {
    // The defect: the mutation wrapped the server's answer in `new Error`, which
    // discarded `details.errors[]` — so a 422 naming the reporting zone arrived
    // as one paragraph under the base-currency field, and an operator had three
    // inputs and no way to tell which one the installation refused.
    const user = userEvent.setup();
    const { fetchMock } = backendFor(SETTINGS_EDITOR, {
      refuse: [
        {
          field: "timezone",
          code: "unknown_zone",
          message: "Not an IANA time zone.",
        },
      ],
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<InstallationSettingsCard />);

    const dialog = await openFrom(user, /edit reporting timezone/i);
    const zone = within(dialog).getByLabelText(/reporting timezone/i);
    await user.clear(zone);
    await user.type(zone, "Europe/Berlim");
    await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

    const refusal = await screen.findByText("Not an IANA time zone.");
    // ON the field: named by the control, so a screen reader hears the refusal
    // when it lands on the input rather than only if the reader wanders past a
    // paragraph at the foot of the form.
    expect(zone.getAttribute("aria-invalid")).toBe("true");
    expect(refusal.id).not.toBe("");
    expect(zone.getAttribute("aria-describedby") ?? "").toContain(refusal.id);
    // A refused save leaves the form open with what was typed still in it —
    // dismissing it on a refusal would discard the edit AND the reason for it.
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("says a refusal once — on the field, or in the summary, never both", async () => {
    const user = userEvent.setup();
    const { fetchMock } = backendFor(SETTINGS_EDITOR, {
      refuse: [
        { field: "name", code: "too_long", message: "That name is too long." },
      ],
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<InstallationSettingsCard />);

    const dialog = await openFrom(user, /edit organization name/i);
    const name = within(dialog).getByLabelText(/organization name/i);
    await user.type(name, " GmbH");
    await user.click(within(dialog).getByRole("button", { name: /^save$/i }));

    const refusal = await screen.findByText("That name is too long.");
    // One occurrence, and it is the FIELD's. A refusal drawn on the input AND
    // repeated in a paragraph below states one problem twice, and the paragraph
    // is the copy a reader learns to skip — so the blanket summary carries only
    // what no field claimed.
    expect(screen.getAllByText("That name is too long.")).toHaveLength(1);
    expect(refusal.className).toContain("field-error");
    expect(screen.getAllByRole("alert")).toHaveLength(1);
  });
});
