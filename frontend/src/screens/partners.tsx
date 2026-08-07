// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import {
  Button,
  EmptyState,
  Field,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryGate, throwProblem } from "./common";
import { EntityRef } from "./entityref";
import {
  type ListPage,
  type ListQuery,
  ListTable,
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

function asMarginTier(value: string): MarginTier | undefined {
  return (MARGIN_TIERS as readonly string[]).includes(value)
    ? (value as MarginTier)
    : undefined;
}

function asRelationshipStage(value: string): RelationshipStage | undefined {
  return (RELATIONSHIP_STAGES as readonly string[]).includes(value)
    ? (value as RelationshipStage)
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
    throwProblem(error);
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
  // This form only mounts while editing (PartnerDetail/PartnerTab remount it
  // fresh each time `editing` flips true), so the lazy initializer is the
  // only seeding this needs — a re-sync effect keyed on `partner` would
  // re-run on a background refetch of ["partner", organizationId] mid-edit
  // and overwrite whatever the user is typing.
  const [values, setValues] = useState<PartnerFormValues>(() =>
    defaultFormValues(partner),
  );

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
      <Field label={t("partner.role")} required>
        {(control) => (
          <Select
            {...control}
            value={values.partner_role}
            onChange={(value) =>
              setValues({
                ...values,
                partner_role: asPartnerRole(value) ?? values.partner_role,
              })
            }
            options={PARTNER_ROLES.map((role) => ({
              value: role,
              label: t(ROLE_LABELS[role]),
            }))}
          />
        )}
      </Field>
      <Field label={t("partner.certStatus")}>
        {(control) => (
          <Select
            {...control}
            value={values.cert_status}
            onChange={(value) =>
              setValues({
                ...values,
                cert_status: asCertStatus(value) ?? values.cert_status,
              })
            }
            options={CERT_STATUSES.map((status) => ({
              value: status,
              label: t(CERT_LABELS[status]),
            }))}
          />
        )}
      </Field>
      <Field label={t("partner.marginTier")}>
        {(control) => (
          <Select
            {...control}
            value={values.margin_tier}
            onChange={(value) =>
              setValues({
                ...values,
                margin_tier: value
                  ? (asMarginTier(value) ?? values.margin_tier)
                  : "",
              })
            }
            // The clearing entry is a real choice, not a placeholder: a tier once
            // set has to be clearable back to "no tier agreed" (the wire's null).
            // It carries a LABEL — a blank one is an unreadable strip in a drawn
            // list and silence to a screen reader.
            options={[
              { value: "", label: t("field.unset") },
              ...MARGIN_TIERS.map((tier) => ({
                value: tier,
                label: t(MARGIN_TIER_LABELS[tier]),
              })),
            ]}
          />
        )}
      </Field>
      <Field label={t("partner.stage")}>
        {(control) => (
          <Select
            {...control}
            value={values.relationship_stage}
            onChange={(value) =>
              setValues({
                ...values,
                relationship_stage:
                  asRelationshipStage(value) ?? values.relationship_stage,
              })
            }
            options={RELATIONSHIP_STAGES.map((stage) => ({
              value: stage,
              label: t(STAGE_LABELS[stage]),
            }))}
          />
        )}
      </Field>
      <Field label={t("partner.nextStep")}>
        {(control) => (
          <TextInput
            {...control}
            value={values.next_step}
            onChange={(event) =>
              setValues({ ...values, next_step: event.target.value })
            }
          />
        )}
      </Field>
      <Field label={t("partner.nextStepDue")}>
        {(control) => (
          <TextInput
            {...control}
            type="date"
            value={values.next_step_due_at}
            onChange={(event) =>
              setValues({ ...values, next_step_due_at: event.target.value })
            }
          />
        )}
      </Field>
      <Field label={t("partner.servedSegments")}>
        {(control) => (
          <TextInput
            {...control}
            value={values.served_segments}
            placeholder={t("partner.servedSegmentsHint")}
            onChange={(event) =>
              setValues({ ...values, served_segments: event.target.value })
            }
          />
        )}
      </Field>
      {mutation.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(mutation.error, t)}
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
    <section className="card">
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
      <dl className="detail-grid">
        {partner.partner_role && (
          <>
            <dt>{t("partner.role")}</dt>
            <dd>{t(ROLE_LABELS[partner.partner_role])}</dd>
          </>
        )}
        <dt>{t("partner.certStatus")}</dt>
        <dd>{t(CERT_LABELS[partner.cert_status])}</dd>
        {partner.margin_tier && (
          <>
            <dt>{t("partner.marginTier")}</dt>
            <dd>{t(MARGIN_TIER_LABELS[partner.margin_tier])}</dd>
          </>
        )}
        <dt>{t("partner.stage")}</dt>
        <dd>{t(STAGE_LABELS[partner.relationship_stage])}</dd>
        {partner.next_step && (
          <>
            <dt>{t("partner.nextStep")}</dt>
            <dd>{partner.next_step}</dd>
          </>
        )}
        {partner.served_segments && partner.served_segments.length > 0 && (
          <>
            <dt>{t("partner.servedSegments")}</dt>
            <dd>{partner.served_segments.join(", ")}</dd>
          </>
        )}
      </dl>
    </section>
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
          <section className="card">
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
          </section>
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
    throwProblem(error);
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

  return (
    <div className="wrap">
      <ListTable
        state={state}
        unit="unit.partners"
        searchable={false}
        showArchivedToggle={false}
        columns={[
          {
            key: "org",
            header: t("partner.organization"),
            // The Partner payload carries only organization_id; EntityRef
            // hydrates the company name off the org read and backlinks to
            // its 360.
            // Named, not linked: the row's own identity link already goes to
            // this company, and a control inside that link would be invalid
            // markup offering the same destination twice.
            cell: (partner: Partner) => (
              <EntityRef
                kind="organization"
                id={partner.organization_id}
                asText
              />
            ),
            fixed: true,
          },
          {
            key: "role",
            header: t("partner.role"),
            cell: (partner: Partner) =>
              partner.partner_role ? t(ROLE_LABELS[partner.partner_role]) : "",
          },
          {
            key: "cert",
            header: t("partner.certStatus"),
            cell: (partner: Partner) => t(CERT_LABELS[partner.cert_status]),
          },
          {
            key: "stage",
            header: t("partner.stage"),
            cell: (partner: Partner) =>
              t(STAGE_LABELS[partner.relationship_stage]),
          },
        ]}
        rowKey={(partner) => partner.organization_id}
        rowRoute={(partner) => ({
          screen: "companies",
          id: partner.organization_id,
        })}
        chips={[
          {
            key: "partner_role",
            label: "partner.role",
            allLabel: "partner.roleAll",
            options: PARTNER_ROLES.map((role) => ({
              value: role,
              label: ROLE_LABELS[role],
            })),
          },
          {
            key: "cert_status",
            label: "partner.certStatus",
            allLabel: "partner.certStatusAll",
            options: CERT_STATUSES.map((status) => ({
              value: status,
              label: CERT_LABELS[status],
            })),
          },
        ]}
        views={[{ label: "list.sortNewest", sort: "-created_at" }]}
      />
    </div>
  );
}
