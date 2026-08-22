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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { PersonComposer } from "./persondrawers";
import { installFetchStub, jsonResponse, meRoute } from "./story-utils";

// The project a message to a person belongs to: one choice, two effects. The
// draft request carries it so the server grounds the words in the person's
// page scoped to that project, and the send files the mail under it so the
// project's timeline sees the message. These tests assert the request bodies,
// because the wire is the contract.

type View = components["schemas"]["Person360"];

const PROJECTS = [
  {
    project_id: "proj-1",
    name: "ERP rollout",
    key: "ERP-27",
    phase: "delivering",
  },
  {
    project_id: "proj-2",
    name: "Datacentre migration",
    key: "DC-4",
    phase: "pursuing",
  },
];

function viewWith(projects: unknown[] | undefined): View {
  return {
    as_of: "2026-08-15T09:00:00Z",
    person: {
      id: "p-1",
      full_name: "Dana Buyer",
      emails: [
        {
          id: "pe-1",
          person_id: "p-1",
          email: "dana@brandt.example",
          email_type: "work",
          is_primary: true,
          position: 0,
          source: "manual",
          captured_by: "human:u1",
        },
      ],
      reachability: [],
    },
    activities: { data: [] },
    projects,
    sections_omitted: [],
  } as unknown as View;
}

function render(view: View) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const Wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
  return rtlRender(
    <PersonComposer
      personId="p-1"
      view={view}
      guard={undefined}
      open={true}
      onClose={() => {}}
    />,
    { wrapper: Wrapper },
  );
}

describe("the person composer's project attribution", () => {
  const bodies: { key: string; body: unknown }[] = [];
  beforeEach(() => {
    bodies.length = 0;
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /consent-purposes": () => jsonResponse({ data: [] }),
      "GET /channel-providers": () => jsonResponse({ data: [] }),
      "POST /people/p-1/draft-email": (body) => {
        bodies.push({ key: "draft", body });
        return jsonResponse({
          subject: "ERP-27 cutover",
          body: "About the cutover.",
          generated_by: "deterministic",
          reasoning: [],
        });
      },
    });
  });
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("offers no picker when the person is part of no project", () => {
    render(viewWith([]));
    expect(screen.queryByLabelText("Project")).toBeNull();
  });

  it("sends the chosen project on the draft request and shows what the draft is scoped to", async () => {
    const user = userEvent.setup();
    render(viewWith(PROJECTS));

    await pickOption(
      user,
      screen.getByLabelText("Project"),
      "DC-4 · Datacentre migration",
    );
    expect(screen.getByText("Scoped to DC-4")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Draft with AI" }));

    await waitFor(() => expect(bodies.length).toBe(1));
    expect(bodies[0].body).toEqual({ project_id: "proj-2" });
  });

  it("defaults to the person's only live project, visibly", () => {
    render(viewWith([PROJECTS[0]]));
    expect(screen.getByText("Scoped to ERP-27")).toBeTruthy();
  });
});
