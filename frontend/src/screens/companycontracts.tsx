import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import { Badge, Button, EmptyState } from "../design-system/atoms";
import type { SectionState } from "../design-system/surfacestate";
import { formatDate, formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { QueryStates, throwProblem } from "./common";
import { RECORD_ZONE, SectionCard } from "./company360";

// The account's agreements: what it signed, what each is worth, and when the
// next one has to be decided.
//
// TWO READINGS THAT ARE NOT THE SAME READING, and the whole card turns on
// keeping them apart.
//
// `status` is what a human asserted. `under_contract` is computed from the
// dates. They disagree exactly when a term has run out and nobody has moved the
// status yet — which is the normal state of an account whose expiry proposal is
// sitting in an approval queue. Showing only the status would render that
// account as a live customer; showing only the derived reading would erase the
// pending decision. So a row that has ended while still marked active says both.
//
// A superseded row is history, not a candidate, and reads that way — the same
// tone the documents card gives a superseded file.

type Contract = components["schemas"]["Contract"];
type ContractStatus = NonNullable<Contract["status"]>;

const STATUS_LABELS: Record<ContractStatus, MessageKey> = {
  draft: "contracts.status.draft",
  active: "contracts.status.active",
  expired: "contracts.status.expired",
  cancelled: "contracts.status.cancelled",
  superseded: "contracts.status.superseded",
};

// Only two states change how a row should READ. A superseded agreement is
// history; a cancelled one is a fact the reader needs to notice. The rest are
// equal citizens and get no tone, because tone on everything is tone on nothing.
const STATUS_TONE: Partial<Record<ContractStatus, "warn" | "danger">> = {
  superseded: "warn",
  cancelled: "danger",
};

function contractsState(
  loading: boolean,
  failed: boolean,
  mayRead: boolean,
  count: number,
): SectionState {
  // A reader without the grant is WITHHELD, never empty: "this account has no
  // agreements" and "you may not see them" are different sentences, and only
  // one of them is about the account.
  if (!mayRead) {
    return "withheld";
  }
  if (loading) {
    return "loading";
  }
  if (failed) {
    return "unavailable";
  }
  if (count === 0) {
    return "empty";
  }
  return "ready";
}

export function CompanyContractsCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const mayRead = useCan("contract", "read");
  const [activeOnly, setActiveOnly] = useState(false);

  const query = useQuery({
    queryKey: ["orgContracts", orgId, activeOnly],
    enabled: mayRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/contracts", {
        params: {
          path: { id: orgId },
          query: activeOnly ? { under_contract_only: true } : {},
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data?.data ?? [];
    },
  });
  const contracts = query.data ?? [];

  return (
    <SectionCard
      title={t("contracts.title")}
      state={contractsState(
        query.isPending,
        query.isError,
        mayRead,
        contracts.length,
      )}
      emptyLabel={t("contracts.empty")}
    >
      <div className="docs-filters">
        <Button
          small
          aria-pressed={!activeOnly}
          onClick={() => setActiveOnly(false)}
        >
          {t("contracts.filter.all")}
        </Button>
        <Button
          small
          aria-pressed={activeOnly}
          onClick={() => setActiveOnly(true)}
        >
          {t("contracts.filter.active")}
        </Button>
      </div>
      <QueryStates query={query}>
        {contracts.length === 0 ? (
          <EmptyState>
            {t(activeOnly ? "contracts.noneActive" : "contracts.empty")}
          </EmptyState>
        ) : (
          <ul className="docs-list">
            {contracts.map((contract) => (
              <li key={contract.id} className="docs-row">
                <span className="docs-name">{contract.title}</span>
                {contract.contract_number && (
                  <span className="t-caption">{contract.contract_number}</span>
                )}
                <span>{contractValue(contract, locale)}</span>
                {contract.status && (
                  <Badge tone={STATUS_TONE[contract.status]}>
                    {t(STATUS_LABELS[contract.status])}
                  </Badge>
                )}
                <ContractTermState contract={contract} />
              </li>
            ))}
          </ul>
        )}
      </QueryStates>
    </SectionCard>
  );
}

/**
 * ContractTermState is where the row says what is actually true about its
 * dates, which is not always what its status says.
 *
 * The order is deliberate: the pending status change is the most surprising
 * thing a reader can meet, so it leads. A cancellation that has not taken
 * effect comes next, because the customer is still under contract and a card
 * that read as though they had gone would be wrong on the day it matters most.
 * A renewal date is ordinary information and comes last.
 */
function ContractTermState({ contract }: Readonly<{ contract: Contract }>) {
  const t = useT();
  const { locale } = useLocale();

  // Its dates have run out and nobody has moved the status. Saying only
  // "active" here would present an approval queue as a live agreement.
  if (contract.under_contract === false && contract.status === "active") {
    return (
      <span className="t-caption">{t("contracts.endedPendingStatus")}</span>
    );
  }
  if (contract.cancellation_effective_on && contract.under_contract) {
    return (
      <Badge tone="warn">
        {t("contracts.endsOn", {
          when: formatDate(
            contract.cancellation_effective_on,
            locale,
            RECORD_ZONE,
          ),
        })}
      </Badge>
    );
  }
  if (contract.renewal_on) {
    return (
      <span className="t-caption">
        {t("contracts.renewsOn", {
          when: formatDate(contract.renewal_on, locale, RECORD_ZONE),
        })}
      </span>
    );
  }
  return null;
}

// One agreement's value, with the basis said in words.
//
// An annualized figure NEVER renders as a bare amount: a reader who cannot tell
// a three-year total from a per-year figure has been handed a number they will
// misuse, and the row is the last place that distinction can be made.
export function contractValue(contract: Contract, locale: Locale): string {
  if (contract.value_minor == null || !contract.currency) {
    return "";
  }
  const amount = formatMoney(contract.value_minor, contract.currency, locale);
  return contract.value_basis === "annualized_12m" ? `${amount} / a` : amount;
}
