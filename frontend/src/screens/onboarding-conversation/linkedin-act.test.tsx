/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../i18n";
import {
  conversationReducer,
  initialConversationState,
} from "./conversation-machine";
import { LinkedInAct } from "./linkedin-act";

// The LinkedIn act asks for the network BEFORE the inbox, says why, names
// exactly what it will read, and can be declined in one click.

function renderAct(dispatch = vi.fn()) {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <LinkedInAct
          state={{
            ...initialConversationState,
            act: "linkedin" as const,
            phase: "ln.why" as const,
          }}
          dispatch={dispatch}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return dispatch;
}

beforeEach(() => vi.stubGlobal("scrollTo", vi.fn()));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("says why the network matters before asking for it", () => {
  renderAct();
  expect(screen.getByText(/who your team already knows/i)).toBeTruthy();
  // The promise a member is owed before handing over their address book.
  expect(screen.getByText(/do NOT become contacts/i)).toBeTruthy();
});

it("names every scope it will read, and that it reads nothing else", () => {
  renderAct();
  expect(
    screen.getByText(/name, position, company and the date you connected/i),
  ).toBeTruthy();
  expect(
    screen.getByText(/No messages, no posts, no who-viewed-you/i),
  ).toBeTruthy();
  expect(
    screen.getByText(/sends no invitations and no messages/i),
  ).toBeTruthy();
});

it("will not authorize until the profile it attributes the network to is given", async () => {
  const dispatch = renderAct();
  const button = screen.getByRole("button", {
    name: "Authorize with LinkedIn",
  });
  expect(button).toBeDisabled();

  await userEvent.type(
    screen.getByTestId("linkedin-profile"),
    "https://www.linkedin.com/in/lars",
  );
  expect(button).not.toBeDisabled();
  await userEvent.click(button);
  expect(dispatch).toHaveBeenCalledWith({
    type: "LINKEDIN_CONNECTED",
    profile: "https://www.linkedin.com/in/lars",
  });
});

it("can be declined in one click, without a profile", async () => {
  const dispatch = renderAct();
  await userEvent.click(
    screen.getByRole("button", { name: "Skip LinkedIn for now" }),
  );
  expect(dispatch).toHaveBeenCalledWith({ type: "LINKEDIN_SKIPPED" });
});

// The developer app is not approved yet, so nothing syncs. Saying so is not
// optional: a member who believes their network is importing would wait
// forever for contacts that never arrive.
it("admits that nothing syncs yet and points at the path that works today", () => {
  renderAct();
  const note = screen.getByTestId("linkedin-pending");
  expect(note.textContent).toMatch(/awaiting approval/i);
  expect(note.textContent).toMatch(/Connections\.csv under Settings/i);
});

// Routing: LinkedIn sits between the recap and the inbox, and BOTH of its
// exits land on the inbox. An exit that skipped the inbox would leave a
// workspace with no mail flowing and no prompt to fix it.
it("hands off to the inbox act whether it is connected or skipped", () => {
  const atRecap = {
    ...initialConversationState,
    act: "results" as const,
    phase: "re.recap" as const,
  };
  const atLinkedIn = conversationReducer(atRecap, { type: "RESULTS_CONTINUE" });
  expect(atLinkedIn.act).toBe("linkedin");
  expect(atLinkedIn.phase).toBe("ln.why");

  for (const event of [
    { type: "LINKEDIN_CONNECTED" as const, profile: "https://x" },
    { type: "LINKEDIN_SKIPPED" as const },
  ]) {
    const next = conversationReducer(atLinkedIn, event);
    expect(next.act).toBe("connect");
    expect(next.phase).toBe("cn.consent");
  }
});
