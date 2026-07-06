import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Badge, DataTable, SectionHeader } from "../design-system/atoms";
import { RecordView } from "../design-system/composed";
import { ProvenanceTag } from "../design-system/trust";
import { useT } from "../i18n";
import { problemMessage, provenanceOf, QueryGate } from "./common";
import { activityTimeline } from "./people";

// Companies list + company 360 (B-EP09.10a/b). Firmographics render
// evidence-or-omit: a field with no stored value is absent, never guessed.

type Organization = components["schemas"]["Organization"];

export function CompaniesScreen() {
  const t = useT();
  const query = useQuery({
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
  return (
    <div className="wrap">
      <SectionHeader title={t("nav.companies")} />
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <DataTable
            columns={[
              {
                key: "name",
                header: t("org.name"),
                render: (org: Organization) => (
                  <strong>{org.display_name}</strong>
                ),
              },
              {
                key: "industry",
                header: t("org.industry"),
                render: (org: Organization) => org.industry ?? "",
              },
              {
                key: "size",
                header: t("org.size"),
                render: (org: Organization) => org.size_band ?? "",
              },
              {
                key: "class",
                header: t("org.classification"),
                render: (org: Organization) =>
                  org.classification ? (
                    <Badge>{org.classification}</Badge>
                  ) : null,
              },
              {
                key: "provenance",
                header: t("people.capturedBy"),
                render: (org: Organization) => (
                  <ProvenanceTag provenance={provenanceOf(org.captured_by)} />
                ),
              },
            ]}
            rows={page.data}
            rowKey={(org) => org.id}
            onRowClick={(org) => navigate({ screen: "companies", id: org.id })}
          />
        )}
      </QueryGate>
    </div>
  );
}

export function CompanyScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const orgQuery = useQuery({
    queryKey: ["organization", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const timelineQuery = useQuery({
    queryKey: ["activities", "organization", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: {
          query: { entity_type: "organization", entity_id: id, limit: 20 },
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  return (
    <div className="wrap">
      <QueryGate query={orgQuery}>
        {(org) => (
          <RecordView
            name={org.display_name}
            subtitle={org.legal_name ?? undefined}
            zone="Europe/Berlin"
            badges={
              <>
                {org.classification && <Badge>{org.classification}</Badge>}
                <ProvenanceTag provenance={provenanceOf(org.captured_by)} />
              </>
            }
            timeline={
              timelineQuery.isSuccess
                ? activityTimeline(timelineQuery.data.data)
                : []
            }
          >
            <section className="card" style={{ marginBottom: 16 }}>
              <SectionHeader
                title={t("org.firmographics")}
                sub={t("org.evidenceOrOmit")}
              />
              <dl className="firmo">
                {org.industry && (
                  <div>
                    <dt>{t("org.industry")}</dt>
                    <dd>{org.industry}</dd>
                  </div>
                )}
                {org.size_band && (
                  <div>
                    <dt>{t("org.size")}</dt>
                    <dd>{org.size_band}</dd>
                  </div>
                )}
                {org.domains && org.domains.length > 0 && (
                  <div>
                    <dt>{t("org.domains")}</dt>
                    <dd className="t-mono">
                      {org.domains.map((domain) => domain.domain).join(", ")}
                    </dd>
                  </div>
                )}
              </dl>
            </section>
          </RecordView>
        )}
      </QueryGate>
    </div>
  );
}
