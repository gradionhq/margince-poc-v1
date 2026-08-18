// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button } from "../design-system/atoms";
import { EvidenceMark } from "../design-system/evidencemark";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import { throwProblem } from "./common";
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
    // The record page's own card, with the provider's state in the header's
    // action slot. It used to be a bare `<section className="pe-card">` — a
    // class no stylesheet defines — which read as unframed text the moment it
    // was shown anywhere but inside a drawer.
    <Panel
      title={t("provider.profile.title")}
      titleAction={
        <Badge tone={profileTone(profile.state)}>
          {t(profileLabel(profile.state))}
        </Badge>
      }
      actions={<EnrichNow personId={personId} profile={profile} />}
    >
      <PanelBody>
        <ProviderValues profile={profile} />
        <RunWatch personId={personId} profile={profile} />
      </PanelBody>
    </Panel>
  );
}

// A component rather than a hook call in the section, so the watch mounts
// only where a profile exists — the section returns early without one, and a
// hook cannot sit behind that return.
function RunWatch({
  personId,
  profile,
}: Readonly<{ personId: string; profile: Profile }>) {
  useRunWatch(personId, profile);
  return null;
}

function ProviderValues({ profile }: Readonly<{ profile: Profile }>) {
  const t = useT();
  const source = boughtFrom(profile);
  return (
    <>
      {profile.emails.length > 0 && (
        <div>
          <Eyebrow as="h4">{t("provider.profile.emails")}</Eyebrow>
          {profile.emails.map((email) => (
            <div key={email.value}>
              <EvidenceMark value={email.value} source={source} />{" "}
              {email.email_type && (
                <span className="t-caption">
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
          <Eyebrow as="h4">{t("provider.profile.mobiles")}</Eyebrow>
          {profile.mobile_phones.map((phone) => (
            <div key={phone.value}>
              <EvidenceMark value={phone.value} source={source} />{" "}
              {phone.confidence != null && (
                <span className="t-caption">
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
          <Eyebrow as="h4">{t("provider.profile.linkedin")}</Eyebrow>
          <EvidenceMark value={profile.linkedin_url} source={source} />
        </div>
      )}
      {profile.current_employment && (
        <div>
          <Eyebrow as="h4">{t("provider.profile.employment")}</Eyebrow>
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
          <Eyebrow as="h4">{t("provider.profile.jobHistory")}</Eyebrow>
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
          <Eyebrow as="h4">{t("provider.profile.location")}</Eyebrow>
          <EvidenceMark value={profile.location} source={source} />
        </div>
      )}
      {profile.departments.length > 0 && (
        <div>
          <Eyebrow as="h4">{t("provider.profile.departments")}</Eyebrow>
          <EvidenceMark
            value={profile.departments.join(", ")}
            source={source}
          />
        </div>
      )}
      {profile.seniorities.length > 0 && (
        <div>
          <Eyebrow as="h4">{t("provider.profile.seniorities")}</Eyebrow>
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
        <p className="t-caption">
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
      // page re-reads and RunWatch picks the run up from there.
      void queryClient.invalidateQueries({ queryKey: ["person360", personId] });
    },
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

/** Watches a run that is still moving and refreshes the page when it lands.
 *
 *  It polls the RUN, not the page, and reads whether to continue from the
 *  response it just received — which is the only version that can stop
 *  itself. Deciding from the `profile` prop instead would freeze the answer
 *  at whatever state mounted the poll: the prop only changes when the page
 *  refetches, which is the thing the poll exists to cause.
 */
function useRunWatch(personId: string, profile: Profile) {
  const queryClient = useQueryClient();
  const runId = profile.latest_run?.id;
  useQuery({
    queryKey: ["provider-run", personId, runId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/people/{id}/enrichment-runs/{run_id}",
        { params: { path: { id: personId, run_id: runId ?? "" } } },
      );
      if (error) {
        throwProblem(error);
      }
      // A run that has stopped moving is the page's cue to re-read: the
      // claims land in the same transaction as the terminal state, so by
      // now the section has something new to show.
      if (data && !RUNNING_RUN_STATES.has(data.state)) {
        void queryClient.invalidateQueries({
          queryKey: ["person360", personId],
        });
      }
      return data;
    },
    enabled: runId != null && isRunning(profile.state),
    refetchInterval: (query) =>
      query.state.data && RUNNING_RUN_STATES.has(query.state.data.state)
        ? 2500
        : false,
  });
}

/** The run states that are still moving. `submitting` counts: the run is
 *  mid-flight to the provider, and treating it as finished would offer a
 *  second "look them up" while the first is still out. */
const RUNNING_RUN_STATES = new Set<
  components["schemas"]["ProviderRun"]["state"]
>(["queued", "submitting", "in_progress"]);
