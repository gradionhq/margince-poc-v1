// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { DataTable, EmptyState, Skeleton } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
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
// The report is the card's SUBJECT rather than an answer to a question, so it
// takes a stacked `SettingRow`: full width under its own naming, at the same x
// as the naming column of every other card on this page. The row is what makes
// the caveat land in the right place — see the footnote comment below.

type LinkedInReach = components["schemas"]["LinkedInReachResponse"];
type ReachAccount = LinkedInReach["accounts"][number];

// The largest page the contract's `limit` admits, asked for explicitly. This
// view has no cursor — the endpoint declares no cursor parameter and the
// response carries none — so ONE page is the whole of what a reader can reach,
// which makes where the list stops this screen's stated choice rather than the
// server's unnamed default of 50. The row's caveat reads `accounts_total`,
// computed over everything, so what sits past the edge is still counted out
// loud.
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

/**
 * The row's caveat: what this view cannot show, in the terms the state calls
 * for.
 *
 * Two sentences, never both — a reader looking at a list needs to know where it
 * stops, and a reader looking at an empty one needs to know that the
 * connections exist and simply match nothing on file yet. Neither is a
 * footnote: they qualify the figures, so they read before them.
 *
 * Returns `undefined` — not an empty string — when there is nothing to say, so
 * the row draws no description element and the control points at no
 * `aria-describedby` that is not on the page.
 */
function reachCaveat(
  t: ReturnType<typeof useT>,
  reach: LinkedInReach | undefined,
  shown: number,
): string | undefined {
  const unresolved = reach?.unresolved_connections ?? 0;
  if (shown === 0) {
    return unresolved > 0
      ? t("linkedinReach.allUnresolved", { unresolved })
      : undefined;
  }
  return t("linkedinReach.footnote", {
    shown,
    total: reach?.accounts_total ?? shown,
    unresolved,
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
        <p className="settings-panel-sub">{t("linkedinReach.sub")}</p>
        {query.isPending && <Skeleton width="70%" />}
        {/* A failed read is not an empty one. EmptyState drew "no accounts
            reached" chrome around the server's own refusal, so a read nobody
            managed to make looked exactly like a network that reaches nobody. */}
        {query.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(query.error, t)}
          </Callout>
        )}
        {/* ONE row, whether the network reaches anybody or not. The empty state
            used to stand outside the list, where `.empty` draws its
            page-furniture slab — 90px of grey saying one sentence, the loudest
            thing on a card whose other lines are one deep. Inside a stacked row
            the primitive caps it at a row's own interval and reads left-aligned
            (settingrow.css), and the answer lines up under the same naming the
            table gets. */}
        {query.isSuccess && (
          <SettingList>
            <SettingRow
              testId="linkedin-reach-table"
              layout="stack"
              label={t("linkedinReach.accountsLabel")}
              // What this view cannot show, said out loud — and said ABOVE the
              // figures rather than under them. It was a footnote, which is
              // where a caveat arrives after the reader has already believed
              // the list: a truncated list read as the whole network
              // understates reach, and the unresolved count is the number that
              // shrinks as accounts are created. The row's description is the
              // slot for exactly this, and it is the same ordering
              // `SurfaceState` makes for a stale read.
              //
              // The unresolved count is reported whether or not anything
              // resolved, and it matters MOST when nothing did: a fresh
              // workspace that imported five thousand connections and matched
              // no account should see the five thousand, not just "none yet".
              description={reachCaveat(t, query.data, accounts.length)}
              // `.settingrow-measure` is what lets the table's own
              // `.table-scroll` box scroll: `.settingrow-control` is a flex row,
              // so a child with the default `min-width: auto` would grow to the
              // table's full width and push the card sideways instead.
              control={
                accounts.length === 0 ? (
                  <EmptyState>
                    <p className="t-small">{t("linkedinReach.empty")}</p>
                  </EmptyState>
                ) : (
                  <div className="settingrow-measure">
                    <ReachTable accounts={accounts} />
                  </div>
                )
              }
            />
          </SettingList>
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
      label={t("linkedinReach.accountsLabel")}
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
