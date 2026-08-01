/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import {
  clearPendingAuthorize,
  readPendingAuthorize,
  stashPendingAuthorize,
} from "./pendingauthorize";
import { ResumeConnectBanner } from "./resumeconnectbanner";

// The component navigates with `globalThis.location.assign` (the same seam
// connectors.tsx's reconnect uses) rather than `location.href =` directly, so
// a test can intercept it without jsdom attempting a real navigation — the
// pattern this repo already uses elsewhere for the same reason. useT() falls
// back to its context's DEFAULT_LOCALE ("en") with no Provider mounted, so
// this banner needs none.
function stubAssign(assigned: string[]) {
  vi.stubGlobal("location", {
    ...globalThis.location,
    assign: (url: string) => assigned.push(url),
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  clearPendingAuthorize();
});

it("offers to resume the connection the human left to mint a passport for", async () => {
  stashPendingAuthorize({
    url: "/oauth/authorize?client_id=x&scope=read",
    clientName: "Claude Code",
  });
  render(<ResumeConnectBanner />);
  expect(screen.getByText(/finish connecting claude code/i)).toBeTruthy();
});

// I9: no stash, no banner. A banner offering to resume nothing is an
// invitation to a confusing 403 at the end of a flow that was never pending.
it("renders nothing when no connection is pending", () => {
  clearPendingAuthorize();
  const { container } = render(<ResumeConnectBanner />);
  expect(container.textContent).toBe("");
});

// Re-entering /oauth/authorize is the point: the server re-validates and
// arms a FRESH nonce, so a human who took longer than the 300s TTL to mint
// still lands on a live request rather than a dead one.
it("re-enters the authorize endpoint rather than returning to the consent screen", async () => {
  const assigned: string[] = [];
  stubAssign(assigned);
  stashPendingAuthorize({
    url: "/oauth/authorize?client_id=x&scope=read",
    clientName: "Claude Code",
  });
  render(<ResumeConnectBanner />);
  await userEvent.click(
    screen.getByRole("button", { name: /continue connecting/i }),
  );
  expect(assigned).toEqual(["/oauth/authorize?client_id=x&scope=read"]);
  expect(readPendingAuthorize()).toBeNull();
});

it("clears the pending connection when the human cancels it", async () => {
  stashPendingAuthorize({
    url: "/oauth/authorize?client_id=x&scope=read",
    clientName: "Claude Code",
  });
  render(<ResumeConnectBanner />);
  await userEvent.click(
    screen.getByRole("button", { name: /cancel this connection/i }),
  );
  expect(readPendingAuthorize()).toBeNull();
  expect(screen.queryByText(/finish connecting/i)).toBeNull();
});
