// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { type Mock, vi } from "vitest";
import type { components } from "../api/schema";
import { meFixture } from "../app/mefixture";
import { emptyPage, jsonResponse } from "./company.fixtures";
import { ORG, project, project360 } from "./projects.fixtures";

type Project = components["schemas"]["Project"];
type Project360 = components["schemas"]["Project360"];

/**
 * The fetch stub for the project surfaces. `respond` answers first and
 * returns null to fall through; the defaults answer /me, the roster, the
 * company list, the project list and the 360.
 */
export function projectsBackend(
  opts: Readonly<{
    projects?: Project[];
    view?: Project360;
    respond?: (
      url: string,
      method: string,
      request: Request,
    ) => Promise<Response | null>;
  }> = {},
): {
  fetchMock: Mock<(request: Request) => Promise<Response>>;
  urls: string[];
} {
  const urls: string[] = [];
  const fetchMock = vi.fn(async (request: Request) => {
    urls.push(request.url);
    const answered = await opts.respond?.(request.url, request.method, request);
    return answered ?? quietDefault(request, opts);
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, urls };
}

// The reads the list and the page fire on every render, answered from the
// fixtures above. A GET the table does not match is an empty page.
function quietDefault(
  request: Request,
  opts: Readonly<{ projects?: Project[]; view?: Project360 }>,
): Response {
  const pathname = new URL(request.url).pathname;
  const rows = opts.projects ?? [project()];
  const listPage = { next_cursor: null, has_more: false };
  const exact: Record<string, () => Response> = {
    "/v1/me": () => jsonResponse(meFixture()),
    "/v1/organizations": () => jsonResponse({ data: [ORG], page: listPage }),
    [`/v1/organizations/${ORG.id}`]: () => jsonResponse(ORG),
    "/v1/projects": () => jsonResponse({ data: rows, page: listPage }),
  };
  const matched = Object.hasOwn(exact, pathname) ? exact[pathname] : undefined;
  if (matched) {
    return matched();
  }
  if (pathname.endsWith("/360")) {
    return jsonResponse(opts.view ?? project360());
  }
  const single = /\/projects\/([^/]+)$/.exec(pathname);
  if (single) {
    const found = rows.find((row) => row.id === single[1]);
    return found ? jsonResponse(found) : jsonResponse({ title: "gone" }, 404);
  }
  return emptyPage();
}
