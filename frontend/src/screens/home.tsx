import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { navigate } from "../app/router";
import { EmptyState, SectionHeader } from "../design-system/atoms";
import { DealCard } from "../design-system/composed";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";
import { ApprovalRow, usePendingApprovals } from "./inbox";

// Home / Morning Brief (B-EP09.12b): a ranked queue composed from LIVE
// signals — pending 🟡 approvals first (nothing sent yet; reusing the
// canonical 12a inline editor), then stalled deals. No signal → an honest
// quiet state, never invented urgency.

export function HomeScreen() {
  const t = useT();
  const approvalsQuery = usePendingApprovals();
  const dealsQuery = useQuery({
    queryKey: ["deals"],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals", {
        params: { query: { limit: 100 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  return (
    <div className="wrap narrow">
      <SectionHeader title={t("home.brief")} sub={t("home.sub")} />
      <QueryGate query={approvalsQuery}>
        {(approvals) => {
          const stalled = (dealsQuery.data?.data ?? []).filter(
            (deal) => deal.stalled && deal.status === "open",
          );
          if (approvals.data.length === 0 && stalled.length === 0) {
            return <EmptyState>{t("home.quiet")}</EmptyState>;
          }
          return (
            <div>
              {approvals.data.length > 0 && (
                <section aria-label={t("home.staged")}>
                  <SectionHeader
                    title={t("home.staged")}
                    sub={t("brief.nothingSent")}
                  />
                  {approvals.data.map((approval) => (
                    <ApprovalRow key={approval.id} approval={approval} />
                  ))}
                </section>
              )}
              {stalled.length > 0 && (
                <section aria-label={t("home.stalled")}>
                  <SectionHeader title={t("home.stalled")} />
                  <div
                    style={{ display: "flex", flexDirection: "column", gap: 8 }}
                  >
                    {stalled.map((deal) => (
                      <DealCard
                        key={deal.id}
                        deal={{
                          id: deal.id,
                          name: deal.name,
                          org: "",
                          valueMinor: deal.amount_minor ?? 0,
                          currency: deal.currency ?? "EUR",
                          ageMs: Math.max(
                            0,
                            Date.now() -
                              new Date(
                                deal.last_activity_at ?? deal.created_at,
                              ).getTime(),
                          ),
                          stalled: true,
                        }}
                        onOpen={() =>
                          navigate({ screen: "deals", id: deal.id })
                        }
                      />
                    ))}
                  </div>
                </section>
              )}
            </div>
          );
        }}
      </QueryGate>
    </div>
  );
}
