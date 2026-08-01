// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
  Skeleton,
} from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";
import "./linkedin-review.css";

// The two halves of the LinkedIn tab that make the import worth doing
// (ADR-0078 §2.1b).
//
// The REVIEW QUEUE decides the matcher's middle tier. An exact email match
// confirms itself and an ambiguous name matches nothing, but LinkedIn exports
// an address only for the connections who allowed it, so almost every candidate
// arrives as a suggestion that needs a human. Before this queue existed the
// import card counted those suggestions and offered nowhere to act on them.
//
// The REACH TABLE is the answer the whole import is for: which accounts this
// member's network already reaches, and how many of those people are not yet
// contacts. It is also the half that works on a one-person workspace — asking
// which COLLEAGUE knows an account has no content when there is one member,
// while asking whether your own network reaches it has content immediately.

type LinkedInConnection = components["schemas"]["LinkedInConnection"];
type LinkedInReach = components["schemas"]["LinkedInReachResponse"];

const SUGGESTED_KEY = ["linkedin-connections", "suggested"] as const;
const REACH_KEY = ["linkedin-reach"] as const;

function useSuggestedMatches() {
  return useQuery({
    queryKey: SUGGESTED_KEY,
    queryFn: async (): Promise<LinkedInConnection[]> => {
      const { data, error } = await api.GET("/me/linkedin-connections", {
        params: { query: { match_status: "suggested" } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
}

// Both decisions invalidate the same two reads: a confirmation changes the
// queue AND the reach table's contacts-on-file count, and a rejection changes
// the queue. Invalidating both from one place keeps the tab from showing a
// decided row beside a stale count.
function useDecideMatch(verdict: "confirm" | "reject") {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      const path =
        verdict === "confirm"
          ? ("/me/linkedin-connections/{id}/confirm" as const)
          : ("/me/linkedin-connections/{id}/reject" as const);
      const { data, error } = await api.POST(path, {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: SUGGESTED_KEY });
      await client.invalidateQueries({ queryKey: REACH_KEY });
    },
  });
}

/** The matcher's suggestions, one decision each. */
export function LinkedInReviewCard() {
  const t = useT();
  const query = useSuggestedMatches();
  const confirm = useDecideMatch("confirm");
  const reject = useDecideMatch("reject");
  const deciding = confirm.isPending || reject.isPending;
  const rows = query.data ?? [];

  return (
    <section className="card li-review">
      <SectionHeader
        title={t("linkedinReview.title")}
        sub={t("linkedinReview.sub")}
      />
      {query.isPending && <Skeleton width="70%" />}
      {query.isError && <EmptyState>{query.error.message}</EmptyState>}
      {query.isSuccess && rows.length === 0 && (
        <EmptyState>{t("linkedinReview.empty")}</EmptyState>
      )}
      {(confirm.isError || reject.isError) && (
        <p role="alert" className="co-error">
          {(confirm.error ?? reject.error)?.message}
        </p>
      )}
      {rows.length > 0 && (
        <ul className="li-review-list" data-testid="linkedin-review-list">
          {rows.map((row) => (
            <li key={row.id}>
              <div className="li-review-who">
                <span className="li-review-name">{row.full_name}</span>
                {/* Position and employer are what a human judges the guess on.
                    The ORIGINAL strings, never the folded forms the matcher
                    compares — nobody can decide "andreas muller · simio". */}
                {(row.position || row.company_name) && (
                  <span className="t-caption">
                    {[row.position, row.company_name]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                )}
              </div>
              <div className="li-review-guess">
                {row.matched_person_name ? (
                  <Badge tone="accent">{row.matched_person_name}</Badge>
                ) : (
                  // A suggestion pointing at a contact this member cannot read.
                  // The row is shown so it can still be rejected; the record is
                  // not named, because that is what the person read closes.
                  <span className="t-caption">
                    {t("linkedinReview.hiddenContact")}
                  </span>
                )}
              </div>
              <div className="li-review-actions">
                <Button
                  small
                  variant="primary"
                  disabled={deciding || !row.matched_person_id}
                  onClick={() => confirm.mutate(row.id)}
                >
                  {t("linkedinReview.confirm")}
                </Button>
                <Button
                  small
                  disabled={deciding}
                  onClick={() => reject.mutate(row.id)}
                >
                  {t("linkedinReview.reject")}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

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
