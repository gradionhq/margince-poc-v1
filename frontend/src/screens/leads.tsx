import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  DataTable,
  SectionHeader,
  SegmentedControl,
  TextInput,
} from "../design-system/atoms";
import { ProvenanceTag } from "../design-system/trust";
import { useT } from "../i18n";
import { ArchiveAction } from "./archive";
import {
  problemMessage,
  provenanceOf,
  QueryGate,
  throwProblem,
  useMe,
} from "./common";
import { CreateAction, type CreateField } from "./create";
import { EditAction } from "./edit";
import {
  ListGate,
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";

// Leads (B-EP09.10a/b): visually SEGREGATED from the contact graph — the
// lead surface is accent-tinted, lead detail is its own screen (never
// person.html — gap §3.5), and promote is eligibility-gated. Lead score is
// lead-local; the ≥60 / 40–59 / <40 colour thresholds are pinned by test.
// Search/filter/sort/pagination (P-14), the rich create modal (P-15), the
// If-Match edit form (P-1), and the dedupe view-existing link (P-16) are
// wired in here the same way as contacts (people.tsx) — the Promote button
// and score/status/company badges on the lead 360 stay exactly as they
// were. Status-change and score-override are Phase 4, not surfaced here.

type Lead = components["schemas"]["Lead"];
type CreateLeadRequest = components["schemas"]["CreateLeadRequest"];
type UpdateLeadRequest = components["schemas"]["UpdateLeadRequest"];

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

async function fetchLeadsPage(
  query: ListQuery,
  cursor: string | null,
): Promise<ListPage<Lead>> {
  const { data, error } = await api.GET("/leads", {
    params: {
      query: {
        q: query.q || undefined,
        sort: query.sort || undefined,
        include_archived: query.includeArchived || undefined,
        cursor: cursor || undefined,
        limit: 50,
        ...query.filters,
      },
    },
  });
  if (error) {
    // A LIST read's honest-error path only needs a message to render — the
    // dedupe "view existing" link is a create/update-only concern.
    throw new Error(problemMessage(error));
  }
  return {
    data: data.data,
    page: {
      next_cursor: data.page.next_cursor ?? null,
      has_more: data.page.has_more,
    },
  };
}

// Builds the create-lead request body: scalar fields trim to undefined when
// blank (never sent rather than sent empty). Lead email is a single string —
// not a repeatable list — so no rows channel is used here.
export function mapLeadBody(values: Record<string, string>): CreateLeadRequest {
  return {
    full_name: values.full_name?.trim() || undefined,
    email: values.email?.trim() || undefined,
    linkedin_url: values.linkedin_url?.trim() || undefined,
    title: values.title?.trim() || undefined,
    company_name: values.company_name?.trim() || undefined,
    candidate_org_key: values.candidate_org_key?.trim() || undefined,
    status: "new",
    source: "manual",
  };
}

function stringField(value: unknown): string {
  return typeof value === "string" ? value : "";
}

// Builds the PATCH body: only the five scalar fields this task surfaces —
// status and score are Phase 4 and never sent from this form.
export function mapLeadUpdate(
  values: Record<string, unknown>,
): UpdateLeadRequest {
  return {
    full_name: stringField(values.full_name).trim() || undefined,
    email: stringField(values.email).trim() || undefined,
    title: stringField(values.title).trim() || undefined,
    company_name: stringField(values.company_name).trim() || undefined,
    candidate_org_key:
      stringField(values.candidate_org_key).trim() || undefined,
  };
}

const leadCreateFields: CreateField[] = [
  { key: "full_name", label: "create.fullName", required: true },
  { key: "email", label: "create.email", type: "email" },
  { key: "linkedin_url", label: "create.linkedinUrl" },
  { key: "title", label: "create.personTitle" },
  { key: "company_name", label: "create.companyName" },
  { key: "candidate_org_key", label: "create.candidateOrgKey" },
];

const leadEditFields: CreateField[] = [
  { key: "full_name", label: "create.fullName", required: true },
  { key: "email", label: "create.email", type: "email" },
  { key: "title", label: "create.personTitle" },
  { key: "company_name", label: "create.companyName" },
  { key: "candidate_org_key", label: "create.candidateOrgKey" },
];

const leadStatusFilterOptions = [
  { value: "new", label: "lead.statusNew" },
  { value: "working", label: "lead.statusWorking" },
  { value: "promoted", label: "lead.statusPromoted" },
  { value: "disqualified", label: "lead.statusDisqualified" },
] as const;

async function createLead(values: Record<string, string>): Promise<Lead> {
  const { data, error } = await api.POST("/leads", {
    body: mapLeadBody(values),
  });
  if (error) {
    throwProblem(error);
  }
  return data;
}

export function LeadsScreen() {
  const t = useT();
  const state = useListQuery<Lead>({
    key: "leads",
    initialSort: "-created_at",
    fetchPage: fetchLeadsPage,
  });
  const { query, setQuery } = state;

  return (
    <div className="wrap lead-surface">
      <div className="list-head">
        <SectionHeader title={t("nav.leads")} sub={t("lead.segregated")} />
        <CreateAction
          label={t("create.lead")}
          invalidate="leads"
          screen="leads"
          create={createLead}
          resolveExisting={(_code, id) => ({ screen: "leads", id })}
          fields={leadCreateFields}
        />
      </div>
      <ListToolbar
        query={query}
        setQuery={setQuery}
        sortOptions={[
          { value: "-score", label: "list.sortScore" },
          { value: "-created_at", label: "list.sortNewest" },
        ]}
        filters={[
          {
            kind: "select",
            key: "status",
            label: "lead.filterStatus",
            options: leadStatusFilterOptions.map((option) => ({ ...option })),
          },
        ]}
      />
      <ListGate state={state} empty={t("common.empty")}>
        {(rows) => (
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
                    {lead.archived_at && (
                      <Badge tone="warn">{t("lead.disqualified")}</Badge>
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
            rows={rows}
            rowKey={(lead) => lead.id}
            onRowClick={(lead) => navigate({ screen: "leads", id: lead.id })}
          />
        )}
      </ListGate>
    </div>
  );
}

const LEAD_OPEN_STATUSES = ["new", "working"] as const;
type LeadOpenStatus = (typeof LEAD_OPEN_STATUSES)[number];

function isOpenStatus(status: Lead["status"]): status is LeadOpenStatus {
  return status === "new" || status === "working";
}

// Phase 4 lifecycle controls (P-10/11/12): status (new↔working only —
// promoted/disqualified are terminal and stay badge-only), the score
// explain/override panel (the read carries no per-factor breakdown, so
// "explain" here is honestly just the override-vs-machine story), and the
// owner display + "Assign to me" (no user-list endpoint exists yet, so
// reassigning to an arbitrary OTHER user isn't buildable here). All three
// share one PATCH /leads/{id} + If-Match(lead.version) mutation.
function LeadLifecycle({
  lead,
  id,
  onChanged,
}: Readonly<{ lead: Lead; id: string; onChanged: () => void }>) {
  const t = useT();
  const me = useMe();
  const scoreFieldId = useId();
  const reasonFieldId = useId();
  const [overriding, setOverriding] = useState(false);
  const [scoreValue, setScoreValue] = useState("");
  const [reasonValue, setReasonValue] = useState("");

  const patch = useMutation({
    mutationFn: async (body: UpdateLeadRequest) => {
      const { data, error } = await api.PATCH("/leads/{id}", {
        params: { path: { id }, ...ifMatch(lead.version) },
        body,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      onChanged();
      setOverriding(false);
      setScoreValue("");
      setReasonValue("");
    },
  });

  const reasonBlank = reasonValue.trim() === "";
  const scoreBlank = scoreValue.trim() === "";
  const meId = me.data?.user?.id;

  return (
    <div
      className="card card-inset"
      style={{
        marginTop: 14,
        display: "flex",
        flexDirection: "column",
        gap: 12,
      }}
    >
      {isOpenStatus(lead.status) && (
        <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
          <span className="t-caption">{t("lead.setStatus")}</span>
          <SegmentedControl
            options={LEAD_OPEN_STATUSES}
            value={lead.status}
            labels={{
              new: t("lead.status.new"),
              working: t("lead.status.working"),
            }}
            onChange={(status) => patch.mutate({ status })}
          />
        </div>
      )}

      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        <span className="t-caption">{t("lead.explainScore")}</span>
        {lead.score_override_reason ? (
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            <p>
              {t("lead.scoreOverridden", {
                reason: lead.score_override_reason,
              })}
            </p>
            {lead.score_computed != null && (
              <p className="t-caption">
                {t("lead.machineScore", { score: lead.score_computed })}
              </p>
            )}
            <Button
              small
              disabled={patch.isPending}
              onClick={() => patch.mutate({ score: null })}
            >
              {t("lead.clearOverride")}
            </Button>
          </div>
        ) : overriding ? (
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              gap: 8,
              maxWidth: 320,
            }}
          >
            <div
              className="t-caption"
              style={{ display: "flex", flexDirection: "column", gap: 4 }}
            >
              <label htmlFor={scoreFieldId}>
                {t("lead.overrideScoreValue")}
              </label>
              <TextInput
                id={scoreFieldId}
                type="number"
                min={0}
                max={100}
                value={scoreValue}
                onChange={(event) => setScoreValue(event.target.value)}
              />
            </div>
            <div
              className="t-caption"
              style={{ display: "flex", flexDirection: "column", gap: 4 }}
            >
              <label htmlFor={reasonFieldId}>{t("lead.overrideReason")}</label>
              <TextInput
                id={reasonFieldId}
                value={reasonValue}
                onChange={(event) => setReasonValue(event.target.value)}
              />
            </div>
            <div style={{ display: "flex", gap: 8 }}>
              <Button
                variant="primary"
                small
                disabled={reasonBlank || scoreBlank || patch.isPending}
                onClick={() =>
                  patch.mutate({
                    score: Number(scoreValue),
                    score_override_reason: reasonValue.trim(),
                  })
                }
              >
                {t("lead.saveOverride")}
              </Button>
              <Button small onClick={() => setOverriding(false)}>
                {t("create.cancel")}
              </Button>
            </div>
          </div>
        ) : (
          <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
            <span className="t-caption">{t("lead.machineComputed")}</span>
            <Button small onClick={() => setOverriding(true)}>
              {t("lead.overrideScore")}
            </Button>
          </div>
        )}
      </div>

      <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
        <span className="t-caption">
          {lead.owner_id
            ? t("lead.owner", { owner: lead.owner_id })
            : t("lead.unassigned")}
        </span>
        {meId && meId !== lead.owner_id && (
          <Button
            small
            disabled={patch.isPending}
            onClick={() => patch.mutate({ owner_id: meId })}
          >
            {t("lead.assignToMe")}
          </Button>
        )}
      </div>

      {patch.isError && (
        <span className="t-caption" style={{ color: "var(--danger)" }}>
          {patch.error instanceof Error ? patch.error.message : null}
        </span>
      )}
    </div>
  );
}

export function LeadScreen({ id }: Readonly<{ id: string }>) {
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
            <div className="list-head">
              <SectionHeader
                title={lead.full_name ?? lead.email ?? t("nav.leads")}
                sub={t("lead.segregated")}
              />
              <EditAction
                label={t("record.edit")}
                fields={leadEditFields}
                record={{
                  id: lead.id,
                  version: lead.version,
                  full_name: lead.full_name ?? "",
                  email: lead.email ?? "",
                  title: lead.title ?? "",
                  company_name: lead.company_name ?? "",
                  candidate_org_key: lead.candidate_org_key ?? "",
                }}
                update={async (values) => {
                  const { data, error } = await api.PATCH("/leads/{id}", {
                    params: {
                      path: { id },
                      ...ifMatch(lead.version),
                    },
                    body: mapLeadUpdate(values),
                  });
                  if (error) {
                    throwProblem(error);
                  }
                  return data;
                }}
                invalidate="leads"
                recordKey="lead"
              />
              <ArchiveAction
                label={t("record.disqualify")}
                confirmText={t("record.disqualifyConfirm")}
                archive={async () => {
                  const { data, error } = await api.DELETE("/leads/{id}", {
                    params: { path: { id } },
                  });
                  if (error) {
                    throwProblem(error);
                  }
                  return data;
                }}
                invalidate="leads"
                recordKey="lead"
                onArchived={() => navigate({ screen: "leads" })}
              />
            </div>
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
              {lead.archived_at && (
                <Badge tone="warn">{t("lead.disqualified")}</Badge>
              )}
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
            <LeadLifecycle
              lead={lead}
              id={id}
              onChanged={() => {
                queryClient.invalidateQueries({ queryKey: ["leads"] });
                queryClient.invalidateQueries({ queryKey: ["lead", id] });
              }}
            />
          </div>
        )}
      </QueryGate>
    </div>
  );
}
