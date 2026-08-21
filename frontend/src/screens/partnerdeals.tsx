import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, DataTable, EmptyState } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import { QueryGate, throwProblem } from "./common";
import { dealStatusTone } from "./deals";
import { EntityRef } from "./entityref";

// The deals this partner brought us, on the partner's own company page.
//
// A partner's deals belong to the CUSTOMER, not to the partner, so the account
// page's own Deals tab — which lists deals where this company is the customer —
// is silent about them by construction. Without this panel a partner's page
// showed nothing of the work they had done until a deal was won and had earned
// money, and an open partner-sourced deal was invisible on the one page a
// reader goes to for the partner relationship.
//
// It sits above the commission ledger for that reason: the pipeline is what
// exists, the ledger is what it has paid.

type Deal = components["schemas"]["Deal"];

/**
 * Every deal attributed to this partner, followed page by page.
 *
 * Stopping at page one would under-report a productive partner silently, which
 * is the same failure the commission ledger walks its cursor to avoid. The
 * server filters on `partner_org_id`, so `sourced` and `influenced` both land
 * here — a partner's page should show the deals they helped with, not only the
 * ones that earn them money, and the attribution column says which is which.
 */
async function fetchPartnerDeals(organizationId: string): Promise<Deal[]> {
  const deals: Deal[] = [];
  let cursor: string | undefined;
  do {
    const { data, error } = await api.GET("/deals", {
      params: { query: { partner_org_id: organizationId, limit: 50, cursor } },
    });
    if (error) {
      throwProblem(error);
    }
    deals.push(...(data?.data ?? []));
    cursor = data?.page?.has_more
      ? (data.page.next_cursor ?? undefined)
      : undefined;
  } while (cursor);
  return deals;
}

export function PartnerDeals({
  organizationId,
}: Readonly<{ organizationId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const query = useQuery({
    queryKey: ["partner-deals", organizationId],
    queryFn: () => fetchPartnerDeals(organizationId),
  });

  return (
    <Panel
      title={t("partnerDeals.panelTitle")}
      sub={t("partnerDeals.panelSub")}
    >
      <QueryGate query={query}>
        {(deals) => (
          <PanelBody>
            {deals.length === 0 ? (
              <EmptyState>{t("partnerDeals.none")}</EmptyState>
            ) : (
              <SourcedDeals deals={deals} locale={locale} />
            )}
          </PanelBody>
        )}
      </QueryGate>
    </Panel>
  );
}

function SourcedDeals({
  deals,
  locale,
}: Readonly<{ deals: Deal[]; locale: Locale }>) {
  const t = useT();
  return (
    <div data-testid="partner-deals">
      <DataTable
        // The scroller a wide table needs is a named region, so it reuses the
        // heading already above it rather than inventing a second name for the
        // same thing.
        label={t("partnerDeals.panelTitle")}
        rows={deals}
        rowKey={(deal) => deal.id}
        columns={[
          {
            key: "deal",
            header: t("partnerDeals.column.deal"),
            render: (deal) => (
              <EntityRef kind="deal" id={deal.id} name={deal.name} />
            ),
          },
          {
            // The whole point of the panel: a partner brings a deal FOR another
            // company, so the customer is the fact that makes the row make
            // sense. A withheld organization renders as the reference EntityRef
            // gives any unreadable record rather than going blank.
            key: "customer",
            header: t("partnerDeals.column.customer"),
            render: (deal) => (
              <EntityRef kind="organization" id={deal.organization_id} />
            ),
          },
          {
            key: "attribution",
            header: t("partnerDeals.column.attribution"),
            render: (deal) =>
              t(
                deal.partner_attribution === "influenced"
                  ? "deal.attributionInfluenced"
                  : "deal.attributionSourced",
              ),
          },
          {
            key: "amount",
            header: t("partnerDeals.column.amount"),
            // A masked amount is null and stays blank: the row still names the
            // deal and its customer, which is what this panel is for.
            render: (deal) =>
              deal.amount_minor != null && deal.currency
                ? formatMoney(deal.amount_minor, deal.currency, locale)
                : "—",
          },
          {
            key: "status",
            header: t("partnerDeals.column.status"),
            render: (deal) => (
              <Badge tone={dealStatusTone(deal.status)} quiet>
                {deal.status}
              </Badge>
            ),
          },
        ]}
      />
    </div>
  );
}
