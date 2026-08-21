/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { SettingsScreen, settingsAddress } from "./settings";
import { IDLE_JOB_HEALTH, jsonResponse, render } from "./settings.testkit";

// The danger zone on the Maintenance entry. Reset data is the one control on
// this screen that destroys an installation's data, so it is gated twice — the
// literal admin role AND the switch a deployment arms — and it owes the admin
// who ran it a report of what it actually cleared.

// No shared fetch stub: the backend a claim needs is installed beside the claim,
// so what answered it is readable where it is asserted.
beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
});

// The danger-zone Reset data action: server-driven, gated on the literal admin
// role AND me.data_reset_available — the switch a deployment arms, not the
// posture it happens to run under. A dedicated backend per test so the
// role/capability combination is explicit rather than layered on the shared
// default. `allow` defaults to the reindex write that opens the Maintenance
// entry the card lives on — a test about the card should not also have to argue
// its way onto the entry, and one that wants it CLOSED says so with `{}`.
function resetDataBackend(opts: {
  roles: string[];
  dataResetAvailable: boolean;
  allow?: GrantSpec;
  onReset?: (body: unknown) => void;
  resetStatus?: number;
  resetBody?: unknown;
}) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    const method = (
      input instanceof Request ? input.method : (init?.method ?? "GET")
    ).toUpperCase();
    if (url.endsWith("/v1/me")) {
      const me = meFixture({
        roles: opts.roles,
        allow: opts.allow ?? { embedding_reindex: ["read", "update"] },
      });
      return jsonResponse({
        ...me,
        user: { ...me.user, email: "ada@acme.test" },
        workspace_name: "Acme Inc",
        data_reset_available: opts.dataResetAvailable,
      });
    }
    // The job report is the danger zone's neighbour on this entry, and an admin
    // fetches it on arrival — so it answers with the shape the endpoint serves.
    // A generic `{data: []}` here would crash the card that reads it, and every
    // assertion below would fail describing the wrong thing.
    if (url.includes("/admin/job-health")) {
      return jsonResponse(IDLE_JOB_HEALTH);
    }
    if (url.includes("/admin/reset-data") && method === "POST") {
      const raw = input instanceof Request ? await input.clone().text() : "";
      const body = raw ? JSON.parse(raw) : {};
      opts.onReset?.(body);
      if (opts.resetStatus && opts.resetStatus !== 200) {
        return jsonResponse(
          opts.resetBody ?? { detail: "confirmation mismatch" },
          opts.resetStatus,
        );
      }
      return jsonResponse(
        opts.resetBody ?? { status: "reset", tables_cleared: 3 },
      );
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

describe("ResetDataCard (danger zone)", () => {
  it("shows the Reset data control for an admin where the reset is armed", async () => {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({ roles: ["admin"], dataResetAvailable: true }),
    );
    render(<SettingsScreen route={settingsAddress("maintenance")} />);
    expect(await screen.findByText(/reset data/i)).toBeTruthy();
  });

  it("hides Reset data for an admin where the reset was never armed", async () => {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({ roles: ["admin"], dataResetAvailable: false }),
    );
    render(<SettingsScreen route={settingsAddress("maintenance")} />);
    // The job report is the entry's own card, so its heading proves Maintenance
    // rendered — the danger zone below it is what has to stay away.
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Background jobs" }),
      ).toBeTruthy(),
    );
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });

  it("hides Reset data from a rep even where the reset is armed", async () => {
    vi.stubGlobal(
      "fetch",
      // A rep is no admin and holds no embedding_reindex grant, so Maintenance
      // is not theirs to reach in the first place.
      resetDataBackend({ roles: ["rep"], dataResetAvailable: true, allow: {} }),
    );
    render(<SettingsScreen route={settingsAddress("maintenance")} />);
    // With no member grant, the rep falls back to Account — proven here by
    // the identity card rendering instead of anything maintenance-shaped.
    await waitFor(() => expect(screen.getByText("ada@acme.test")).toBeTruthy());
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });

  // The card is admin-ONLY, narrower than the Maintenance entry that hosts it:
  // the server's auth.RequireAdmin on /admin/reset-data admits only the literal
  // "admin" role, so an ops user — who reaches the entry on the reindex grant
  // and uses its other cards — must never see a Reset-data button that could
  // only 403 on confirm.
  it("reaches Maintenance as ops but never sees Reset data", async () => {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({ roles: ["ops"], dataResetAvailable: true }),
    );
    render(<SettingsScreen route={settingsAddress("maintenance")} />);
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Search index" }),
      ).toBeTruthy(),
    );
    expect(screen.queryByText(/reset data/i)).toBeNull();
  });

  it("enables the confirm button once the input is non-empty and POSTs the typed confirmation", async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      resetDataBackend({
        roles: ["admin"],
        dataResetAvailable: true,
        onReset: (body) => posted.push(body),
      }),
    );
    render(<SettingsScreen route={settingsAddress("maintenance")} />);
    await user.click(
      await screen.findByRole("button", { name: /reset data/i }),
    );

    const dialog = await screen.findByRole("dialog");
    // The org name is shown so the admin can copy it into the input.
    expect(within(dialog).getByText("Acme Inc")).toBeTruthy();
    const confirmButton = within(dialog).getByRole("button", {
      name: /reset everything/i,
    });
    expect(confirmButton).toHaveProperty("disabled", true);

    const input = within(dialog).getByRole("textbox");
    await user.type(input, "Acme Inc");
    expect(confirmButton).toHaveProperty("disabled", false);

    await user.click(confirmButton);

    await waitFor(() => expect(posted).toEqual([{ confirmation: "Acme Inc" }]));
    // The dialog closes and the input clears on success.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("surfaces the server's confirmation-mismatch message on a 422", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      resetDataBackend({
        roles: ["admin"],
        dataResetAvailable: true,
        resetStatus: 422,
        resetBody: {
          detail:
            "The typed confirmation does not match the organization name.",
        },
      }),
    );
    render(<SettingsScreen route={settingsAddress("maintenance")} />);
    await user.click(
      await screen.findByRole("button", { name: /reset data/i }),
    );
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByRole("textbox");
    await user.type(input, "Wrong Name");
    await user.click(
      within(dialog).getByRole("button", { name: /reset everything/i }),
    );
    expect(
      await screen.findByText(
        "The typed confirmation does not match the organization name.",
      ),
    ).toBeTruthy();
  });

  // The full response (Task 8's five extra counters) — an admin who triggers a
  // reset that now spans tables, jobs, streams, cache keys and blob storage
  // learns what actually happened, not just that the button worked.
  const fullResetBody = {
    status: "reset",
    tables_cleared: 84,
    jobs_deleted: 12,
    streams_purged: 12,
    cache_keys_deleted: 341,
    objects_deleted: 7,
    drain_timed_out: false,
  };

  // Fixed precondition for every summary test below: admin + the armed reset,
  // on the Maintenance entry that hosts the card.
  function renderSettingsAsAdmin(opts: { resetResponse: unknown }) {
    vi.stubGlobal(
      "fetch",
      resetDataBackend({
        roles: ["admin"],
        dataResetAvailable: true,
        resetBody: opts.resetResponse,
      }),
    );
    return render(<SettingsScreen route={settingsAddress("maintenance")} />);
  }

  // Opens the confirm dialog, types the confirmation, and submits — the same
  // three steps every summary test needs before it can see a result.
  async function confirmReset(user: UserEvent, orgName: string) {
    await user.click(
      await screen.findByRole("button", { name: /reset data/i }),
    );
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByRole("textbox");
    await user.type(input, orgName);
    await user.click(
      within(dialog).getByRole("button", { name: /reset everything/i }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  }

  it("reports what the reset cleared", async () => {
    const user = userEvent.setup();
    renderSettingsAsAdmin({ resetResponse: fullResetBody });
    await confirmReset(user, "Acme Inc");

    expect(
      await screen.findByText(
        // The whole line, not a prefix: dropping the trailing counters is
        // exactly the regression this guards, and a prefix match would pass.
        "Cleared 84 tables, 12 job rows, 12 event streams, 341 cache keys and 7 stored files.",
      ),
    ).toBeInTheDocument();
  });

  it("warns when a job was still running at drain time", async () => {
    const user = userEvent.setup();
    renderSettingsAsAdmin({
      resetResponse: { ...fullResetBody, drain_timed_out: true },
    });
    await confirmReset(user, "Acme Inc");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /background job was still running/,
    );
  });

  it("shows no summary before a reset has run", async () => {
    renderSettingsAsAdmin({ resetResponse: fullResetBody });
    // Wait for the card itself: until /v1/me resolves ResetDataCard renders
    // null, and an assertion made before that passes against an empty screen
    // rather than against a card that is deliberately quiet.
    const card = (
      await screen.findByRole("button", { name: /reset data/i })
    ).closest("section");
    if (!(card instanceof HTMLElement)) {
      throw new Error("the Reset data control renders outside a card");
    }
    // Read that card rather than the page: the summary is a status region inside
    // it, and the two cards above it on Maintenance each render a loading
    // skeleton that is also a status region while its query is in flight — a
    // page-wide query would be answered by whichever of those was still pending.
    expect(within(card).queryByRole("status")).not.toBeInTheDocument();
  });

  it("clears a prior success summary once a retry fails, rather than showing both", async () => {
    const user = userEvent.setup();
    // The first POST to /admin/reset-data succeeds; the second (a retry, e.g.
    // after a typo) 422s. A dedicated fetch mock rather than resetDataBackend
    // because that helper's resetStatus is fixed for every call — this test
    // needs the response to change between the two attempts.
    let resetCalls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        const method = (
          input instanceof Request ? input.method : (init?.method ?? "GET")
        ).toUpperCase();
        if (url.endsWith("/v1/me")) {
          const me = meFixture({
            roles: ["admin"],
            allow: { embedding_reindex: ["read", "update"] },
          });
          return jsonResponse({
            ...me,
            workspace_name: "Acme Inc",
            data_reset_available: true,
          });
        }
        if (url.includes("/admin/job-health")) {
          return jsonResponse(IDLE_JOB_HEALTH);
        }
        if (url.includes("/admin/reset-data") && method === "POST") {
          resetCalls += 1;
          if (resetCalls === 1) {
            return jsonResponse(fullResetBody);
          }
          return jsonResponse(
            { detail: "The typed confirmation does not match." },
            422,
          );
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    render(<SettingsScreen route={settingsAddress("maintenance")} />);

    await confirmReset(user, "Acme Inc");
    expect(
      await screen.findByText(/Cleared 84 tables, 12 job rows/),
    ).toBeInTheDocument();

    // Retry: the dialog stays open on error, so the summary from the first
    // attempt must not still be sitting behind it.
    await user.click(
      await screen.findByRole("button", { name: /reset data/i }),
    );
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByRole("textbox");
    await user.type(input, "Acme Inc");
    await user.click(
      within(dialog).getByRole("button", { name: /reset everything/i }),
    );

    expect(
      await screen.findByText("The typed confirmation does not match."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
