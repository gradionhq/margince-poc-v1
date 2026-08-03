// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { EmptyState, SectionHeader, Skeleton } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";
import "./linkedin-reach.css";

// Which accounts a member's imported network reaches (ADR-0078 §2.1b) — the
// answer the whole import is for.
//
// It is the half that works on a one-person workspace: asking which COLLEAGUE
// knows an account has no content when there is one member, while asking
// whether your own network reaches it has content immediately.

type LinkedInReach = components["schemas"]["LinkedInReachResponse"];

const REACH_KEY = ["linkedin-reach"] as const;

function useLinkedInReach() {
  return useQuery({
    queryKey: REACH_KEY,
    queryFn: async (): Promise<LinkedInReach> => {
      const { data, error } = await api.GET("/me/linkedin-reach", {
        params: { query: {} },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

/** Which accounts this member's imported network reaches. */
export function LinkedInReachCard() {
  const t = useT();
  const query = useLinkedInReach();
  const accounts = query.data?.accounts ?? [];

  return (
    <section className="card li-reach">
      <SectionHeader
        title={t("linkedinReach.title")}
        sub={t("linkedinReach.sub")}
      />
      {query.isPending && <Skeleton width="70%" />}
      {query.isError && <EmptyState>{query.error.message}</EmptyState>}
      {/* The unresolved count is reported whether or not anything resolved,
          and it matters MOST when nothing did: a fresh workspace that imported
          five thousand connections and matched no account should see the five
          thousand, not just "none yet". Hiding it behind a non-empty list hid
          the number in exactly the case it explains. */}
      {query.isSuccess && accounts.length === 0 && (
        <>
          <EmptyState>{t("linkedinReach.empty")}</EmptyState>
          {(query.data?.unresolved_connections ?? 0) > 0 && (
            <p className="co-muted">
              {t("linkedinReach.allUnresolved", {
                unresolved: query.data?.unresolved_connections ?? 0,
              })}
            </p>
          )}
        </>
      )}
      {accounts.length > 0 && (
        <>
          <table className="li-reach-table" data-testid="linkedin-reach-table">
            <thead>
              <tr>
                <th>{t("linkedinReach.account")}</th>
                <th>{t("linkedinReach.connections")}</th>
                <th>{t("linkedinReach.onFile")}</th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((account) => (
                <tr key={account.organization_id}>
                  <td>
                    <a href={`#/companies/${account.organization_id}`}>
                      {account.display_name}
                    </a>
                  </td>
                  <td className="t-mono">{account.connections}</td>
                  {/* The GAP is the finding: people you know there who are not
                      contacts. Rendering only the total would hide it. */}
                  <td className="t-mono">
                    {t("linkedinReach.onFileOf", {
                      onFile: account.contacts_on_file,
                      total: account.connections,
                    })}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {/* What this view cannot show, said out loud. A truncated list read
              as the whole network would understate reach, and the unresolved
              count is the number that shrinks as accounts are created. */}
          <p className="co-muted">
            {t("linkedinReach.footnote", {
              shown: accounts.length,
              total: query.data?.accounts_total ?? accounts.length,
              unresolved: query.data?.unresolved_connections ?? 0,
            })}
          </p>
        </>
      )}
    </section>
  );
}
