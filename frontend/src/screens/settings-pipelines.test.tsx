/** @vitest-environment jsdom */
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { PipelinesCard } from "./settings";
import { jsonResponse, PIPELINE_ADMIN, render } from "./settings.testkit";

// The pipeline and stage editor the Data model entry carries. Each affordance
// follows its OWN verb — create, update, delete — so the cases below grant one
// at a time and read the card directly rather than through the entry that hosts
// it.

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

// Routed by URL, with the pipelines list stubbed to the D-8 shape (an array
// with embedded stages) and a POST /stages hook so a test can inspect the exact
// body shipped.
function settingsStub(opts: {
  roles: string[];
  allow?: GrantSpec;
  onStagePost?: (body: unknown) => void;
  onStageDelete?: (url: string) => void;
  // What the server answers a removal with, when the scenario is about a
  // refusal: a stage still holding deals, or the terminal pair.
  stageDeleteRefusal?: { status: number; body: unknown };
}) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    const method = input instanceof Request ? input.method : "GET";
    if (url.endsWith("/v1/me")) {
      return jsonResponse(
        meFixture({ roles: opts.roles, allow: opts.allow ?? PIPELINE_ADMIN }),
      );
    }
    if (url.includes("/pipelines")) {
      return jsonResponse({
        data: [
          {
            id: "pl",
            name: "Sales",
            is_default: true,
            position: 0,
            stages: [
              {
                id: "s1",
                pipeline_id: "pl",
                name: "Qualify",
                position: 1,
                semantic: "open",
                win_probability: 20,
              },
            ],
          },
        ],
        page: { next_cursor: null },
      });
    }
    if (url.includes("/stages") && method === "POST") {
      const raw = input instanceof Request ? await input.clone().text() : "";
      const body = raw ? JSON.parse(raw) : {};
      opts.onStagePost?.(body);
      return jsonResponse(body);
    }
    if (url.includes("/stages/") && method === "DELETE") {
      opts.onStageDelete?.(url);
      if (opts.stageDeleteRefusal) {
        return jsonResponse(
          opts.stageDeleteRefusal.body,
          opts.stageDeleteRefusal.status,
        );
      }
      return new Response(null, { status: 204 });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

describe("PipelinesCard", () => {
  it("shows create controls for an admin", async () => {
    vi.stubGlobal("fetch", settingsStub({ roles: ["admin"] }));
    render(<PipelinesCard />);
    expect(await screen.findByText("New pipeline")).toBeTruthy();
  });
  it("hides create controls for a rep", async () => {
    vi.stubGlobal("fetch", settingsStub({ roles: ["rep"], allow: {} }));
    render(<PipelinesCard />);
    await screen.findByText("Sales");
    expect(screen.queryByText("New pipeline")).toBeNull();
  });
  // One grant at a time: create and update govern different controls, and a
  // fixture holding both cannot tell a correct binding from a transposed one.
  it("offers stage editing on update alone, without the create affordance", async () => {
    vi.stubGlobal(
      "fetch",
      settingsStub({ roles: ["admin"], allow: { pipeline: ["update"] } }),
    );
    render(<PipelinesCard />);
    await screen.findByText("Sales");
    expect(screen.getByTestId("new-stage-pl")).toBeTruthy();
    expect(screen.queryByText("New pipeline")).toBeNull();
  });

  it("offers the create affordance on create alone, without stage editing", async () => {
    vi.stubGlobal(
      "fetch",
      settingsStub({ roles: ["admin"], allow: { pipeline: ["create"] } }),
    );
    render(<PipelinesCard />);
    expect(await screen.findByText("New pipeline")).toBeTruthy();
    expect(screen.queryByTestId("new-stage-pl")).toBeNull();
  });

  // Removal is pipeline:delete, a different verb from everything else on
  // this card — a principal who may add and rename stages must not be
  // shown a control the server would only ever 403.
  it("withholds stage removal from a principal holding update alone", async () => {
    vi.stubGlobal(
      "fetch",
      settingsStub({
        roles: ["admin"],
        allow: { pipeline: ["read", "update"] },
      }),
    );
    render(<PipelinesCard />);
    expect(await screen.findByTestId("new-stage-pl")).toBeTruthy();
    expect(screen.queryByTestId("remove-stage-s1")).toBeNull();
  });

  it("removes a stage through DELETE once the confirm is taken", async () => {
    const deleted: string[] = [];
    vi.stubGlobal(
      "fetch",
      settingsStub({
        roles: ["admin"],
        allow: { pipeline: ["read", "update", "delete"] },
        onStageDelete: (url) => deleted.push(url),
      }),
    );
    render(<PipelinesCard />);
    await userEvent.click(await screen.findByTestId("remove-stage-s1"));
    // The dialog names the stage, so the confirm is about a stage the
    // reader recognises rather than "this one".
    expect(screen.getByText(/leaves the pipeline/).textContent).toContain(
      "Qualify",
    );
    await userEvent.click(screen.getByRole("button", { name: "Remove stage" }));
    await waitFor(() => expect(deleted).toHaveLength(1));
    expect(deleted[0]).toContain("/stages/s1");
  });

  // The refusal is the server's, and it names the deals in the way. The
  // dialog stays open showing it: a closed dialog would drop the only
  // sentence telling the admin what to move.
  it("shows the occupied-stage refusal and keeps the stage", async () => {
    vi.stubGlobal(
      "fetch",
      settingsStub({
        roles: ["admin"],
        allow: { pipeline: ["read", "update", "delete"] },
        stageDeleteRefusal: {
          status: 422,
          // Exactly what the server sends: a MessageFault renders a
          // machine code and a reason, and no per-field details body —
          // a fixture that invented one would document a contract the
          // backend does not have.
          body: {
            type: "https://errors.gradion.com/stage_occupied",
            title: "Unprocessable Entity",
            status: 422,
            code: "stage_occupied",
            detail:
              "1 deal(s) still sit on this stage: Acme rollout. Move them to another stage first.",
          },
        },
      }),
    );
    render(<PipelinesCard />);
    await userEvent.click(await screen.findByTestId("remove-stage-s1"));
    await userEvent.click(screen.getByRole("button", { name: "Remove stage" }));
    expect(await screen.findByText(/Acme rollout/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove stage" })).toBeTruthy();
  });

  it("create stage posts the pipeline_id + semantic + win_probability", async () => {
    const posts: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      settingsStub({ roles: ["admin"], onStagePost: (b) => posts.push(b) }),
    );
    render(<PipelinesCard />);
    await userEvent.click(await screen.findByTestId("new-stage-pl"));
    await userEvent.type(screen.getByLabelText(/Name/), "Discovery");
    await userEvent.type(screen.getByLabelText(/Win probability/), "15");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() =>
      expect(posts[0]).toMatchObject({
        pipeline_id: "pl",
        semantic: "open",
        win_probability: 15,
      }),
    );
  });
});
