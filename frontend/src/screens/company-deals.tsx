// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { throwProblem } from "./common";

// The account's deals, ALL of them.
//
// Deliberately its own read rather than the 360's `deals` section: that
// section carries the OPEN deals and reports the closed ones as two totals
// (`won_lifetime`, `lost_count`). A tab built on it would list the open deals
// under a heading promising every deal, which is the kind of quiet omission
// this page exists not to make.
//
// The table that consumes this is unbuilt: main has since redesigned the
// table surface (#347) and rebuilt this page (#602, #747), so the view layer
// belongs on that foundation rather than this branch's. The read is kept
// because the reason for it survives either design.
export function useAccountDeals(orgId: string) {
  return useInfiniteQuery({
    queryKey: ["deals", "account", orgId],
    // A typed local rather than a bare undefined: an inferred TPageParam of
    // `undefined` rejects the string cursor getNextPageParam returns, and the
    // whole query's data type collapses to unknown.
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/deals", {
        params: {
          query: {
            organization_id: orgId,
            sort: "-updated_at",
            limit: 50,
            cursor: pageParam,
          },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? undefined,
  });
}
