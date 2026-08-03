/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { TagAction } from "./companyactions";

// How a typed tag name resolves to ONE tag. The interesting behaviour is the
// request sequence — which reads happen before the write, and what the action
// does with the collisions the server can answer with — so these drive the
// real component and assert on the calls it makes.

type Call = { method: string; path: string };

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

const page = { has_more: false };

/** stub records every call and answers from `reply`. */
function stub(reply: (call: Call, nth: number) => Response) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const call = {
        method: request.method,
        path: new URL(request.url).pathname,
      };
      calls.push(call);
      return reply(call, calls.length);
    }),
  );
  return calls;
}

function Wrapper({ children }: Readonly<{ children: ReactNode }>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider>{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

async function addTagNamed(name: string) {
  render(
    <Wrapper>
      <TagAction orgId="o-1" />
    </Wrapper>,
  );
  await userEvent.click(screen.getByRole("button", { name: "Add tag" }));
  await userEvent.type(screen.getByRole("textbox"), name);
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Add tag resolves a typed name to one tag", () => {
  it("reads the catalog at submit, so a retry reuses what the last attempt made", async () => {
    // The tag exists because a previous attempt created it and then failed to
    // apply it. Nothing the component loaded on mount knows that, so a
    // resolution reading a snapshot would create a second "Landlord".
    const calls = stub((call) => {
      if (call.path.endsWith("/tags") && call.method === "GET") {
        return json({ data: [{ id: "t-1", name: "Landlord" }], page });
      }
      return json({ id: "tg-1" }, 201);
    });

    await addTagNamed("landlord");

    // No POST /tags at all: the existing tag was matched, case-insensitively.
    expect(
      calls.some((c) => c.method === "POST" && c.path.endsWith("/tags")),
    ).toBe(false);
    expect(calls.some((c) => c.path.includes("/apply"))).toBe(true);
  });

  it("resolves a create-time collision by reading the winner back", async () => {
    // Somebody created the same name between this action's read and its write.
    // tag names are unique per workspace, so their row IS the asked-for tag.
    let seenTags = [{ id: "t-1", name: "Other" }];
    const calls = stub((call) => {
      if (call.path.endsWith("/tags") && call.method === "GET") {
        return json({ data: seenTags, page });
      }
      if (call.path.endsWith("/tags") && call.method === "POST") {
        seenTags = [...seenTags, { id: "t-9", name: "Landlord" }];
        return json({ title: "conflict" }, 409);
      }
      return json({ id: "tg-1" }, 201);
    });

    await addTagNamed("Landlord");

    // Read, failed create, read again, then apply the winner.
    expect(
      calls.filter((c) => c.method === "GET" && c.path.endsWith("/tags")),
    ).toHaveLength(2);
    expect(calls.at(-1)?.path).toContain("/tags/t-9/apply");
  });

  it("treats an already-applied tag as the state the rep asked for", async () => {
    const calls = stub((call) => {
      if (call.path.endsWith("/tags") && call.method === "GET") {
        return json({ data: [{ id: "t-1", name: "Landlord" }], page });
      }
      return json({ title: "conflict" }, 409);
    });

    await addTagNamed("Landlord");

    // The apply collided and the action did NOT surface it: the tag is on the
    // company, which is what was asked for.
    expect(calls.some((c) => c.path.includes("/apply"))).toBe(true);
    expect(screen.queryByText(/conflict/i)).toBeNull();
  });
});
