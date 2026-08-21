/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ChangePasswordCard } from "./passwordcard";

// What this card must not do is let someone press submit into a request the
// server will refuse, and what it must not hide is that the change signs them
// out. Both are cheaper to hold here than to discover in the product.
//
// The three fields live in a dialog the row's verb opens, so every case here
// opens it first — and the two buttons are named for what each one does, the
// row's for opening the form and the dialog's for saving what was typed into
// it, which is what keeps them tellable apart while both are mounted.

function renderCard() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <ChangePasswordCard />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

const submitButton = () =>
  screen.getByRole("button", { name: /^save new password$/i });

// The dialog, opened. Every case needs it, and `userEvent.setup()` is called
// once per test by the caller rather than here: one instance carries the shared
// input-device state, and a second one forgets which keys the first left held.
async function openForm(user: ReturnType<typeof userEvent.setup>) {
  renderCard();
  await user.click(screen.getByRole("button", { name: /^change password$/i }));
  return screen.getByRole("dialog");
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("ChangePasswordCard", () => {
  it("warns that the change signs you out, before the button is pressed", async () => {
    const user = userEvent.setup();
    await openForm(user);
    expect(
      screen.getByText(/signs you out everywhere, including here/i),
    ).toBeInTheDocument();
  });

  it("will not submit until the confirmation matches", async () => {
    const user = userEvent.setup();
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    await openForm(user);

    await user.type(
      screen.getByLabelText(/current password/i),
      "old password!",
    );
    await user.type(
      screen.getByLabelText(/^new password/i),
      "a fine new password",
    );
    await user.type(
      screen.getByLabelText(/confirm new password/i),
      "a fine new passwrod",
    );

    expect(submitButton()).toBeDisabled();
    expect(screen.getByText(/don't match/i)).toBeInTheDocument();
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("will not submit a new password below the server's floor", async () => {
    const user = userEvent.setup();
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    await openForm(user);

    await user.type(
      screen.getByLabelText(/current password/i),
      "old password!",
    );
    await user.type(screen.getByLabelText(/^new password/i), "short");
    await user.type(screen.getByLabelText(/confirm new password/i), "short");

    expect(submitButton()).toBeDisabled();
    // The refusal is announced and reads in the danger tone — not in the same
    // meta-grey as the neutral hint it replaces, and not in the same grey as
    // the success line this card shows in the very same slot.
    const refusal = screen.getByRole("alert");
    expect(refusal).toHaveTextContent(/at least 12 characters/i);
    expect(refusal).toHaveClass("field-error");
    expect(screen.getByLabelText(/^new password/i)).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("counts the floor in characters, not bytes", async () => {
    // ELEVEN emoji is the boundary that separates the two implementations:
    // eleven characters, but twenty-two UTF-16 code units. A `.length` check
    // sees 22 and enables the button on a password the server will reject with
    // a 422; a code-point count sees 11 and refuses it here. A shorter sample
    // proves nothing — four emoji is eight units, below the floor either way.
    const user = userEvent.setup();
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    await openForm(user);

    await user.type(
      screen.getByLabelText(/current password/i),
      "old password!",
    );
    await user.type(screen.getByLabelText(/^new password/i), "🔑".repeat(11));
    await user.type(
      screen.getByLabelText(/confirm new password/i),
      "🔑".repeat(11),
    );
    expect(submitButton()).toBeDisabled();
    expect(fetchSpy).not.toHaveBeenCalled();

    // And exactly twelve characters clears it.
    await user.clear(screen.getByLabelText(/^new password/i));
    await user.clear(screen.getByLabelText(/confirm new password/i));
    await user.type(screen.getByLabelText(/^new password/i), "🔑".repeat(12));
    await user.type(
      screen.getByLabelText(/confirm new password/i),
      "🔑".repeat(12),
    );
    expect(submitButton()).toBeEnabled();
  });

  it("sends both passwords under the keys the server reads", async () => {
    const user = userEvent.setup();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(null, { status: 204 }),
    );
    await openForm(user);

    await user.type(
      screen.getByLabelText(/current password/i),
      "old password!",
    );
    await user.type(
      screen.getByLabelText(/^new password/i),
      "a fine new password",
    );
    await user.type(
      screen.getByLabelText(/confirm new password/i),
      "a fine new password",
    );
    await user.click(submitButton());

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledOnce());
    const [url, init] = vi.mocked(globalThis.fetch).mock.calls[0];
    expect(String(url)).toContain("/v1/auth/change-password");
    const body = JSON.parse(String(init?.body));
    expect(body.current_password).toBe("old password!");
    expect(body.new_password).toBe("a fine new password");
    // The confirmation is a client-side check and has no business on the wire.
    expect(body).not.toHaveProperty("confirm");
  });

  it("stops claiming success once a later attempt fails", async () => {
    // Otherwise the page states both that the password changed and that it did
    // not, and the reader has no way to tell which is current.
    const user = userEvent.setup();
    const fetchSpy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ detail: "nope" }), {
          status: 401,
          headers: { "Content-Type": "application/problem+json" },
        }),
      );
    await openForm(user);

    // Reopened for the second attempt, because the first one succeeded and a
    // successful change closes the dialog. That is the shape of the real
    // sequence this case is about: a reader who changed their password, came
    // back, and got refused.
    const fill = async (next: string) => {
      await user.type(
        screen.getByLabelText(/current password/i),
        "old password!",
      );
      await user.type(screen.getByLabelText(/^new password/i), next);
      await user.type(screen.getByLabelText(/confirm new password/i), next);
      await user.click(submitButton());
    };
    await fill("a fine new password");
    await screen.findByRole("status");

    await user.click(
      screen.getByRole("button", { name: /^change password$/i }),
    );
    await fill("another fine password");
    await screen.findByRole("alert");
    expect(screen.queryByRole("status")).toBeNull();
    expect(fetchSpy).toHaveBeenCalledTimes(2);
  });

  it("reports the server's own refusal rather than a generic one", async () => {
    const user = userEvent.setup();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({ detail: "the current password does not match" }),
        {
          status: 401,
          headers: { "Content-Type": "application/problem+json" },
        },
      ),
    );
    await openForm(user);

    await user.type(
      screen.getByLabelText(/current password/i),
      "wrong password!",
    );
    await user.type(
      screen.getByLabelText(/^new password/i),
      "a fine new password",
    );
    await user.type(
      screen.getByLabelText(/confirm new password/i),
      "a fine new password",
    );
    await user.click(submitButton());

    // Which of the two fields was wrong is the whole value of the message;
    // a generic "couldn't be changed" would send someone re-typing both.
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /current password does not match/i,
    );
  });

  // The fields stay editable while the change is in flight, so `ready` — the
  // precondition that decides whether the change may START — can go false in
  // the middle of a request that has already left. Read as a refusal at that
  // point it would natively disable the button, and a natively disabled button
  // drops the focus the reader is standing on and stops exposing the busy
  // state, mid-change.
  it("stays busy when a field is cleared after the change is already going", async () => {
    const user = userEvent.setup();
    // Never settles: the in-flight state is a state to look at rather than a
    // window this test has to race.
    vi.spyOn(globalThis, "fetch").mockReturnValue(new Promise(() => {}));
    await openForm(user);
    await user.type(
      screen.getByLabelText(/current password/i),
      "old-password-1",
    );
    await user.type(
      screen.getByLabelText(/^new password/i),
      "new-password-long",
    );
    await user.type(
      screen.getByLabelText(/confirm new password/i),
      "new-password-long",
    );
    await user.click(submitButton());

    await waitFor(() =>
      expect(submitButton()).toHaveAttribute("aria-busy", "true"),
    );
    await user.clear(screen.getByLabelText(/current password/i));

    // Still going, still focusable, still saying so.
    expect(submitButton()).toHaveAttribute("aria-busy", "true");
    expect(submitButton()).toBeEnabled();
  });

  it("closes the dialog on success so the typed password does not linger on screen", async () => {
    const user = userEvent.setup();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(null, { status: 204 }),
    );
    await openForm(user);

    await user.type(
      screen.getByLabelText(/current password/i),
      "old password!",
    );
    await user.type(
      screen.getByLabelText(/^new password/i),
      "a fine new password",
    );
    await user.type(
      screen.getByLabelText(/confirm new password/i),
      "a fine new password",
    );
    await user.click(submitButton());

    expect(await screen.findByRole("status")).toHaveTextContent(
      /password changed/i,
    );
    // Gone, not blank: the outcome is a sentence on the row, and a reader who
    // has just been signed out everywhere has nothing left to type here.
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByLabelText(/current password/i)).toBeNull();
  });
});
