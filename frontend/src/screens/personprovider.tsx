import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button } from "../design-system/atoms";
import { EvidenceMark } from "../design-system/evidencemark";
import { useT } from "../i18n";
import {
  canEnrichNow,
  isRunning,
  profileLabel,
  profileTone,
} from "./provider-status";

// What a licensed data provider was PAID to tell us about this person
// (ADR-0101), shown BESIDE the canonical record and never folded into it.
//
// Every value carries a provenance mark, because a bought value and one a
// colleague typed are different kinds of fact and a page that renders them
// alike invites a rep to treat a purchase as a confirmation.

type Person360 = components["schemas"]["Person360"];
type Profile = components["schemas"]["PersonProviderProfile"];

/** The mark every value in this section carries: bought from a named third
 *  party, on a date. `connector` rather than `agent` — nothing inferred this,
 *  somebody sold it to us. */
function boughtFrom(profile: Profile) {
  return {
    provenance: {
      kind: "connector" as const,
      connector: profile.provider ?? "provider",
    },
    at: profile.retrieved_at ?? null,
  };
}

export function PersonProviderSection({
  personId,
  profile,
}: Readonly<{ personId: string; profile: Profile | undefined }>) {
  const t = useT();
  if (!profile) {
    // Absent means the caller lacks the grant — `sections_omitted` names it —
    // which is not the same as empty, so the section stays away entirely
    // rather than claiming this person has nothing.
    return null;
  }
  return (
    <section className="pe-card">
      <header
        style={{ display: "flex", alignItems: "center", gap: "var(--space-2)" }}
      >
        <h3>{t("provider.profile.title")}</h3>
        <Badge tone={profileTone(profile.state)}>
          {t(profileLabel(profile.state))}
        </Badge>
      </header>
      <ProviderValues profile={profile} />
      <EnrichNow personId={personId} profile={profile} />
    </section>
  );
}

function ProviderValues({ profile }: Readonly<{ profile: Profile }>) {
  const t = useT();
  const source = boughtFrom(profile);
  return (
    <>
      {profile.emails.length > 0 && (
        <div>
          <h4>{t("provider.profile.emails")}</h4>
          {profile.emails.map((email) => (
            <div key={email.value}>
              <EvidenceMark value={email.value} source={source} />
              {email.email_type && (
                <span className="muted">
                  {/* Which label this is, and WHOSE it is. An address the
                      provider did not classify is labelled from what we
                      asked for, and saying so is the whole point of the
                      distinction. */}
                  {email.email_type_source === "provider"
                    ? t("provider.profile.emailType.provider", {
                        type: email.email_type,
                      })
                    : t("provider.profile.emailType.requested", {
                        type: email.email_type,
                      })}
                </span>
              )}
            </div>
          ))}
        </div>
      )}
      {profile.mobile_phones.length > 0 && (
        <div>
          <h4>{t("provider.profile.mobiles")}</h4>
          {profile.mobile_phones.map((phone) => (
            <div key={phone.value}>
              <EvidenceMark value={phone.value} source={source} />
              {phone.confidence != null && (
                <span className="muted">
                  {t("provider.profile.confidence", {
                    percent: String(Math.round(phone.confidence * 100)),
                  })}
                </span>
              )}
            </div>
          ))}
        </div>
      )}
      {profile.linkedin_url && (
        <div>
          <h4>{t("provider.profile.linkedin")}</h4>
          <EvidenceMark value={profile.linkedin_url} source={source} />
        </div>
      )}
      {profile.current_employment && (
        <div>
          <h4>{t("provider.profile.employment")}</h4>
          <EvidenceMark
            value={[
              profile.current_employment.job_title,
              profile.current_employment.company_name,
            ]
              .filter(Boolean)
              .join(" · ")}
            source={source}
          />
        </div>
      )}
      {profile.job_history.length > 0 && (
        <div>
          <h4>{t("provider.profile.jobHistory")}</h4>
          {profile.job_history.map((job) => (
            <div key={`${job.company_name}-${job.job_title ?? ""}`}>
              <EvidenceMark
                value={[job.job_title, job.company_name]
                  .filter(Boolean)
                  .join(" · ")}
                source={source}
              />
            </div>
          ))}
        </div>
      )}
      {profile.location && (
        <div>
          <h4>{t("provider.profile.location")}</h4>
          <EvidenceMark value={profile.location} source={source} />
        </div>
      )}
      {profile.departments.length > 0 && (
        <div>
          <h4>{t("provider.profile.departments")}</h4>
          <EvidenceMark
            value={profile.departments.join(", ")}
            source={source}
          />
        </div>
      )}
      {profile.seniorities.length > 0 && (
        <div>
          <h4>{t("provider.profile.seniorities")}</h4>
          <EvidenceMark
            value={profile.seniorities.join(", ")}
            source={source}
          />
        </div>
      )}
      {profile.categories_not_requested.length > 0 && (
        // The difference between "we asked and they had nothing" and "we
        // never asked". Without this line a blank field reads as the first
        // when it is often the second.
        <p className="muted">
          {t("provider.profile.notRequested", {
            categories: profile.categories_not_requested.join(", "),
          })}
        </p>
      )}
    </>
  );
}

function EnrichNow({
  personId,
  profile,
}: Readonly<{ personId: string; profile: Profile }>) {
  const t = useT();
  const queryClient = useQueryClient();

  const enrich = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/people/{id}/enrichment-runs", {
        params: { path: { id: personId } },
        body: { provider: profile.provider ?? "surfe" },
      });
      if (error) {
        throw error;
      }
      return data;
    },
    onSuccess: () => {
      // The run is durable and the provider has not been called yet, so the
      // page re-reads and the poll below picks the run up from there.
      void queryClient.invalidateQueries({ queryKey: ["person360", personId] });
    },
  });

  // While a run is moving, ask again — and ONLY while it is moving. Polling a
  // terminal run spends requests to learn nothing.
  useQuery({
    queryKey: ["person360-provider-poll", personId],
    queryFn: async () => {
      await queryClient.invalidateQueries({
        queryKey: ["person360", personId],
      });
      return null;
    },
    enabled: isRunning(profile.state),
    refetchInterval: 2500,
  });

  if (!canEnrichNow(profile.state)) {
    return null;
  }
  return (
    <Button
      small
      type="button"
      disabled={enrich.isPending}
      onClick={() => enrich.mutate()}
    >
      <Search size={15} aria-hidden="true" /> {t("provider.profile.enrichNow")}
    </Button>
  );
}

/** The section's own read of the assembled page, for a caller that already
 *  holds it. Exported so personpage can pass what it fetched rather than the
 *  section fetching the whole 360 again. */
export function providerProfileOf(
  view: Person360 | undefined,
): Profile | undefined {
  return view?.provider_profile;
}
