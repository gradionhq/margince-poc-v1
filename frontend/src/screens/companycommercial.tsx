import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { navigate } from "../app/router";
import { Badge } from "../design-system/atoms";
import { formatDate, formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import { throwProblem } from "./common";
import { RECORD_ZONE, SectionCard, sectionState } from "./company360";

// The commercial relationship: what is running with this account, and what we
// last put in front of them.
//
// TWO BLOCKS, NOT FOUR. The mockup draws an active contract with its value and
// number, and a renewal countdown beside it. Margince has no concept of a
// contract: nothing stores a contract value, a contract number or a renewal
// date, so both blocks would have to be invented. Deriving them from an
// accepted offer was considered and refused — an accepted offer is not a
// signed contract, it carries no renewal date, and a card that calls one the
// other is the kind of small lie that costs the reader trust in every other
// figure on the page.
//
// Contracts as a real record is worth building and is its own feature; it is
// raised upstream rather than faked here.

type Organization360 = components["schemas"]["Organization360"];
type Deal = NonNullable<Organization360["deals"]>["data"][number];
type Offer = components["schemas"]["Offer"];

export function CompanyCommercialCard({
  view,
}: Readonly<{ view?: Organization360 }>) {
  const t = useT();
  const deals = view?.deals?.data ?? [];
  const state = sectionState(view, "deals", Boolean(view?.deals), deals.length);
  // Both this card and the deals card read the SAME section, so both would
  // otherwise print the same "hidden from you" line. One notice is the answer;
  // two teach the reader that the page repeats itself. The deals card carries
  // it, because it is the one that names the section.
  if (state === "withheld" || state === "unavailable") {
    return null;
  }
  return (
    <SectionCard
      title={t("commercial.title")}
      state={state}
      emptyLabel={t("commercial.noneOpen")}
    >
      <OpenOpportunities deals={deals} />
      <LastOffer
        deals={deals}
        truncated={view?.deals?.page?.has_more === true}
      />
    </SectionCard>
  );
}

function OpenOpportunities({ deals }: Readonly<{ deals: readonly Deal[] }>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <div className="com-block">
      <span className="t-caption">{t("commercial.openOpportunities")}</span>
      <ul className="co-list">
        {deals.map((deal) => (
          <li key={deal.deal_id} className="co-row">
            <button
              type="button"
              className="co-rowlink"
              onClick={() => navigate({ screen: "deals", id: deal.deal_id })}
            >
              {deal.name}
            </button>
            <span className="co-row-meta">
              {deal.stage_name && <Badge>{deal.stage_name}</Badge>}
              {/* An unpriced deal says so rather than showing nothing: the
                  gap between "nobody has costed this" and "this line has no
                  amount column" is the whole point. */}
              <span>
                {dealAmount(deal, locale) ?? t("commercial.unpriced")}
              </span>
              {deal.expected_close_date && (
                <span>
                  {t("commercial.closes", {
                    when: formatDate(
                      deal.expected_close_date,
                      locale,
                      RECORD_ZONE,
                    ),
                  })}
                </span>
              )}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function dealAmount(
  deal: Deal,
  locale: ReturnType<typeof useLocale>["locale"],
): string | undefined {
  // Both halves or nothing. An amount with no currency cannot be rendered
  // without picking one, and a guessed symbol on a real figure is worse than
  // no figure.
  if (deal.amount?.amount_minor == null || !deal.amount.currency) {
    return undefined;
  }
  return formatMoney(deal.amount.amount_minor, deal.amount.currency, locale);
}

// The last offer we put in front of them.
//
// Read off the account's leading open deal, because an offer hangs off a deal
// rather than off a company: there is no account-wide offer read, and the
// alternative — one request per open deal — would cost a page load to answer
// a single line. The deal it came from is NAMED, so a reader can tell which
// offer they are looking at rather than assuming it is the only one.
function LastOffer({
  deals,
  truncated,
}: Readonly<{ deals: readonly Deal[]; truncated: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  // Offers are their own RBAC object: a reader who may see deals may not see
  // what we quoted. Without this the request is fired to be refused, and the
  // refusal renders as "no offer" — which is a claim about the account rather
  // than about the reader's grants.
  const mayRead = useCan("offer", "read");
  const leading = leadingDeal(deals, truncated);
  const offers = useQuery({
    // A DISTINCT key. The deal screen caches its own full offer list under
    // ["deal-offers", id]; sharing it would let this one-row response stand in
    // for that list and leave the deal screen showing a single offer.
    queryKey: ["deal-latest-offer", leading?.deal_id],
    enabled: Boolean(leading) && mayRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/offers", {
        params: { path: { id: leading?.deal_id ?? "" }, query: { limit: 1 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const offer = offers.data?.data?.[0];
  if (!leading || !offer) {
    return null;
  }
  return (
    <div className="com-block">
      <span className="t-caption">
        {t("commercial.lastOffer", { deal: leading.name })}
      </span>
      <span className="co-row-meta">
        <button
          type="button"
          className="co-rowlink"
          onClick={() => navigate({ screen: "deals", id: leading.deal_id })}
        >
          {offer.offer_number ?? t("commercial.offerUnnumbered")}
        </button>
        <Badge tone={OFFER_TONE[offer.status]}>
          {t(`commercial.offer.${offer.status}`)}
        </Badge>
        <span>{offerAmount(offer, locale)}</span>
        {offer.valid_until && (
          <span>
            {t("commercial.validUntil", {
              when: formatDate(offer.valid_until, locale, RECORD_ZONE),
            })}
          </span>
        )}
      </span>
    </div>
  );
}

// The account's leading deal: largest by amount, id as the tiebreak so two
// equal deals do not swap between renders. Undefined when no deal can honestly
// be called the leading one.
//
// TWO refusals, both because picking wrong sends the reader to the wrong
// offer:
//
//   - Mixed currencies. A deal's amount carries no base conversion, so
//     comparing 100 JPY with 100 EUR picks a winner by coincidence.
//   - A truncated deals page. The 360 caps its sections, so the largest deal
//     may not be on the page — and "the last offer" pointing at the
//     second-largest deal is a line the reader has no way to question.
//
// In both cases the block is omitted rather than filled from a guess.
function leadingDeal(
  deals: readonly Deal[],
  truncated: boolean,
): Deal | undefined {
  if (deals.length === 0 || truncated) {
    return undefined;
  }
  const currencies = new Set(
    deals.map((deal) => deal.amount?.currency).filter(Boolean),
  );
  if (currencies.size > 1) {
    return undefined;
  }
  return [...deals].sort((a, b) => {
    const left = a.amount?.amount_minor ?? -1;
    const right = b.amount?.amount_minor ?? -1;
    return right !== left ? right - left : a.deal_id.localeCompare(b.deal_id);
  })[0];
}

function offerAmount(
  offer: Offer,
  locale: ReturnType<typeof useLocale>["locale"],
): string {
  // The GROSS, which is what a buyer sees on the document. `net_minor` is the
  // line sum before tax and would understate the offer beside every other
  // money figure on this page.
  if (offer.gross_minor == null || !offer.currency) {
    return "—";
  }
  return formatMoney(offer.gross_minor, offer.currency, locale);
}

const OFFER_TONE: Record<
  Offer["status"],
  "success" | "warn" | "danger" | undefined
> = {
  draft: undefined,
  sent: undefined,
  accepted: "success",
  rejected: "danger",
  expired: "warn",
  superseded: undefined,
};
