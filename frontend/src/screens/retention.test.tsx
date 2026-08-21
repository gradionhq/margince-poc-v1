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
import { afterEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { RetentionCard } from "./retention";

// Settings → Privacy → Retention. What these tests are actually about: an
// ENABLED policy can be inert, and the screen has to say so per row. The rest
// of the surface (the posture write, the duplicate-scope refusal, the
// unknown-scope refusal, disable-not-delete, and the confirm on delete) is
// asserted the same way — through the wire, never through internals.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const RETENTION_ADMIN: GrantSpec = {
  retention_policy: ["read", "create", "update", "delete"],
};

// An erase policy the posture is currently overriding, and an archive policy it
// is not — the pair the suppressed indicator has to distinguish.
const TRANSCRIPTS = {
  id: "00000000-0000-4000-8000-0000000000a1",
  scope: "activity/transcript",
  object_type: "activity",
  category: "transcript",
  retain_days: 365,
  action: "erase",
  lawful_basis: "Art. 9(2)(a)",
  enabled: true,
  suppressed_by_posture: true,
};

const WON_DEALS = {
  id: "00000000-0000-4000-8000-0000000000a2",
  scope: "deal/won",
  object_type: "deal",
  category: "won",
  retain_days: 2555,
  action: "archive",
  lawful_basis: null,
  enabled: true,
  suppressed_by_posture: false,
};

type Sent = { key: string; body: unknown };

type BackendOptions = Readonly<{
  allow?: GrantSpec;
  retainOnly?: boolean;
  policies?: unknown[];
  overrides?: Record<string, () => Response>;
}>;

// Answers /me with the given grants, the posture and the policy list from the
// options, and records every write so a test can assert the request body — the
// body IS the contract for a policy write.
function backend(options: BackendOptions = {}) {
  const sent: Sent[] = [];
  const allow = options.allow ?? RETENTION_ADMIN;
  let retainOnly = options.retainOnly ?? true;
  const policies = options.policies ?? [TRANSCRIPTS, WON_DEALS];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request =
        input instanceof Request ? input : new Request(String(input), init);
      const url = new URL(request.url, "https://test.local");
      const key = `${request.method} ${url.pathname.replace(/^\/v1/, "")}`;
      let body: unknown = null;
      if (request.method !== "GET" && request.method !== "DELETE") {
        body = await request.json().catch(() => null);
      }
      sent.push({ key, body });
      const override = options.overrides?.[key];
      if (override) {
        return override();
      }
      if (key === "GET /me") {
        return jsonResponse(meFixture({ allow }));
      }
      if (key === "GET /retention/settings") {
        return jsonResponse({ retain_only: retainOnly });
      }
      if (key === "PATCH /retention/settings") {
        retainOnly = (body as { retain_only: boolean }).retain_only;
        return jsonResponse({ retain_only: retainOnly });
      }
      if (key === "GET /retention-policies") {
        return jsonResponse({
          // The posture decides suppression, so the list answers in step with
          // whatever the posture currently is — exactly as the server derives
          // it, which is what makes the re-render after a posture write real.
          data: policies.map((policy) => ({
            ...(policy as object),
            suppressed_by_posture:
              retainOnly && (policy as { action: string }).action !== "archive",
          })),
          page: { next_cursor: null, has_more: false },
        });
      }
      throw new Error(`unexpected request: ${key}`);
    }),
  );
  return sent;
}

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

async function findRow(scope: string): Promise<HTMLElement> {
  return screen.findByTestId(`retention-row-${scope}`);
}

// The window, the action, the basis and the Enabled switch are committed
// together, so they live in the dialog the row's Edit verb opens rather than in
// a panel that unfolds under the row. Every assertion about the editor is
// therefore scoped to the DIALOG, and the row keeps only what it does tonight.
async function openEditor(scope: string): Promise<HTMLElement> {
  const row = await findRow(scope);
  await userEvent.click(within(row).getByRole("button", { name: /edit/i }));
  return screen.findByRole("dialog");
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("RetentionCard rows", () => {
  it("says which enabled policy the posture is holding back, and which it is not", async () => {
    backend();
    render(<RetentionCard />);

    const suppressed = await findRow("activity/transcript");
    expect(
      within(suppressed).getByText(/suppressed by retain-only/i),
    ).toBeInTheDocument();
    // Not just a badge: the row states the consequence, because "enabled but
    // inert" is the one thing a reader cannot infer from the other columns.
    expect(
      within(suppressed).getByText(/will not act until the posture/i),
    ).toBeInTheDocument();

    // Archiving retains, so the posture leaves it alone — and this row must
    // carry none of that copy, or the indicator would mean nothing.
    const acting = await findRow("deal/won");
    expect(within(acting).getByText(/acting nightly/i)).toBeInTheDocument();
    expect(
      within(acting).queryByText(/suppressed by retain-only/i),
    ).not.toBeInTheDocument();
  });

  it("offers a retry when the ladder itself could not be read", async () => {
    let attempts = 0;
    const sent = backend({
      retainOnly: false,
      overrides: {
        "GET /retention-policies": () => {
          attempts += 1;
          // Fails once, then answers — so the retry has something to prove
          // beyond re-rendering the same failure.
          return attempts === 1
            ? jsonResponse(
                {
                  title: "Bad Gateway",
                  status: 502,
                  code: "internal",
                  detail: "the policy store is unreachable",
                },
                502,
              )
            : jsonResponse({
                data: [WON_DEALS],
                page: { next_cursor: null, has_more: false },
              });
        },
      },
    });
    render(<RetentionCard />);

    expect(
      await screen.findByText(/the policy store is unreachable/i),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /retry/i }));

    expect(await findRow("deal/won")).toBeInTheDocument();
    expect(
      sent.filter((call) => call.key === "GET /retention-policies"),
    ).toHaveLength(2);
  });

  it("reads a disabled policy as kept-and-inert, never as deleted", async () => {
    backend({
      retainOnly: false,
      policies: [{ ...WON_DEALS, enabled: false }],
    });
    render(<RetentionCard />);

    const row = await findRow("deal/won");
    expect(within(row).getByText(/disabled/i)).toBeInTheDocument();
    expect(
      within(row).getByText(/its window is preserved/i),
    ).toBeInTheDocument();
    // Still on screen with its window intact — the whole point of E2.
    expect(within(row).getByText(/2555 days/i)).toBeInTheDocument();
  });

  it("disables a policy through enabled:false rather than by deleting it", async () => {
    const sent = backend({ retainOnly: false });
    render(<RetentionCard />);

    const row = await openEditor("deal/won");
    await userEvent.click(
      within(row).getByRole("switch", { name: /enabled/i }),
    );

    await waitFor(() => {
      expect(
        sent.find(
          (call) =>
            call.key === `PATCH /retention-policies/${WON_DEALS.id}` &&
            (call.body as { enabled?: boolean }).enabled === false,
        ),
      ).toBeDefined();
    });
    expect(sent.some((call) => call.key.startsWith("DELETE "))).toBe(false);
  });

  // Pausing and saving are two writes, and only saving is finished with the
  // panel. Collapsing it on the pause would unmount the fields being edited and
  // the switch that was just operated, sending focus to the document body.
  it("keeps the open editor open when the operator flips Enabled", async () => {
    // The write has to LAND here, and the list has to re-read afterwards: a panel
    // that outlives a rejected PATCH proves nothing about the onSuccess that
    // closes it. So the stored row is a mutable one the PATCH really pauses, and
    // the next GET answers with it.
    const stored = { ...WON_DEALS };
    const sent = backend({
      retainOnly: false,
      policies: [stored],
      overrides: {
        [`PATCH /retention-policies/${WON_DEALS.id}`]: () => {
          stored.enabled = false;
          return jsonResponse(stored);
        },
      },
    });
    render(<RetentionCard />);

    const row = await openEditor("deal/won");
    // Typed and deliberately not saved: an unsaved edit is what a collapsing
    // panel takes with it.
    const days = within(row).getByLabelText(/window in days/i);
    await userEvent.clear(days);
    await userEvent.type(days, "900");

    await userEvent.click(
      within(row).getByRole("switch", { name: /enabled/i }),
    );

    // Synchronised on the state the RE-READ brings back rather than on the PATCH
    // being sent — a request is recorded before its response, so asserting there
    // would inspect the panel while a close was still pending and pass however
    // the mutation is wired. Reaching the switch through the row is the pin: a
    // collapsed panel has no switch to find.
    await waitFor(() =>
      expect(
        within(row).getByRole("switch", { name: /enabled/i }),
      ).toHaveAttribute("aria-checked", "false"),
    );
    expect(
      sent.some(
        (call) =>
          call.key === `PATCH /retention-policies/${WON_DEALS.id}` &&
          (call.body as { enabled?: boolean }).enabled === false,
      ),
    ).toBe(true);
    // And the draft and the rest of the panel came through with it.
    expect(within(row).getByLabelText(/window in days/i)).toHaveValue("900");
    expect(
      within(row).getByRole("button", { name: /save policy/i }),
    ).toBeInTheDocument();
  });

  // The three edit fields are local state that outlives a close, because the row
  // is keyed on the policy id and is never remounted. Re-seeding on open is what
  // keeps an abandoned draft from reappearing over a summary that contradicts
  // it — and from being what Save then sends.
  it("re-seeds the editor from the stored policy each time it opens", async () => {
    backend({ retainOnly: false });
    render(<RetentionCard />);

    // The editor is a dialog the row's verb opens, so "each time it opens" is
    // now literally that: type something, leave without saving, open it again.
    // Escape is the way out that keeps the row's Edit button reachable —
    // reaching for it through the dialog is what this assertion used to do, and
    // the button was never inside it.
    const dialog = await openEditor("deal/won");
    const days = within(dialog).getByLabelText(/window in days/i);
    await userEvent.clear(days);
    await userEvent.type(days, "900");
    await userEvent.keyboard("{Escape}");

    const reopened = await openEditor("deal/won");
    expect(within(reopened).getByLabelText(/window in days/i)).toHaveValue(
      "2555",
    );
  });

  it("patches the window and action the operator edited", async () => {
    const sent = backend({ retainOnly: false });
    render(<RetentionCard />);

    const row = await openEditor("deal/won");
    const days = within(row).getByLabelText(/window in days/i);
    await userEvent.clear(days);
    await userEvent.type(days, "900");
    await userEvent.click(
      within(row).getByRole("button", { name: /save policy/i }),
    );

    await waitFor(() => {
      expect(
        sent.find(
          (call) => call.key === `PATCH /retention-policies/${WON_DEALS.id}`,
        )?.body,
      ).toEqual({
        retain_days: 900,
        action: "archive",
        lawful_basis: null,
      });
    });
  });

  it("refuses to send a window the contract would reject", async () => {
    const sent = backend({ retainOnly: false });
    render(<RetentionCard />);

    const row = await openEditor("deal/won");
    const days = within(row).getByLabelText(/window in days/i);
    await userEvent.clear(days);
    await userEvent.type(days, "0");

    expect(within(row).getByText(/whole number of days/i)).toBeInTheDocument();
    expect(
      within(row).getByRole("button", { name: /save policy/i }),
    ).toBeDisabled();
    expect(sent.some((call) => call.key.startsWith("PATCH /retention-p"))).toBe(
      false,
    );
  });
});

describe("the retain-only posture", () => {
  it("writes the posture and re-renders every row's suppression", async () => {
    const sent = backend({ retainOnly: false });
    render(<RetentionCard />);

    // Off to begin with: the destructive policy is acting.
    const before = await findRow("activity/transcript");
    expect(within(before).getByText(/acting nightly/i)).toBeInTheDocument();

    await userEvent.click(
      await screen.findByRole("switch", { name: /retain-only posture/i }),
    );

    await waitFor(() => {
      expect(
        sent.find((call) => call.key === "PATCH /retention/settings")?.body,
      ).toEqual({ retain_only: true });
    });
    // The rows are re-read, not just the toggle: suppression is derived from
    // the posture server-side, so a posture write that left the list alone
    // would leave every destructive row claiming it still acts.
    await waitFor(async () => {
      expect(
        within(await findRow("activity/transcript")).getByText(
          /suppressed by retain-only/i,
        ),
      ).toBeInTheDocument();
    });
  });

  it("shows the posture but withholds the switch without the update grant", async () => {
    backend({ allow: { retention_policy: ["read"] } });
    render(<RetentionCard />);

    expect(
      await screen.findByRole("switch", { name: /retain-only posture/i }),
    ).toBeDisabled();
    expect(
      screen.getByText(/only an admin or ops can change retention/i),
    ).toBeInTheDocument();
    // A reader still sees WHY a row is inert; only the controls are withheld.
    const row = await findRow("activity/transcript");
    expect(
      within(row).getByText(/suppressed by retain-only/i),
    ).toBeInTheDocument();
    expect(
      within(row).queryByRole("button", { name: /edit/i }),
    ).not.toBeInTheDocument();
  });

  it("announces on the switch itself why the posture cannot be changed", async () => {
    backend({ allow: { retention_policy: ["read"] } });
    render(<RetentionCard />);

    const posture = await screen.findByRole("switch", {
      name: /retain-only posture/i,
    });
    // Announced WITH the control rather than printed beside it: a detached
    // paragraph reaches the eye and never the reader who hears only the switch.
    expect(posture).toHaveAccessibleDescription(
      /only an admin or ops can change retention/i,
    );
  });

  // The posture is ONE switch, so it answers its row from the right column like
  // every other single control in settings — it used to sit below its own label
  // with two sentences under it, the only control on the page off the answer
  // column's x. What the posture DOES is the row's description, in the naming
  // column where a sentence has the width to be one.
  it("puts the switch in the answer column and the sentence in the naming", async () => {
    backend({ retainOnly: false });
    render(<RetentionCard />);

    const posture = await screen.findByRole("switch", {
      name: /retain-only posture/i,
    });
    const row = posture.closest(".settingrow");
    expect(row).not.toBeNull();
    if (row instanceof HTMLElement) {
      expect(row.className).not.toContain("settingrow-stack");
      expect(
        within(row)
          .getByText(/destroys nothing/i)
          .closest(".settingrow-naming"),
      ).not.toBeNull();
      expect(posture.closest(".settingrow-control")).not.toBeNull();
    }
  });

  // The posture and the ladder are separate reads, and a posture that failed
  // to load must not take the policy list down with it — the rows are still
  // the truth about what is authored.
  it("reports a posture that could not be read without hiding the ladder", async () => {
    backend({
      overrides: {
        "GET /retention/settings": () =>
          jsonResponse(
            {
              title: "Bad Gateway",
              status: 502,
              code: "internal",
              detail: "the settings store is unreachable",
            },
            502,
          ),
      },
    });
    render(<RetentionCard />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /settings store is unreachable/i,
    );
    expect(await findRow("deal/won")).toBeInTheDocument();
  });

  // Withheld, not absent: this card shares its page with the consent registry an
  // ops seat comes here for, and with a subject queue that explains its own
  // emptiness. A card that vanished would leave that reader to conclude the
  // installation keeps everything forever.
  it("says the ladder is withheld, and asks the server for nothing", async () => {
    const sent = backend({ allow: {} });
    render(<RetentionCard />);

    expect(
      await screen.findByText(/only an admin or ops can see the retention/i),
    ).toBeInTheDocument();
    // Its place, not just its words — the card keeps the heading it has for
    // every other reader.
    expect(screen.getByText("Retention")).toBeInTheDocument();
    // The half of the old behaviour worth keeping: the denial is already known,
    // so neither read fires. A card that could only report an expected 403 would
    // hand the reader a failure with a Retry that cannot succeed.
    expect(sent.filter((call) => call.key !== "GET /me")).toEqual([]);
  });
});

describe("authoring a policy", () => {
  async function openCreateForm() {
    await userEvent.click(
      await screen.findByRole("button", { name: /add policy/i }),
    );
  }

  it("creates the scope, window and action the operator chose", async () => {
    const sent = backend({ retainOnly: false, policies: [] });
    render(<RetentionCard />);
    await openCreateForm();

    await pickOption(
      userEvent.setup(),
      screen.getByRole("combobox", { name: /applies to/i }),
      "Lost deals",
    );
    await userEvent.type(screen.getByLabelText(/window in days/i), "180");
    await userEvent.click(
      screen.getByRole("button", { name: /create policy/i }),
    );

    await waitFor(() => {
      expect(
        sent.find((call) => call.key === "POST /retention-policies")?.body,
      ).toEqual({
        scope: "deal/lost",
        retain_days: 180,
        action: "archive",
        lawful_basis: null,
        enabled: true,
      });
    });
  });

  it("carries the basis it was given and an authored-inert policy", async () => {
    const sent = backend({ retainOnly: false, policies: [] });
    render(<RetentionCard />);
    await openCreateForm();

    await userEvent.type(screen.getByLabelText(/window in days/i), "90");
    await userEvent.type(
      screen.getByLabelText(/lawful basis/i),
      "Art. 6(1)(f)",
    );
    // Authoring a rule to sit inert is legitimate: an operator writes the
    // window now and switches it on once legal has read it.
    await userEvent.click(screen.getByRole("checkbox", { name: /enabled/i }));
    await pickOption(
      userEvent.setup(),
      screen.getByRole("combobox", { name: /^action$/i }),
      "Anonymise",
    );
    await userEvent.click(
      screen.getByRole("button", { name: /create policy/i }),
    );

    await waitFor(() => {
      expect(
        sent.find((call) => call.key === "POST /retention-policies")?.body,
      ).toEqual({
        scope: "lead/unconverted",
        retain_days: 90,
        action: "anonymize",
        lawful_basis: "Art. 6(1)(f)",
        enabled: false,
      });
    });
  });

  // A 409 has exactly one cause here (the unique constraint on the scope), and
  // the operator needs the row to edit — not the constraint's own sentence.
  it("turns a duplicate-scope 409 into a readable refusal", async () => {
    backend({
      retainOnly: false,
      overrides: {
        "POST /retention-policies": () =>
          jsonResponse(
            {
              type: "https://errors.gradion.com/conflict",
              title: "Conflict",
              status: 409,
              code: "conflict",
              detail:
                'retention policy for scope "deal/won" already exists: conflict',
            },
            409,
          ),
      },
    });
    render(<RetentionCard />);
    await openCreateForm();
    await userEvent.type(screen.getByLabelText(/window in days/i), "180");
    await userEvent.click(
      screen.getByRole("button", { name: /create policy/i }),
    );

    const refusal = await screen.findByRole("alert");
    expect(refusal).toHaveTextContent(
      /a policy for this scope already exists/i,
    );
    expect(refusal).toHaveTextContent(/edit the existing row/i);
    // Never the store's own wording, which names a constraint and a Go
    // sentinel rather than what to do about it.
    expect(refusal).not.toHaveTextContent(/conflict/i);
  });

  // The server validates the scope against the evaluator's selector table,
  // which can be narrower than the contract enum. Its refusal names what IS
  // authorable, so it reaches the operator verbatim.
  it("relays the server's own words for an unknown scope", async () => {
    backend({
      retainOnly: false,
      overrides: {
        "POST /retention-policies": () =>
          jsonResponse(
            {
              type: "https://errors.gradion.com/validation-error",
              title: "Unprocessable Entity",
              status: 422,
              code: "validation_error",
              detail:
                'retention scope "deal/won" is not authorable; authorable scopes are activity, activity/transcript, deal/lost',
            },
            422,
          ),
      },
    });
    render(<RetentionCard />);
    await openCreateForm();
    await userEvent.type(screen.getByLabelText(/window in days/i), "180");
    await userEvent.click(
      screen.getByRole("button", { name: /create policy/i }),
    );

    const refusal = await screen.findByRole("alert");
    expect(refusal).toHaveTextContent(/is not authorable/i);
    expect(refusal).toHaveTextContent(/authorable scopes are/i);
  });

  it("keeps the create button inert until the window is a window", async () => {
    backend({ retainOnly: false });
    render(<RetentionCard />);
    await openCreateForm();
    expect(
      screen.getByRole("button", { name: /create policy/i }),
    ).toBeDisabled();
  });

  it("hides the add affordance without the create grant", async () => {
    backend({ allow: { retention_policy: ["read", "update"] } });
    render(<RetentionCard />);
    await findRow("deal/won");
    expect(
      screen.queryByRole("button", { name: /add policy/i }),
    ).not.toBeInTheDocument();
  });

  // A create verb is not a settings decision, so it has no row of its own — the
  // one it had used the button's own words for a label, saying "Add policy"
  // twice a hand apart, and it moved down the card every time a policy was
  // authored. It rides in the panel header instead, above a ladder that grows.
  it("carries the verb in the card header rather than in a row of its own", async () => {
    backend({ retainOnly: false });
    render(<RetentionCard />);

    const verb = await screen.findByRole("button", { name: /add policy/i });
    expect(verb.closest(".panel-head")).not.toBeNull();
    expect(verb.closest(".settingrow")).toBeNull();
    // The words are said once on the card: the row that repeated them is gone.
    expect(screen.getAllByText(/^Add policy$/)).toHaveLength(1);
  });
});

describe("deleting a policy", () => {
  it("goes through the confirm modal, and only then issues the DELETE", async () => {
    const sent = backend({ retainOnly: false });
    render(<RetentionCard />);

    const row = await openEditor("deal/won");
    await userEvent.click(
      within(row).getByRole("button", { name: /delete policy/i }),
    );

    // Staged, not sent: the dialog explains that deleting is not pausing.
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/nothing in that scope ages out/i);
    expect(dialog).toHaveTextContent(/turn enabled off instead/i);
    expect(sent.some((call) => call.key.startsWith("DELETE "))).toBe(false);

    await userEvent.click(
      within(dialog).getByRole("button", { name: /delete policy/i }),
    );
    await waitFor(() => {
      expect(
        sent.some(
          (call) => call.key === `DELETE /retention-policies/${WON_DEALS.id}`,
        ),
      ).toBe(true);
    });
  });

  it("sends nothing when the confirm is dismissed", async () => {
    const sent = backend({ retainOnly: false });
    render(<RetentionCard />);

    const row = await openEditor("deal/won");
    await userEvent.click(
      within(row).getByRole("button", { name: /delete policy/i }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: /cancel/i }),
    );

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(sent.some((call) => call.key.startsWith("DELETE "))).toBe(false);
  });

  it("withholds delete without the delete grant", async () => {
    backend({ allow: { retention_policy: ["read", "update"] } });
    render(<RetentionCard />);

    const row = await openEditor("deal/won");
    expect(
      within(row).queryByRole("button", { name: /delete policy/i }),
    ).not.toBeInTheDocument();
    expect(
      within(row).getByRole("button", { name: /save policy/i }),
    ).toBeInTheDocument();
  });
});
