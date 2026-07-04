import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  DataTable,
  SectionHeader,
} from "../design-system/atoms";
import { ProvenanceTag } from "../design-system/trust";
import { useT } from "../i18n";
import { problemMessage, provenanceOf, QueryGate } from "./common";

// Leads (B-EP09.10a/b): visually SEGREGATED from the contact graph — the
// lead surface is accent-tinted, lead detail is its own screen (never
// person.html — gap §3.5), and promote is eligibility-gated. Lead score is
// lead-local; the ≥60 / 40–59 / <40 colour thresholds are pinned by test.

type Lead = components["schemas"]["Lead"];

export function scoreTone(score: number): "success" | "warn" | "danger" {
  if (score >= 60) {
    return "success";
  }
  if (score >= 40) {
    return "warn";
  }
  return "danger";
}

export function promoteEligible(lead: Lead): boolean {
  return (
    (lead.status === "new" || lead.status === "working") && Boolean(lead.email)
  );
}

export function LeadsScreen() {
  const t = useT();
  const query = useQuery({
    queryKey: ["leads"],
    queryFn: async () => {
      const { data, error } = await api.GET("/leads", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  return (
    <div className="wrap lead-surface">
      <SectionHeader title={t("nav.leads")} sub={t("lead.segregated")} />
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <DataTable
            columns={[
              {
                key: "name",
                header: t("people.name"),
                render: (lead: Lead) => (
                  <span>
                    <strong>{lead.full_name ?? lead.email ?? ""}</strong>
                    {lead.company_name && (
                      <span className="t-caption"> · {lead.company_name}</span>
                    )}
                  </span>
                ),
              },
              {
                key: "score",
                header: t("lead.score"),
                render: (lead: Lead) => (
                  <Badge tone={scoreTone(lead.score)}>{lead.score}</Badge>
                ),
              },
              {
                key: "status",
                header: t("lead.status"),
                render: (lead: Lead) => <Badge>{lead.status}</Badge>,
              },
              {
                key: "provenance",
                header: t("people.capturedBy"),
                render: (lead: Lead) => (
                  <ProvenanceTag provenance={provenanceOf(lead.captured_by)} />
                ),
              },
            ]}
            rows={page.data}
            rowKey={(lead) => lead.id}
            onRowClick={(lead) => navigate({ screen: "leads", id: lead.id })}
          />
        )}
      </QueryGate>
    </div>
  );
}

export function LeadScreen({ id }: { id: string }) {
  const t = useT();
  const queryClient = useQueryClient();
  const leadQuery = useQuery({
    queryKey: ["lead", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/leads/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const promote = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/leads/{id}/promote", {
        params: { path: { id } },
        body: { trigger: "human_qualify" },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ["leads"] });
      if (result.person?.id) {
        navigate({ screen: "contacts", id: result.person.id });
      }
    },
  });

  return (
    <div className="wrap lead-surface">
      <QueryGate query={leadQuery}>
        {(lead) => (
          <div className="card lead-detail">
            <SectionHeader
              title={lead.full_name ?? lead.email ?? t("nav.leads")}
              sub={t("lead.segregated")}
            />
            <div
              style={{
                display: "flex",
                gap: 8,
                flexWrap: "wrap",
                marginBottom: 12,
              }}
            >
              <Badge tone={scoreTone(lead.score)}>
                {t("lead.score")}: {lead.score}
              </Badge>
              <Badge>{lead.status}</Badge>
              {lead.company_name && <Badge>{lead.company_name}</Badge>}
              <ProvenanceTag provenance={provenanceOf(lead.captured_by)} />
            </div>
            {lead.email && <p className="t-mono">{lead.email}</p>}
            <div
              style={{
                marginTop: 14,
                display: "flex",
                gap: 8,
                alignItems: "center",
              }}
            >
              <Button
                variant="primary"
                disabled={!promoteEligible(lead) || promote.isPending}
                onClick={() => promote.mutate()}
              >
                {t("lead.promote")}
              </Button>
              {!promoteEligible(lead) && (
                <span className="t-caption">{t("lead.promoteIneligible")}</span>
              )}
              {promote.isError && (
                <span className="t-caption" style={{ color: "var(--danger)" }}>
                  {promote.error instanceof Error
                    ? promote.error.message
                    : null}
                </span>
              )}
            </div>
          </div>
        )}
      </QueryGate>
    </div>
  );
}
