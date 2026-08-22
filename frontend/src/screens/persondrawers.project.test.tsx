/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
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

// A guard that lets mail go out for the one purpose the stub offers, so a
// send reaches the wire and its links can be read.
const GUARD = {
  person_id: "p-1",
  entries: [
    {
      purpose_key: "transactional",
      purpose_class: "transactional",
      channel: "email",
      verdict: "allowed",
      reason: "contract",
    },
  ],
} as unknown as components["schemas"]["PersonConsentGuard"];

const PURPOSES = {
  data: [
    {
      id: "pu-1",
      key: "transactional",
      label: "Deal messages",
      requires_double_opt_in: false,
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
};

function viewWith(projects: unknown[] | undefined, personId = "p-1"): View {
  return {
    as_of: "2026-08-15T09:00:00Z",
    person: {
      id: personId,
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

function composer(view: View, personId = "p-1") {
  return (
    <PersonComposer
      personId={personId}
      view={view}
      guard={GUARD}
      open={true}
      onClose={() => {}}
    />
  );
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
  return rtlRender(composer(view), { wrapper: Wrapper });
}

// Fills and sends one mail. The body is a contentEditable surface, so it is
// fed by input event rather than by typing.
async function fillAndSend(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText("Subject"), "Hello");
  const body = screen.getByRole("textbox", { name: "Message" });
  body.innerHTML = "<p>Body content</p>";
  fireEvent.input(body);
  await pickOption(
    user,
    screen.getByLabelText("Consent purpose"),
    "Deal messages",
  );
  await user.click(screen.getByRole("button", { name: "Send" }));
}

describe("the person composer's project attribution", () => {
  const bodies: { key: string; body: unknown }[] = [];
  beforeEach(() => {
    bodies.length = 0;
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /consent-purposes": () => jsonResponse(PURPOSES),
      "GET /channel-providers": () => jsonResponse({ data: [] }),
      "POST /emails": (body) => {
        bodies.push({ key: "send", body });
        return jsonResponse({ id: "act-9" }, 202);
      },
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

  it("files the sent mail under the project the picker shows", async () => {
    const user = userEvent.setup();
    render(viewWith(PROJECTS));

    await pickOption(
      user,
      screen.getByLabelText("Project"),
      "ERP-27 · ERP rollout",
    );
    await fillAndSend(user);

    await waitFor(() =>
      expect(bodies.some((b) => b.key === "send")).toBe(true),
    );
    const sent = bodies.find((b) => b.key === "send")?.body as {
      links: unknown[];
    };
    // The selection passed at the click is what files the mail: person and
    // the chosen project, nothing else.
    expect(sent.links).toEqual([
      { entity_type: "person", entity_id: "p-1" },
      { entity_type: "project", entity_id: "proj-1" },
    ]);
  });

  it("drops the project when the composer is reused for another recipient", async () => {
    const user = userEvent.setup();
    const { rerender } = render(viewWith([PROJECTS[0]]));
    // The sole-project default has selected ERP-27 for p-1.
    expect(screen.getByText("Scoped to ERP-27")).toBeTruthy();

    // Same element, new recipient who is part of no project.
    rerender(composer(viewWith([], "p-2"), "p-2"));
    expect(screen.queryByText("Scoped to ERP-27")).toBeNull();
    await fillAndSend(user);

    await waitFor(() =>
      expect(bodies.some((b) => b.key === "send")).toBe(true),
    );
    const sent = bodies.find((b) => b.key === "send")?.body as {
      links: unknown[];
    };
    // A's project must not ride along on B's mail.
    expect(sent.links).toEqual([{ entity_type: "person", entity_id: "p-2" }]);
  });

  it("defaults to the person's only live project, visibly", () => {
    render(viewWith([PROJECTS[0]]));
    expect(screen.getByText("Scoped to ERP-27")).toBeTruthy();
  });
});
