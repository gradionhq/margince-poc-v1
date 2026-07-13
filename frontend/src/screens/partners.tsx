// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Button,
  DataTable,
  EmptyState,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage, QueryGate, throwProblem } from "./common";
import {
  ListGate,
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";

// The Partner tab (company 360, P-6): an org IS a partner iff it has a
// `partner` row (data-model.md §4.3) — GET /organizations/{id}/partner's 404
// means "not a partner yet", not an error, so it renders an honest setup form
// rather than the shared error state. Both the setup and edit paths PUT the
// same UpsertPartnerRequest; first creation carries no If-Match (there is no
// prior version to precondition on), an edit carries the partner's own
// `version`. #/partners (PartnersScreen) is the flat list over every partner,
// reached from the Companies list header — the 9-item nav rail is spec-pinned
// and does not gain a tenth entry for it.

type Partner = components["schemas"]["Partner"];
type UpsertPartnerRequest = components["schemas"]["UpsertPartnerRequest"];
type PartnerRole = NonNullable<UpsertPartnerRequest["partner_role"]>;
type CertStatus = Partner["cert_status"];
type MarginTier = NonNullable<UpsertPartnerRequest["margin_tier"]>;
type RelationshipStage = Partner["relationship_stage"];

const PARTNER_ROLES: readonly PartnerRole[] = [
  "hosting",
  "consulting",
  "strategic",
];
const CERT_STATUSES: readonly CertStatus[] = [
  "applied",
  "certified",
  "suspended",
];
const MARGIN_TIERS: readonly MarginTier[] = [
  "tier1_15",
  "tier2_20",
  "tier3_25",
];
const RELATIONSHIP_STAGES: readonly RelationshipStage[] = [
  "research",
  "identified",
  "contacted",
  "in_conversation",
  "fit_confirmed",
  "agreement_pending",
  "active",
  "active_referring",
  "dormant",
  "no_fit",
];

const ROLE_LABELS: Record<PartnerRole, MessageKey> = {
  hosting: "partner.role.hosting",
  consulting: "partner.role.consulting",
  strategic: "partner.role.strategic",
};

const CERT_LABELS: Record<CertStatus, MessageKey> = {
  applied: "partner.cert.applied",
  certified: "partner.cert.certified",
  suspended: "partner.cert.suspended",
};

const MARGIN_TIER_LABELS: Record<MarginTier, MessageKey> = {
  tier1_15: "partner.marginTier.tier1",
  tier2_20: "partner.marginTier.tier2",
  tier3_25: "partner.marginTier.tier3",
};

const STAGE_LABELS: Record<RelationshipStage, MessageKey> = {
  research: "partner.stage.research",
  identified: "partner.stage.identified",
  contacted: "partner.stage.contacted",
  in_conversation: "partner.stage.inConversation",
  fit_confirmed: "partner.stage.fitConfirmed",
  agreement_pending: "partner.stage.agreementPending",
  active: "partner.stage.active",
  active_referring: "partner.stage.activeReferring",
  dormant: "partner.stage.dormant",
  no_fit: "partner.stage.noFit",
};

function asPartnerRole(value: string): PartnerRole | undefined {
  return (PARTNER_ROLES as readonly string[]).includes(value)
    ? (value as PartnerRole)
    : undefined;
}

function asCertStatus(value: string): CertStatus | undefined {
  return (CERT_STATUSES as readonly string[]).includes(value)
    ? (value as CertStatus)
    : undefined;
}

async function fetchPartner(organizationId: string): Promise<Partner | null> {
  const { data, error, response } = await api.GET(
    "/organizations/{id}/partner",
    { params: { path: { id: organizationId } } },
  );
  if (response.status === 404) {
    return null;
  }
  if (error) {
    throw new Error(problemMessage(error));
  }
  return data ?? null;
}

type PartnerFormValues = {
  partner_role: PartnerRole;
  cert_status: CertStatus;
  margin_tier: "" | MarginTier;
  relationship_stage: RelationshipStage;
  next_step: string;
  next_step_due_at: string;
  served_segments: string;
};

function defaultFormValues(partner?: Partner): PartnerFormValues {
  return {
    partner_role: partner?.partner_role ?? "hosting",
    cert_status: partner?.cert_status ?? "applied",
    margin_tier: partner?.margin_tier ?? "",
    relationship_stage: partner?.relationship_stage ?? "research",
    next_step: partner?.next_step ?? "",
    next_step_due_at: partner?.next_step_due_at ?? "",
    served_segments: (partner?.served_segments ?? []).join(", "),
  };
}

function buildUpsertBody(values: PartnerFormValues): UpsertPartnerRequest {
  const segments = values.served_segments
    .split(",")
    .map((segment) => segment.trim())
    .filter((segment) => segment.length > 0);
  return {
    partner_role: values.partner_role,
    cert_status: values.cert_status,
    margin_tier: values.margin_tier || null,
    relationship_stage: values.relationship_stage,
    next_step: values.next_step.trim() || undefined,
    next_step_due_at: values.next_step_due_at || undefined,
    served_segments: segments.length > 0 ? segments : undefined,
  };
}

// The one form both "make this a partner" and "edit partner" render — they
// differ only in the record they prefill from and whether the PUT carries
// If-Match (absent on first creation: there is no prior version to
// precondition on).
function PartnerForm({
  organizationId,
  partner,
  onSaved,
  onCancel,
  submitLabel,
}: Readonly<{
  organizationId: string;
  partner?: Partner;
  onSaved: () => void;
  onCancel?: () => void;
  submitLabel: MessageKey;
}>) {
  const t = useT();
  const formId = useId();
  const [values, setValues] = useState<PartnerFormValues>(() =>
    defaultFormValues(partner),
  );

  useEffect(() => {
    setValues(defaultFormValues(partner));
  }, [partner]);

  const mutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.PUT("/organizations/{id}/partner", {
        params: {
          path: { id: organizationId },
          ...ifMatch(partner?.version),
        },
        body: buildUpsertBody(values),
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: onSaved,
  });

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        mutation.mutate();
      }}
      style={{ display: "flex", flexDirection: "column", gap: 10 }}
    >
      <div className="field">
        <label className="t-label" htmlFor={`${formId}-role`}>
          {t("partner.role")} *
        </label>
        <select
          id={`${formId}-role`}
          className="input"
          required
          value={values.partner_role}
          onChange={(event) =>
            setValues({
              ...values,
              partner_role:
                asPartnerRole(event.target.value) ?? values.partner_role,
            })
          }
        >
          {PARTNER_ROLES.map((role) => (
            <option key={role} value={role}>
              {t(ROLE_LABELS[role])}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label className="t-label" htmlFor={`${formId}-cert`}>
          {t("partner.certStatus")}
        </label>
        <select
          id={`${formId}-cert`}
          className="input"
          value={values.cert_status}
          onChange={(event) =>
            setValues({
              ...values,
              cert_status:
                asCertStatus(event.target.value) ?? values.cert_status,
            })
          }
        >
          {CERT_STATUSES.map((status) => (
            <option key={status} value={status}>
              {t(CERT_LABELS[status])}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label className="t-label" htmlFor={`${formId}-tier`}>
          {t("partner.marginTier")}
        </label>
        <select
          id={`${formId}-tier`}
          className="input"
          value={values.margin_tier}
          onChange={(event) =>
            setValues({
              ...values,
              margin_tier: (event.target.value || "") as "" | MarginTier,
            })
          }
        >
          <option value="" />
          {MARGIN_TIERS.map((tier) => (
            <option key={tier} value={tier}>
              {t(MARGIN_TIER_LABELS[tier])}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label className="t-label" htmlFor={`${formId}-stage`}>
          {t("partner.stage")}
        </label>
        <select
          id={`${formId}-stage`}
          className="input"
          value={values.relationship_stage}
          onChange={(event) =>
            setValues({
              ...values,
              relationship_stage: (event.target.value ||
                values.relationship_stage) as RelationshipStage,
            })
          }
        >
          {RELATIONSHIP_STAGES.map((stage) => (
            <option key={stage} value={stage}>
              {t(STAGE_LABELS[stage])}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label className="t-label" htmlFor={`${formId}-next-step`}>
          {t("partner.nextStep")}
        </label>
        <TextInput
          id={`${formId}-next-step`}
          value={values.next_step}
          onChange={(event) =>
            setValues({ ...values, next_step: event.target.value })
          }
        />
      </div>
      <div className="field">
        <label className="t-label" htmlFor={`${formId}-next-step-due`}>
          {t("partner.nextStepDue")}
        </label>
        <TextInput
          id={`${formId}-next-step-due`}
          type="date"
          value={values.next_step_due_at}
          onChange={(event) =>
            setValues({ ...values, next_step_due_at: event.target.value })
          }
        />
      </div>
      <div className="field">
        <label className="t-label" htmlFor={`${formId}-segments`}>
          {t("partner.servedSegments")}
        </label>
        <TextInput
          id={`${formId}-segments`}
          value={values.served_segments}
          placeholder={t("partner.servedSegmentsHint")}
          onChange={(event) =>
            setValues({ ...values, served_segments: event.target.value })
          }
        />
      </div>
      {mutation.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {mutation.error instanceof Error ? mutation.error.message : null}
        </p>
      )}
      <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
        {onCancel && (
          <Button small type="button" onClick={onCancel}>
            {t("create.cancel")}
          </Button>
        )}
        <Button
          small
          variant="primary"
          type="submit"
          disabled={mutation.isPending}
        >
          {mutation.isPending ? t("create.saving") : t(submitLabel)}
        </Button>
      </div>
    </form>
  );
}

function PartnerDetail({
  organizationId,
  partner,
  onSaved,
}: Readonly<{
  organizationId: string;
  partner: Partner;
  onSaved: () => void;
}>) {
  const t = useT();
  const [editing, setEditing] = useState(false);

  if (editing) {
    return (
      <div>
        <SectionHeader title={t("partner.edit")} />
        <PartnerForm
          organizationId={organizationId}
          partner={partner}
          submitLabel="record.save"
          onCancel={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            onSaved();
          }}
        />
      </div>
    );
  }

  return (
    <div>
      <div className="list-head">
        <SectionHeader title={t("tab.partner")} />
        <Button
          small
          onClick={() => setEditing(true)}
          data-testid="edit-partner"
        >
          {t("record.edit")}
        </Button>
      </div>
      <dl className="firmo">
        {partner.partner_role && (
          <div>
            <dt>{t("partner.role")}</dt>
            <dd>{t(ROLE_LABELS[partner.partner_role])}</dd>
          </div>
        )}
        <div>
          <dt>{t("partner.certStatus")}</dt>
          <dd>{t(CERT_LABELS[partner.cert_status])}</dd>
        </div>
        {partner.margin_tier && (
          <div>
            <dt>{t("partner.marginTier")}</dt>
            <dd>{t(MARGIN_TIER_LABELS[partner.margin_tier])}</dd>
          </div>
        )}
        <div>
          <dt>{t("partner.stage")}</dt>
          <dd>{t(STAGE_LABELS[partner.relationship_stage])}</dd>
        </div>
        {partner.next_step && (
          <div>
            <dt>{t("partner.nextStep")}</dt>
            <dd>{partner.next_step}</dd>
          </div>
        )}
        {partner.served_segments && partner.served_segments.length > 0 && (
          <div>
            <dt>{t("partner.servedSegments")}</dt>
            <dd>{partner.served_segments.join(", ")}</dd>
          </div>
        )}
      </dl>
    </div>
  );
}

export function PartnerTab({
  organizationId,
}: Readonly<{ organizationId: string }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["partner", organizationId],
    queryFn: () => fetchPartner(organizationId),
  });

  function invalidateAfterSave() {
    queryClient.invalidateQueries({ queryKey: ["partner", organizationId] });
    queryClient.invalidateQueries({
      queryKey: ["organization", organizationId],
    });
    queryClient.invalidateQueries({ queryKey: ["organizations"] });
  }

  return (
    <QueryGate query={query}>
      {(partner) =>
        partner ? (
          <PartnerDetail
            organizationId={organizationId}
            partner={partner}
            onSaved={invalidateAfterSave}
          />
        ) : (
          <div>
            <SectionHeader title={t("tab.partner")} />
            <EmptyState>{t("partner.none")}</EmptyState>
            <div style={{ marginTop: 16 }}>
              <SectionHeader title={t("partner.setup")} />
              <PartnerForm
                organizationId={organizationId}
                submitLabel="create.save"
                onSaved={invalidateAfterSave}
              />
            </div>
          </div>
        )
      }
    </QueryGate>
  );
}

async function fetchPartnersPage(
  query: ListQuery,
  cursor: string | null,
): Promise<ListPage<Partner>> {
  const { data, error } = await api.GET("/partners", {
    params: {
      query: {
        sort: query.sort || undefined,
        cursor: cursor || undefined,
        limit: 50,
        partner_role: asPartnerRole(query.filters.partner_role ?? ""),
        cert_status: asCertStatus(query.filters.cert_status ?? ""),
      },
    },
  });
  if (error) {
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

export function PartnersScreen() {
  const t = useT();
  const state = useListQuery<Partner>({
    key: "partners",
    initialSort: "-created_at",
    fetchPage: fetchPartnersPage,
  });
  const { query, setQuery } = state;

  return (
    <div className="wrap">
      <div className="list-head">
        <SectionHeader title={t("nav.partners")} />
      </div>
      <ListToolbar
        query={query}
        setQuery={setQuery}
        sortOptions={[{ value: "-created_at", label: "list.sortNewest" }]}
        filters={[
          {
            kind: "select",
            key: "partner_role",
            label: "partner.role",
            options: PARTNER_ROLES.map((role) => ({
              value: role,
              label: ROLE_LABELS[role],
            })),
          },
          {
            kind: "select",
            key: "cert_status",
            label: "partner.certStatus",
            options: CERT_STATUSES.map((status) => ({
              value: status,
              label: CERT_LABELS[status],
            })),
          },
        ]}
      />
      <ListGate state={state} empty={t("common.empty")}>
        {(rows) => (
          <DataTable
            columns={[
              {
                key: "org",
                header: t("org.name"),
                render: (partner: Partner) => partner.organization_id,
              },
              {
                key: "role",
                header: t("partner.role"),
                render: (partner: Partner) =>
                  partner.partner_role
                    ? t(ROLE_LABELS[partner.partner_role])
                    : "",
              },
              {
                key: "cert",
                header: t("partner.certStatus"),
                render: (partner: Partner) =>
                  t(CERT_LABELS[partner.cert_status]),
              },
              {
                key: "stage",
                header: t("partner.stage"),
                render: (partner: Partner) =>
                  t(STAGE_LABELS[partner.relationship_stage]),
              },
            ]}
            rows={rows}
            rowKey={(partner) => partner.organization_id}
            onRowClick={(partner) =>
              navigate({ screen: "companies", id: partner.organization_id })
            }
          />
        )}
      </ListGate>
    </div>
  );
}
