import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import { restorePlan } from "./restore";

// The restore decision is pure: server rows in, a start plan out. This suite
// pins the member signal (the state row's `path`, with company-exists only
// the fallback), the step-to-landing mapping, the honored voice skip, and
// the recap derivation (server facts, never persisted narration).

type OnboardingState = components["schemas"]["OnboardingState"];
type CompanyProfile = components["schemas"]["CompanyProfile"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];

function readRow(status: CompanySiteRead["status"]): CompanySiteRead {
  return {
    id: "018f3a1b-0000-7000-8000-0000000000c3",
    target_kind: "onboarding",
    organization_id: null,
    root_url: "https://gradion.com",
    status,
    status_code: null,
    status_detail: null,
    next_attempt_at: null,
    phase: null,
    pages_read: 12,
    pages: [],
    profile_fields: [
      {
        field: "legal_name",
        value: "Gradion GmbH",
        evidence_snippet: "© 2026 Gradion GmbH",
        source_kind: "url",
        source_url: "https://gradion.com",
        confidence: 0.9,
      },
    ],
    facts: [],
    comparisons: [],
    people: [],
    legal_entities: [],
    warnings: [],
    draft_version: 2,
    proposal_hash: "proposal-2",
    created_at: "2026-07-22T08:00:00Z",
    updated_at: "2026-07-22T08:10:00Z",
  };
}

function stateRow(overrides: Partial<OnboardingState> = {}): OnboardingState {
  return {
    path: "creator",
    step: "read",
    source_mode: "website",
    website_url: "https://gradion.com",
    site_read_id: null,
    company_draft: {},
    selected_fact_keys: [],
    voice_skipped: false,
    connect_skipped: false,
    version: 3,
    completed_at: null,
    created_at: "2026-07-22T08:00:00Z",
    updated_at: "2026-07-22T09:00:00Z",
    ...overrides,
  };
}

const profile: CompanyProfile = {
  organization_id: "018f3a1b-0000-7000-8000-0000000000a1",
  display_name: "Gradion",
};

const emptyVoice = { built: false, summary: null };

function words(total: number) {
  return {
    built: false,
    summary: {
      total_words: total,
      target_words: 30000 as const,
      maturity: "collecting" as const,
      quality_band: "thin" as const,
      source_count: 1,
      register_words: {},
    },
  };
}

describe("restorePlan", () => {
  it("starts a fresh creator at the company act with no recap", () => {
    expect(
      restorePlan({
        state: null,
        profile: null,
        voice: null,
        read: null,
        routeConnect: false,
      }),
    ).toEqual({
      kind: "start",
      memberPath: false,
      companyConfirmed: false,
      resumeTarget: null,
      adoptRead: null,
      recap: [],
    });
  });

  it("falls back to company-exists as the member signal ONLY without a state row", () => {
    expect(
      restorePlan({
        state: null,
        profile,
        voice: null,
        read: null,
        routeConnect: false,
      }),
    ).toMatchObject({ memberPath: true });
    // A returning creator has both a saved company and a state row saying
    // creator; the profile must not demote them to the member path.
    expect(
      restorePlan({
        state: stateRow({ step: "voice" }),
        profile,
        voice: emptyVoice,
        read: null,
        routeConnect: false,
      }),
    ).toMatchObject({ memberPath: false, resumeTarget: "vo.invite" });
  });

  it("reads the member path from the state row's path field", () => {
    expect(
      restorePlan({
        state: stateRow({ path: "member", step: "connect" }),
        profile,
        voice: null,
        read: null,
        routeConnect: false,
      }),
    ).toMatchObject({
      memberPath: true,
      companyConfirmed: true,
      resumeTarget: "cn.consent",
    });
  });

  it("keeps the company act open for step read and confirm", () => {
    for (const step of ["read", "confirm"] as const) {
      expect(
        restorePlan({
          state: stateRow({ step }),
          profile: null,
          voice: null,
          read: null,
          routeConnect: false,
        }),
      ).toMatchObject({
        companyConfirmed: false,
        resumeTarget: null,
        recap: [],
      });
    }
  });

  it("resumes collecting when the server corpus already holds words", () => {
    const plan = restorePlan({
      state: stateRow({ step: "voice" }),
      profile,
      voice: words(1240),
      read: null,
      routeConnect: false,
    });
    expect(plan).toMatchObject({ resumeTarget: "vo.collecting" });
    if (plan.kind !== "start") {
      throw new Error("expected a start plan");
    }
    expect(plan.recap).toContainEqual(
      expect.objectContaining({
        i18nKey: "ob.conv.recap.corpus",
        params: { words: 1240 },
      }),
    );
  });

  it("honors a recorded voice skip", () => {
    expect(
      restorePlan({
        state: stateRow({ step: "voice", voice_skipped: true }),
        profile,
        voice: emptyVoice,
        read: null,
        routeConnect: false,
      }),
    ).toMatchObject({ resumeTarget: "vo.skipped" });
    const results = restorePlan({
      state: stateRow({ step: "results", voice_skipped: true }),
      profile,
      voice: emptyVoice,
      read: null,
      routeConnect: false,
    });
    expect(results).toMatchObject({ resumeTarget: "re.recap" });
    if (results.kind !== "start") {
      throw new Error("expected a start plan");
    }
    expect(results.recap).toContainEqual(
      expect.objectContaining({ i18nKey: "ob.conv.recap.voiceSkipped" }),
    );
  });

  it("recaps a built voice and names an unsaved company honestly", () => {
    const plan = restorePlan({
      state: stateRow({ step: "connect" }),
      profile: null,
      voice: { ...words(2000), built: true },
      read: null,
      routeConnect: false,
    });
    expect(plan).toMatchObject({ resumeTarget: "cn.consent" });
    if (plan.kind !== "start") {
      throw new Error("expected a start plan");
    }
    expect(plan.recap.map((entry) => entry.i18nKey)).toEqual([
      "ob.conv.recap.back",
      "ob.conv.recap.companyUnsaved",
      "ob.conv.recap.voiceBuilt",
    ]);
  });

  it("the OAuth return deep link reopens the connect act", () => {
    expect(
      restorePlan({
        state: stateRow({ step: "voice" }),
        profile,
        voice: emptyVoice,
        read: null,
        routeConnect: true,
      }),
    ).toMatchObject({ resumeTarget: "cn.consent" });
  });

  it("adopts a persisted terminal read so the finished review stays reachable", () => {
    for (const status of ["ready", "partial"] as const) {
      const plan = restorePlan({
        state: stateRow({ step: "confirm" }),
        profile: null,
        voice: null,
        read: readRow(status),
        routeConnect: false,
      });
      expect(plan).toMatchObject({
        companyConfirmed: false,
        adoptRead: { id: readRow(status).id },
      });
      if (plan.kind !== "start") {
        throw new Error("expected a start plan");
      }
      expect(plan.recap).toContainEqual(
        expect.objectContaining({
          i18nKey: "ob.conv.recap.readTerminal",
          params: { host: "gradion.com", count: 1 },
        }),
      );
    }
  });

  it("adopts a still-running read so polling resumes", () => {
    const plan = restorePlan({
      state: stateRow(),
      profile: null,
      voice: null,
      read: readRow("reading"),
      routeConnect: false,
    });
    expect(plan).toMatchObject({ adoptRead: { id: readRow("reading").id } });
    if (plan.kind !== "start") {
      throw new Error("expected a start plan");
    }
    expect(plan.recap).toContainEqual(
      expect.objectContaining({
        i18nKey: "ob.conv.recap.readReading",
        params: { host: "gradion.com", pages: 12 },
      }),
    );
  });

  it("reopens fresh with an honest line for a failed or deferred read", () => {
    for (const [status, key] of [
      ["failed", "ob.conv.recap.readFailed"],
      ["deferred", "ob.conv.recap.readDeferred"],
    ] as const) {
      const plan = restorePlan({
        state: stateRow(),
        profile: null,
        voice: null,
        read: readRow(status),
        routeConnect: false,
      });
      expect(plan).toMatchObject({ adoptRead: null });
      if (plan.kind !== "start") {
        throw new Error("expected a start plan");
      }
      expect(plan.recap).toContainEqual(
        expect.objectContaining({ i18nKey: key }),
      );
    }
  });

  it("a completed journey leaves onboarding", () => {
    expect(
      restorePlan({
        state: stateRow({ step: "complete" }),
        profile,
        voice: null,
        read: null,
        routeConnect: false,
      }),
    ).toEqual({ kind: "complete" });
  });
});
