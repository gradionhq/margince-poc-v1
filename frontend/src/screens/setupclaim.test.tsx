/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { fetchSetupStatus, SetupClaimScreen } from "./setupclaim";

// The claim screen is the one place in the product that creates an account
// without one. What these cases hold is that it cannot be submitted into a weak
// root credential, that each refusal reads as the thing that happened, and that
// a probe which cannot answer never claims the installation is claimable.

function renderClaim(onClaimed = vi.fn()) {
  render(
    <LocaleProvider initial="en">
      <SetupClaimScreen onClaimed={onClaimed} />
    </LocaleProvider>,
  );
  return onClaimed;
}

async function fillValid(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/setup token/i), "a-token");
  await user.type(screen.getByLabelText(/organization name/i), "Acme");
  await user.type(screen.getByLabelText(/your name/i), "Ops");
  await user.type(screen.getByLabelText(/your email/i), "ops@acme.test");
  await user.type(
    screen.getByLabelText(/choose a password/i),
    "a bootstrap password!",
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("SetupClaimScreen", () => {
  it("says what it is creating, because minting root quietly is not honest", () => {
    renderClaim();
    expect(
      screen.getByText(/administrator account for the whole installation/i),
    ).toBeInTheDocument();
  });

  it("refuses to submit a password below the server's floor", async () => {
    const user = userEvent.setup();
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    renderClaim();

    await user.type(screen.getByLabelText(/setup token/i), "a-token");
    await user.type(screen.getByLabelText(/organization name/i), "Acme");
    await user.type(screen.getByLabelText(/your name/i), "Ops");
    await user.type(screen.getByLabelText(/your email/i), "ops@acme.test");
    await user.type(screen.getByLabelText(/choose a password/i), "short");

    // The button is the gate, and the hint says why — a form that lets you
    // press submit and then reports a 422 has wasted the round trip and the
    // person's attention.
    expect(
      screen.getByRole("button", { name: /create the organization/i }),
    ).toBeDisabled();
    expect(screen.getByText(/at least 12 characters/i)).toBeInTheDocument();
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("sends the claim and hands back to the boundary on success", async () => {
    const user = userEvent.setup();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ workspace_id: "ws-1" }), { status: 201 }),
    );
    const onClaimed = renderClaim();

    await fillValid(user);
    await user.click(
      screen.getByRole("button", { name: /create the organization/i }),
    );

    await waitFor(() => expect(onClaimed).toHaveBeenCalledOnce());
    const [, init] = vi.mocked(globalThis.fetch).mock.calls[0];
    const body = JSON.parse(String(init?.body));
    expect(body.setup_token).toBe("a-token");
    expect(body.admin_email).toBe("ops@acme.test");
    // The field name the server reads. A camelCase key here would decode to an
    // empty password, which is the failure this whole path exists to prevent.
    expect(body.admin_password).toBe("a bootstrap password!");
  });

  it("tells a wrong token apart from an installation someone else claimed", async () => {
    const user = userEvent.setup();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("{}", { status: 401 }),
    );
    renderClaim();
    await fillValid(user);
    await user.click(
      screen.getByRole("button", { name: /create the organization/i }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /setup token isn't valid/i,
    );
  });

  it("says the installation is already claimed on a conflict", async () => {
    const user = userEvent.setup();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("{}", { status: 409 }),
    );
    renderClaim();
    await fillValid(user);
    await user.click(
      screen.getByRole("button", { name: /create the organization/i }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /already has an organization/i,
    );
  });

  it("does not hand back to the boundary when the claim was refused", async () => {
    const user = userEvent.setup();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("{}", { status: 401 }),
    );
    const onClaimed = renderClaim();
    await fillValid(user);
    await user.click(
      screen.getByRole("button", { name: /create the organization/i }),
    );
    await screen.findByRole("alert");
    expect(onClaimed).not.toHaveBeenCalled();
  });
});

describe("fetchSetupStatus", () => {
  it("reads a claimable installation", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ claimable: true }), { status: 200 }),
    );
    await expect(fetchSetupStatus()).resolves.toEqual({ claimable: true });
  });

  it("answers not-claimable when the probe fails, rather than throwing", async () => {
    // The caller is a boundary that has already decided the installation is
    // unavailable. A probe that cannot answer must leave the true message
    // standing, not replace it with a claim screen or an error page.
    vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("offline"));
    await expect(fetchSetupStatus()).resolves.toEqual({ claimable: false });

    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("nope", { status: 500 }),
    );
    await expect(fetchSetupStatus()).resolves.toEqual({ claimable: false });
  });

  it("treats a body that is not the expected shape as not claimable", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ claimable: "yes" }), { status: 200 }),
    );
    await expect(fetchSetupStatus()).resolves.toEqual({ claimable: false });
  });
});
