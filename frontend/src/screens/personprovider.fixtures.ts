import type { components } from "../api/schema";

// The provider snapshot every person-page gallery needs, in ONE place.
//
// The research drawer's stories and the Research tab's stories both show what
// a completed purchase looks like, and both had their own copy of it — two
// fixtures claiming to be the same run, free to drift into disagreeing about
// what the provider returned. A fixture is a claim about the payload; a second
// copy is a second claim.

export const completedProviderRun: components["schemas"]["ProviderRun"] = {
  id: "run-1",
  subject_kind: "person",
  person_id: "p-1",
  provider: "surfe",
  trigger: "manual",
  state: "completed",
  skip_reason: null,
  connection_version: 1,
  configuration_snapshot: {
    mode: "on_demand",
    preset: "professional_only",
    automatic_individual_create: true,
    automatic_import: false,
    categories: { email: true, mobile: true },
  },
  requested_categories: ["email", "mobile"],
  reservations: [{ pool: "email", reserved_credits: 1, actual_credits: 1 }],
  claims_unwritten: false,
  submitted_at: "2026-08-12T09:00:00Z",
  completed_at: "2026-08-12T09:02:00Z",
  safe_status_code: null,
  created_at: "2026-08-12T09:00:00Z",
  updated_at: "2026-08-12T09:02:00Z",
};

export const providerCompletedProfile: components["schemas"]["PersonProviderProfile"] =
  {
    state: "completed",
    provider: "surfe",
    retrieved_at: "2026-08-12T09:02:00Z",
    safe_status_code: null,
    // Both categories were asked for and both came back, so nothing here is
    // reported as skipped: `mobile_phones` below is populated, and claiming it
    // as "not requested" beside a value the run actually returned would say
    // two contradictory things about the same run.
    categories_not_requested: [],
    emails: [
      {
        value: "dana.buyer@surfe.example",
        email_type: "professional",
        email_type_source: "provider",
        validation_status: "valid",
      },
    ],
    mobile_phones: [{ value: "+491701234567", confidence: 0.82 }],
    linkedin_url: "https://linkedin.com/in/danabuyer",
    current_employment: {
      company_name: "Brandt Automotive GmbH",
      company_domain: "brandt-automotive.example",
      job_title: "Head of Fleet",
    },
    job_history: [
      {
        company_name: "Voss Logistics",
        job_title: "Fleet Coordinator",
        started_at: "2018-01-01T00:00:00Z",
        ended_at: "2022-02-01T00:00:00Z",
      },
    ],
    location: "Munich, Germany",
    city: "Munich",
    region: "Bavaria",
    country: "DE",
    departments: ["Operations"],
    seniorities: ["Head"],
    latest_run: completedProviderRun,
  };
