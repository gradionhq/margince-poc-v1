import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type DragEvent, useMemo, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  DataTable,
  EmptyState,
  Modal,
  SectionHeader,
  SegmentedControl,
  TextInput,
} from "../design-system/atoms";
import {
  type BoardColumn,
  type BoardDeal,
  PipelineBoard,
  RecordView,
} from "../design-system/composed";
import { AutonomyDot } from "../design-system/trust";
import { formatDate, formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";
import { CreateAction } from "./create";
import { LogActivity } from "./logactivity";
import { activityTimeline } from "./people";

// Deal surfaces (B-EP09.11a/b/c): the five-stage Kanban with drag-to-advance
// (terminal stages are a 🟡 confirm, AC-deal-6), the board↔table segmented
// control over the SAME fetched set (no reload), and the deal 360 with the
// stage stepper and the live pending-approval staged cards. Weighting math
// stays out of the UI beyond same-currency page-local sub-lines: a mixed-
// currency column renders no sum (the FX rule: never sum native minors
// across currencies).

type Deal = components["schemas"]["Deal"];
type Stage = components["schemas"]["Stage"];
type Offer = components["schemas"]["Offer"];

function usePipeline() {
  return useQuery({
    queryKey: ["pipelines"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      const pipeline =
        data.data.find((candidate) => candidate.is_default) ?? data.data[0];
      if (!pipeline) {
        throw new Error("no pipeline");
      }
      return pipeline;
    },
  });
}

function useDeals() {
  return useQuery({
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
}

function toBoardDeal(deal: Deal): BoardDeal {
  const since = deal.last_activity_at ?? deal.created_at;
  return {
    id: deal.id,
    name: deal.name,
    org: "",
    valueMinor: deal.amount_minor ?? 0,
    currency: deal.currency ?? "EUR",
    ageMs: Math.max(0, Date.now() - new Date(since).getTime()),
    stalled: deal.stalled ?? false,
  };
}

export function buildColumns(stages: Stage[], deals: Deal[]): BoardColumn[] {
  return [...stages]
    .sort((a, b) => a.position - b.position)
    .map((stage) => {
      const stageDeals = deals.filter((deal) => deal.stage_id === stage.id);
      const currencies = new Set(
        stageDeals.map((deal) => deal.currency ?? "EUR"),
      );
      const currency = currencies.size === 1 ? [...currencies][0] : null;
      // Sub-lines are page-local, same-currency display sums only; a mixed
      // column shows no figure rather than a cross-currency lie.
      const raw = currency
        ? stageDeals.reduce((sum, deal) => sum + (deal.amount_minor ?? 0), 0)
        : null;
      return {
        stage: stage.id,
        label: stage.name,
        probabilityPct: stage.win_probability,
        rawMinor: raw ?? 0,
        weightedMinor:
          raw === null ? 0 : Math.round((raw * stage.win_probability) / 100),
        currency: currency ?? "EUR",
        deals: stageDeals.map(toBoardDeal),
        sumHidden: raw === null,
        semantic: stage.semantic,
      } as BoardColumn & { sumHidden: boolean; semantic: Stage["semantic"] };
    });
}

type PendingAdvance = {
  dealId: string;
  toStage: Stage;
};

// Won reads success, lost reads danger, an open deal carries no status tone.
function dealStatusTone(
  status: Deal["status"],
): "success" | "danger" | undefined {
  if (status === "won") {
    return "success";
  }
  if (status === "lost") {
    return "danger";
  }
  return undefined;
}

export function DealsScreen({
  startCreating = false,
}: Readonly<{ startCreating?: boolean }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const pipelineQuery = usePipeline();
  const dealsQuery = useDeals();
  const [view, setView] = useState<"board" | "table">("board");
  const [pending, setPending] = useState<PendingAdvance | null>(null);
  const [lostReason, setLostReason] = useState("");
  const [toast, setToast] = useState<string | null>(null);
  const dragging = useRef<string | null>(null);
  const lastDragEnd = useRef(0);

  const advance = useMutation({
    mutationFn: async (input: {
      dealId: string;
      toStage: Stage;
      lostReason?: string;
    }) => {
      const terminal = input.toStage.semantic !== "open";
      const { data, error } = await api.POST("/deals/{id}/advance", {
        params: { path: { id: input.dealId } },
        body: {
          to_stage_id: input.toStage.id,
          ...(terminal ? { status: input.toStage.semantic } : {}),
          ...(input.toStage.semantic === "lost"
            ? { lost_reason: input.lostReason }
            : {}),
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (_deal, input) => {
      queryClient.invalidateQueries({ queryKey: ["deals"] });
      setToast(t("deals.advanced", { stage: input.toStage.name }));
      setTimeout(() => setToast(null), 3500);
    },
  });

  const stages = pipelineQuery.data?.stages ?? [];

  const orgsQuery = useQuery({
    queryKey: ["organizations"],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const createDeal = async (values: Record<string, string>) => {
    const pipeline = pipelineQuery.data;
    if (!pipeline) {
      throw new Error(problemMessage(null));
    }
    const amount = values.amount?.trim();
    const { data, error } = await api.POST("/deals", {
      body: {
        name: values.name.trim(),
        pipeline_id: pipeline.id,
        stage_id: values.stage_id,
        // The UI takes major units; the wire is minor units.
        amount_minor: amount ? Math.round(Number(amount) * 100) : null,
        currency: values.currency || "EUR",
        organization_id: values.organization_id || null,
        expected_close_date: values.expected_close_date || null,
        source: "manual",
      },
    });
    if (error) {
      throw new Error(problemMessage(error));
    }
    return data;
  };

  // Open-stage targets only: a deal is born open (INV-CLOSE-PAST twin rule);
  // won/lost are reached through the confirmed advance, never at create.
  const openStages = stages.filter((stage) => stage.semantic === "open");

  const requestAdvance = (dealId: string, stageId: string) => {
    const toStage = stages.find((stage) => stage.id === stageId);
    if (!toStage) {
      return;
    }
    if (toStage.semantic === "open") {
      advance.mutate({ dealId, toStage });
    } else {
      // Terminal-stage advance is a 🟡 confirm (AC-deal-6).
      setLostReason("");
      setPending({ dealId, toStage });
    }
  };

  // Board interactions are hoisted here so the render-prop tree below doesn't
  // nest their event callbacks past the readable depth.
  const openDeal = (deal: BoardDeal) => {
    if (Date.now() - lastDragEnd.current > 250) {
      navigate({ screen: "deals", id: deal.id });
    }
  };

  const cardDragHandlers = (deal: BoardDeal) => ({
    draggable: true as const,
    onDragStart: (event: DragEvent) => {
      dragging.current = deal.id;
      event.dataTransfer.setData("text/plain", deal.id);
    },
  });

  const columnDropHandlers = (column: BoardColumn) => ({
    onDragOver: (event: DragEvent) => {
      event.preventDefault();
      (event.currentTarget as HTMLElement).classList.add("droptarget");
    },
    onDragLeave: (event: DragEvent) => {
      (event.currentTarget as HTMLElement).classList.remove("droptarget");
    },
    onDrop: (event: DragEvent) => {
      event.preventDefault();
      (event.currentTarget as HTMLElement).classList.remove("droptarget");
      const dealId =
        event.dataTransfer.getData("text/plain") || dragging.current;
      dragging.current = null;
      lastDragEnd.current = Date.now();
      if (dealId) {
        requestAdvance(dealId, column.stage);
      }
    },
  });

  return (
    <div className="wrap">
      <div className="list-head">
        <SectionHeader title={t("nav.deals")} />
        {openStages.length > 0 && (
          <CreateAction
            label={t("create.deal")}
            invalidate="deals"
            screen="deals"
            create={createDeal}
            startOpen={startCreating}
            fields={[
              { key: "name", label: "create.dealName", required: true },
              { key: "amount", label: "create.amount", type: "number" },
              {
                key: "currency",
                label: "create.currency",
                type: "select",
                required: true,
                options: ["EUR", "USD", "GBP", "CHF"].map((code) => ({
                  value: code,
                  label: code,
                })),
              },
              {
                key: "stage_id",
                label: "create.stage",
                type: "select",
                required: true,
                options: openStages.map((stage) => ({
                  value: stage.id,
                  label: stage.name,
                })),
              },
              {
                key: "organization_id",
                label: "create.organization",
                type: "select",
                options: (orgsQuery.data?.data ?? []).map((org) => ({
                  value: org.id,
                  label: org.display_name,
                })),
              },
              {
                key: "expected_close_date",
                label: "create.expectedClose",
                type: "date",
              },
            ]}
          />
        )}
      </div>
      <div style={{ marginBottom: 12 }}>
        <SegmentedControl
          options={["board", "table"] as const}
          value={view}
          onChange={setView}
          labels={{ board: t("deals.viewBoard"), table: t("deals.viewTable") }}
        />
      </div>
      <QueryGate query={dealsQuery}>
        {(page) => (
          <QueryGate query={pipelineQuery}>
            {(pipeline) => {
              const columns = buildColumns(pipeline.stages ?? [], page.data);
              return view === "board" ? (
                <PipelineBoard
                  columns={columns}
                  onOpen={openDeal}
                  cardDragHandlers={cardDragHandlers}
                  columnDropHandlers={columnDropHandlers}
                />
              ) : (
                <DealTable deals={page.data} stages={pipeline.stages ?? []} />
              );
            }}
          </QueryGate>
        )}
      </QueryGate>
      {advance.isError && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 10 }}
        >
          {advance.error instanceof Error ? advance.error.message : null}
        </p>
      )}
      {toast && (
        <div className="toast-region">
          <output className="toast">
            <span className="dot dot-auto" />
            {toast}
          </output>
        </div>
      )}
      <Modal
        open={pending !== null}
        onClose={() => setPending(null)}
        labelledBy="advance-title"
      >
        {pending && (
          <>
            <p className="t-sub" id="advance-title">
              <AutonomyDot tier="confirm" />{" "}
              {t("deals.confirmAdvance", { stage: pending.toStage.name })}
            </p>
            <p className="t-caption" style={{ marginTop: 6 }}>
              {t("deals.confirmTerminal", { status: pending.toStage.semantic })}
            </p>
            {pending.toStage.semantic === "lost" && (
              <div className="field" style={{ marginTop: 10 }}>
                <span className="t-label" id="lost-reason-label">
                  {t("deals.lostReason")}
                </span>
                <TextInput
                  aria-labelledby="lost-reason-label"
                  value={lostReason}
                  onChange={(event) => setLostReason(event.target.value)}
                />
              </div>
            )}
            <div className="actions">
              <Button onClick={() => setPending(null)}>
                {t("deals.cancel")}
              </Button>
              <Button
                variant="primary"
                disabled={
                  pending.toStage.semantic === "lost" &&
                  lostReason.trim() === ""
                }
                onClick={() => {
                  advance.mutate({
                    dealId: pending.dealId,
                    toStage: pending.toStage,
                    lostReason: lostReason.trim() || undefined,
                  });
                  setPending(null);
                }}
              >
                {t("deals.confirm")}
              </Button>
            </div>
          </>
        )}
      </Modal>
    </div>
  );
}

function DealTable({
  deals,
  stages,
}: Readonly<{ deals: Deal[]; stages: Stage[] }>) {
  const t = useT();
  const { locale } = useLocale();
  const [sortKey, setSortKey] = useState<"name" | "amount" | "close">("name");
  const [descending, setDescending] = useState(false);
  const stageName = useMemo(
    () => new Map(stages.map((stage) => [stage.id, stage.name])),
    [stages],
  );

  const sorted = useMemo(() => {
    const compareDeals = (a: Deal, b: Deal): number => {
      if (sortKey === "amount") {
        return (a.amount_minor ?? 0) - (b.amount_minor ?? 0);
      }
      if (sortKey === "close") {
        return (a.expected_close_date ?? "").localeCompare(
          b.expected_close_date ?? "",
        );
      }
      return a.name.localeCompare(b.name);
    };
    const rows = [...deals];
    rows.sort((a, b) => {
      const compare = compareDeals(a, b);
      return descending ? -compare : compare;
    });
    return rows;
  }, [deals, sortKey, descending]);

  const sortBy = (key: typeof sortKey) => {
    if (key === sortKey) {
      setDescending((value) => !value);
    } else {
      setSortKey(key);
      setDescending(false);
    }
  };

  return (
    <div>
      <div style={{ display: "flex", gap: 6, marginBottom: 8 }}>
        <Button small onClick={() => sortBy("name")}>
          {t("people.name")}
        </Button>
        <Button small onClick={() => sortBy("amount")}>
          {t("deals.amount")}
        </Button>
        <Button small onClick={() => sortBy("close")}>
          {t("deals.close")}
        </Button>
      </div>
      <DataTable
        columns={[
          {
            key: "name",
            header: t("people.name"),
            render: (deal: Deal) => deal.name,
          },
          {
            key: "stage",
            header: t("deals.stage"),
            render: (deal: Deal) => stageName.get(deal.stage_id) ?? "",
          },
          {
            key: "amount",
            header: t("deals.amount"),
            render: (deal: Deal) =>
              deal.amount_minor != null && deal.currency ? (
                <span className="t-mono">
                  {formatMoney(deal.amount_minor, deal.currency, locale)}
                </span>
              ) : null,
          },
          {
            key: "close",
            header: t("deals.close"),
            render: (deal: Deal) =>
              deal.expected_close_date
                ? formatDate(deal.expected_close_date, locale, "Europe/Berlin")
                : null,
          },
          {
            key: "status",
            header: t("lead.status"),
            render: (deal: Deal) => (
              <Badge tone={dealStatusTone(deal.status)}>{deal.status}</Badge>
            ),
          },
        ]}
        rows={sorted}
        rowKey={(deal) => deal.id}
        onRowClick={(deal) => navigate({ screen: "deals", id: deal.id })}
      />
    </div>
  );
}

export function DealScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const dealQuery = useQuery({
    queryKey: ["deal", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const pipelineQuery = usePipeline();
  const stakeholdersQuery = useQuery({
    queryKey: ["deal-stakeholders", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/stakeholders", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const approvalsQuery = useQuery({
    queryKey: ["approvals", "pending"],
    queryFn: async () => {
      const { data, error } = await api.GET("/approvals", {
        params: { query: { status: "pending", limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const timelineQuery = useQuery({
    queryKey: ["activities", "deal", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: { query: { entity_type: "deal", entity_id: id, limit: 20 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const offersQuery = useQuery({
    queryKey: ["deal-offers", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/offers", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const createOffer = useMutation({
    mutationFn: async (currency: string) => {
      const { data, error } = await api.POST("/deals/{id}/offers", {
        params: { path: { id } },
        body: { currency, source: "manual" },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (offer: Offer) => {
      navigate({ screen: "offers", id: offer.id });
    },
  });
  const decide = useMutation({
    mutationFn: async (input: {
      approvalId: string;
      verdict: "approve" | "reject";
    }) => {
      const path =
        input.verdict === "approve"
          ? "/approvals/{id}/approve"
          : "/approvals/{id}/reject";
      const { error } = await api.POST(path, {
        params: { path: { id: input.approvalId } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ["approvals", "pending"] }),
  });

  return (
    <div className="wrap">
      <QueryGate query={dealQuery}>
        {(deal) => {
          const stages = [...(pipelineQuery.data?.stages ?? [])].sort(
            (a, b) => a.position - b.position,
          );
          const dealApprovals = (approvalsQuery.data?.data ?? []).filter(
            (approval) => approval.target_entity_id === deal.id,
          );
          return (
            <RecordView
              name={deal.name}
              subtitle={
                deal.amount_minor != null && deal.currency
                  ? formatMoney(deal.amount_minor, deal.currency, locale)
                  : undefined
              }
              zone="Europe/Berlin"
              badges={
                <Badge tone={dealStatusTone(deal.status)}>{deal.status}</Badge>
              }
              timeline={
                timelineQuery.isSuccess
                  ? activityTimeline(timelineQuery.data.data)
                  : []
              }
            >
              {stages.length > 0 && (
                <nav className="stepper" aria-label={t("deals.stage")}>
                  {stages.map((stage) => (
                    <span
                      key={stage.id}
                      className={
                        stage.id === deal.stage_id ? "step current" : "step"
                      }
                      aria-current={
                        stage.id === deal.stage_id ? "step" : undefined
                      }
                    >
                      {stage.name}
                    </span>
                  ))}
                </nav>
              )}
              {dealApprovals.length > 0 && (
                <section className="card" style={{ marginBottom: 16 }}>
                  <SectionHeader title={t("deal.pendingApprovals")} />
                  {dealApprovals.map((approval) => (
                    <div
                      key={approval.id}
                      className="staging-card"
                      style={{ marginBottom: 8 }}
                    >
                      <div
                        style={{
                          display: "flex",
                          alignItems: "center",
                          gap: 8,
                        }}
                      >
                        <AutonomyDot tier="confirm" />
                        <span className="t-label">{approval.kind}</span>
                        <span className="t-small">{approval.proposed_by}</span>
                      </div>
                      <div className="approval-gate">
                        <Button
                          variant="primary"
                          small
                          onClick={() =>
                            decide.mutate({
                              approvalId: approval.id,
                              verdict: "approve",
                            })
                          }
                        >
                          {t("trust.accept")}
                        </Button>
                        <Button
                          small
                          onClick={() =>
                            decide.mutate({
                              approvalId: approval.id,
                              verdict: "reject",
                            })
                          }
                        >
                          {t("trust.dismiss")}
                        </Button>
                      </div>
                    </div>
                  ))}
                </section>
              )}
              {stakeholdersQuery.isSuccess &&
                stakeholdersQuery.data.data.length > 0 && (
                  <section className="card" style={{ marginBottom: 16 }}>
                    <SectionHeader title={t("deal.stakeholders")} />
                    <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
                      {stakeholdersQuery.data.data.map((stakeholder) => (
                        <Badge key={stakeholder.id}>
                          {stakeholder.role ??
                            stakeholder.person_id ??
                            stakeholder.kind}
                        </Badge>
                      ))}
                    </div>
                  </section>
                )}
              <section className="card" style={{ marginBottom: 16 }}>
                <div className="list-head">
                  <SectionHeader title={t("deal.offers")} />
                  <Button
                    small
                    disabled={createOffer.isPending}
                    onClick={() => createOffer.mutate(deal.currency ?? "EUR")}
                  >
                    {t("deal.newOffer")}
                  </Button>
                </div>
                {offersQuery.isSuccess &&
                  (offersQuery.data.data.length > 0 ? (
                    <DataTable
                      columns={[
                        {
                          key: "offer_number",
                          header: t("deal.offerNumber"),
                          render: (offer: Offer) => offer.offer_number,
                        },
                        {
                          key: "revision",
                          header: t("deal.offerRevision"),
                          render: (offer: Offer) => String(offer.revision),
                        },
                        {
                          key: "status",
                          header: t("lead.status"),
                          render: (offer: Offer) => (
                            <Badge>{offer.status}</Badge>
                          ),
                        },
                        {
                          key: "gross",
                          header: t("deals.amount"),
                          render: (offer: Offer) => (
                            <span className="t-mono">
                              {formatMoney(
                                offer.gross_minor,
                                offer.currency,
                                locale,
                              )}
                            </span>
                          ),
                        },
                      ]}
                      rows={offersQuery.data.data}
                      rowKey={(offer) => offer.id}
                      onRowClick={(offer) =>
                        navigate({ screen: "offers", id: offer.id })
                      }
                    />
                  ) : (
                    <EmptyState>{t("deal.offersEmpty")}</EmptyState>
                  ))}
              </section>
              <LogActivity entityType="deal" entityId={deal.id} />
            </RecordView>
          );
        }}
      </QueryGate>
    </div>
  );
}
