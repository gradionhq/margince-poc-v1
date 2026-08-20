// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { DataTable, EmptyState, Skeleton } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import "./linkedin-reach.css";

// Which accounts a member's imported network reaches (ADR-0078 §2.1b) — the
// answer the whole import is for.
//
// It is the half that works on a one-person workspace: asking which COLLEAGUE
// knows an account has no content when there is one member, while asking
// whether your own network reaches it has content immediately.
//
// The card holds ONE report and no decision, so it draws no `SettingList`: a
// row exists to put an answer at the same x as the answers above and below it,
// and there is nothing here to line up against. A single row whose label
// repeated the card's own title would be noise.

type LinkedInReach = components["schemas"]["LinkedInReachResponse"];
type ReachAccount = LinkedInReach["accounts"][number];

// The largest page the contract's `limit` admits, asked for explicitly. This
// view has no cursor — the endpoint declares no cursor parameter and the
// response carries none — so ONE page is the whole of what a reader can reach,
// which makes where the list stops this screen's stated choice rather than the
// server's unnamed default of 50. The footnote reads `accounts_total`, computed
// over everything, so what sits past the edge is still counted out loud.
const REACH_PAGE_LIMIT = 200;

const REACH_KEY = ["linkedin-reach", REACH_PAGE_LIMIT] as const;

function useLinkedInReach() {
  return useQuery({
    queryKey: REACH_KEY,
    queryFn: async (): Promise<LinkedInReach> => {
      const { data, error } = await api.GET("/me/linkedin-reach", {
        params: { query: { limit: REACH_PAGE_LIMIT } },
      });
      if (error) {
        throwProblem(error);
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
    // No per-card bottom margin: the tab owns the rhythm between its cards.
    <Panel title={t("linkedinReach.title")}>
      <PanelBody>
        <p className="t-caption">{t("linkedinReach.sub")}</p>
        {query.isPending && <Skeleton width="70%" />}
        {/* A failed read is not an empty one. EmptyState drew "no accounts
            reached" chrome around the server's own refusal, so a read nobody
            managed to make looked exactly like a network that reaches nobody. */}
        {query.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(query.error, t)}
          </Callout>
        )}
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
            <div className="li-reach" data-testid="linkedin-reach-table">
              <ReachTable accounts={accounts} />
            </div>
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
      </PanelBody>
    </Panel>
  );
}

// DataTable, not a hand-rolled <table>: this sheet used to declare its own
// header type, its own cell padding and its own row rule, none of which agreed
// with the tables on the neighbouring cards. It also carried `display: block`
// on the table itself to make it scroll, which is the one shape where a header
// row can come apart from the figures it names — DataTable's own
// `.table-scroll` wrapper scrolls the whole table instead, so that cannot
// happen.
//
// Every cell stays `white-space: nowrap` (`.li-reach-cell`): an account name
// plus two counts is wider than a phone, so the table takes the sideways scroll
// and the page does not. Nowrap lives on spans this file owns rather than on
// `.table td`, because a screen sheet reaching into a primitive's internals is
// a second author for a rhythm the design system owns.
function ReachTable({
  accounts,
}: Readonly<{ accounts: readonly ReachAccount[] }>) {
  const t = useT();
  return (
    <DataTable
      columns={[
        {
          key: "account",
          header: t("linkedinReach.account"),
          render: (account: ReachAccount) => (
            <a
              className="li-reach-cell li-reach-link"
              href={`#/companies/${account.organization_id}`}
            >
              {account.display_name}
            </a>
          ),
        },
        {
          key: "connections",
          header: t("linkedinReach.connections"),
          render: (account: ReachAccount) => (
            <span className="t-mono li-reach-cell li-reach-figure">
              {account.connections}
            </span>
          ),
        },
        {
          key: "onFile",
          // The GAP is the finding: people you know there who are not
          // contacts. Rendering only the total would hide it.
          header: t("linkedinReach.onFile"),
          render: (account: ReachAccount) => (
            <span className="t-mono li-reach-cell li-reach-figure">
              {t("linkedinReach.onFileOf", {
                onFile: account.contacts_on_file,
                total: account.connections,
              })}
            </span>
          ),
        },
      ]}
      rows={[...accounts]}
      rowKey={(account) => account.organization_id}
    />
  );
}
