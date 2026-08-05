/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import type { CompanyDraft } from "../onboarding";
import { changeDraftField, EMPTY_DRAFT } from "../onboarding";
import type { SuggestedCompanyChange } from "../onboarding-read";
import { draftWithDecidedLegalEntity } from "./company-proposal";
import { useClarifyAnswers } from "./use-clarify-answers";

// The legal-entity clarify authorizes exactly legal_name (the contract's own
// rule: a selected_option verifies one field+value tuple and nothing else),
// so address and registration number never ride in the server's reply. This
// hook is where the read's own richer candidate (legal_entities, matched by
// the authorized name) fills them in instead — never for any other clarify,
// and never for a candidate the server did not just authorize.

type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type LegalEntity = components["schemas"]["CompanySiteReadLegalEntity"];
type Clarify = components["schemas"]["OnboardingClarify"];

const gradionEntity: LegalEntity = {
  name: "Gradion Co., Ltd.",
  registered_address: "Level 12, Bitexco Tower, District 1, Ho Chi Minh City",
  register_number: "0318 447 291",
  evidence_snippet: "Gradion Co., Ltd. · Company Limited · 0318 447 291",
  source_url: "https://gradion.com/legal-notice",
};

const entityClarify: Clarify = {
  id: "clarify:legal_name:1",
  question: "Which legal entity is this installation for?",
  field: "legal_name",
  options: [
    { value: gradionEntity.name, label: gradionEntity.name },
    { value: "Some Other GmbH", label: "Some Other GmbH" },
  ],
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function wrapper({ children }: Readonly<{ children: ReactNode }>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

// One reply shape covers every test: only legal_name ever comes back
// authorized, exactly as the contract promises.
function stubAuthorizedReply() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const body = (await request.clone().json()) as {
        selected_option?: { field: string; value: string };
      };
      const selected = body.selected_option;
      return jsonResponse({
        kind: "clarification",
        act: "company",
        message: "Recorded.",
        proposed_changes: selected
          ? [{ ...selected, reason: "You chose this." }]
          : [],
        citations: [],
        remaining_required_fields: [],
        available_action: "confirm_company",
        ai_runtime: {
          currency: "USD",
          call_attempts: 1,
          tokens_in: 10,
          tokens_out: 5,
          latency_ms: 100,
          estimated_cost_microusd: 0,
          unpriced_calls: 0,
          models: [],
        },
      });
    }),
  );
}

// Both collaborators are the REAL ones the company act wires in: the
// ordinary change path is a changeDraftField loop over the authorized
// changes, and an entity pick goes through draftWithDecidedLegalEntity.
// Stubbing either would leave the one thing these tests are about — which
// path a decision takes, and what provenance it leaves behind — unexercised.
function setupHook(
  legalEntities: readonly LegalEntity[],
  clarify: Clarify = entityClarify,
  applyLegalEntityOverride?: (entity: LegalEntity) => void,
  startingDraft: CompanyDraft = EMPTY_DRAFT,
) {
  const proposalRef: { current: Proposal } = {
    current: { ready: true, open_questions: [clarify] },
  };
  const draftRef = { current: startingDraft };
  const applyChanges = vi.fn((changes: readonly SuggestedCompanyChange[]) => {
    for (const change of changes) {
      draftRef.current = changeDraftField(
        draftRef.current,
        change.field,
        change.value,
      );
    }
  });
  const applyLegalEntity = vi.fn(
    applyLegalEntityOverride ??
      ((entity: LegalEntity) => {
        draftRef.current = draftWithDecidedLegalEntity(
          draftRef.current,
          entity,
        );
      }),
  );
  const legalEntitiesRef = { current: legalEntities };
  const { result } = renderHook(
    () =>
      useClarifyAnswers({
        locale: "en",
        proposalRef,
        draftRef,
        legalEntitiesRef,
        history: () => [],
        applyChanges,
        applyLegalEntity,
      }),
    { wrapper },
  );
  return { result, draftRef, applyChanges, applyLegalEntity };
}

describe("useClarifyAnswers — the legal-entity fill", () => {
  // Pins the whole wiring, not just the fill in isolation: a picked entity
  // must both RECORD as an ordinary authorized answer (no failure, the
  // choice sitting in answers) AND fill address/registration number — the
  // regression this guards is exactly a caller that supplies legalEntitiesRef
  // and applyLegalEntity but something between them throws, which used to
  // surface as a raw, un-actionable error instead of a recorded choice.
  it("records the answer and fills address and registration number from the entity the server just authorized", async () => {
    stubAuthorizedReply();
    const { result, draftRef, applyLegalEntity } = setupHook([gradionEntity]);

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() =>
      expect(applyLegalEntity).toHaveBeenCalledWith(gradionEntity),
    );
    // The choice itself recorded cleanly: no failure, and it is still the
    // answer on file (rollback never ran).
    expect(result.current.failure).toBeNull();
    expect(result.current.answers).toContainEqual(
      expect.objectContaining({
        clarifyId: entityClarify.id,
        field: "legal_name",
        value: gradionEntity.name,
      }),
    );
    expect(draftRef.current.values.registered_address).toBe(
      gradionEntity.registered_address,
    );
    expect(draftRef.current.values.register_vat).toBe(
      gradionEntity.register_number,
    );
    // Grounded, not typed by hand — the review must show this as the site's
    // own evidence, not as something the human entered.
    expect(draftRef.current.grounded.registered_address).toMatchObject({
      source_kind: "url",
      source_url: gradionEntity.source_url,
    });
  });

  // One decision, one provenance. The pick settles three fields at once and
  // the server authorizes only the name; sending that name down the ordinary
  // change path is what used to stamp it as something the human typed, so a
  // single click left the address and registration number reading as the
  // site's evidence and the name beside them reading as hand-entered.
  it("leaves every field the one pick settled with the same provenance, the authorized name included", async () => {
    stubAuthorizedReply();
    const { result, draftRef } = setupHook([gradionEntity]);

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() =>
      expect(draftRef.current.values.legal_name).toBe(gradionEntity.name),
    );
    for (const field of [
      "legal_name",
      "registered_address",
      "register_vat",
    ] as const) {
      expect(draftRef.current.grounded[field]).toMatchObject({
        source_kind: "url",
        source_url: gradionEntity.source_url,
      });
      // The human chose among the read's own candidates rather than writing
      // a value, so nothing here is theirs to have asserted.
      expect(draftRef.current.edited.has(field)).toBe(false);
    }
  });

  // Answering the question is the decision ABOUT the legal name, so it wins
  // over a name typed earlier — otherwise the click reads as ignored, and
  // the details it does fill would describe a different company than the
  // name left standing above them.
  it("settles the name the human just chose over one they typed earlier, and stops calling it their own", async () => {
    stubAuthorizedReply();
    const typed = changeDraftField(
      EMPTY_DRAFT,
      "legal_name",
      "Gradion, roughly",
    );
    const { result, draftRef } = setupHook(
      [gradionEntity],
      entityClarify,
      undefined,
      typed,
    );

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() =>
      expect(draftRef.current.values.legal_name).toBe(gradionEntity.name),
    );
    expect(draftRef.current.edited.has("legal_name")).toBe(false);
    expect(draftRef.current.values.registered_address).toBe(
      gradionEntity.registered_address,
    );
  });

  it("still records the answer, with no leaked exception message, if the entity fill itself throws", async () => {
    stubAuthorizedReply();
    const throwing = () => {
      throw new TypeError("Cannot read properties of undefined (reading 'x')");
    };
    const { result, draftRef } = setupHook(
      [gradionEntity],
      entityClarify,
      throwing,
    );

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(result.current.authorizing).toBe(false));
    // The authorization itself succeeded regardless — a bug in the fill is
    // never reported as if the choice had failed, and never as a raw
    // exception message either.
    expect(result.current.failure).toBeNull();
    expect(result.current.answers).toContainEqual(
      expect.objectContaining({ clarifyId: entityClarify.id }),
    );
    // The choice is on record server-side either way, so it still has to
    // reach the draft: the ordinary change path carries it when the
    // grounded one cannot.
    expect(draftRef.current.values.legal_name).toBe(gradionEntity.name);
  });

  it("never fills anything when no candidate on the read matches the authorized value", async () => {
    stubAuthorizedReply();
    const { result, draftRef, applyLegalEntity } = setupHook([]);

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(result.current.authorizing).toBe(false));
    expect(applyLegalEntity).not.toHaveBeenCalled();
    expect(draftRef.current.values.registered_address).toBe("");
  });

  it("never triggers the entity fill for a clarify over a different field, even when the value happens to match a candidate's name", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          kind: "clarification",
          act: "company",
          message: "Recorded.",
          proposed_changes: [
            { field: "display_name", value: gradionEntity.name, reason: "x" },
          ],
          citations: [],
          remaining_required_fields: [],
          available_action: "confirm_company",
          ai_runtime: {
            currency: "USD",
            call_attempts: 1,
            tokens_in: 10,
            tokens_out: 5,
            latency_ms: 100,
            estimated_cost_microusd: 0,
            unpriced_calls: 0,
            models: [],
          },
        }),
      ),
    );
    const nameClarify: Clarify = {
      id: "clarify:display_name:1",
      question: "What should we call your company?",
      field: "display_name",
      options: [{ value: gradionEntity.name, label: gradionEntity.name }],
    };
    const { result, applyLegalEntity } = setupHook(
      [gradionEntity],
      nameClarify,
    );

    act(() => {
      result.current.answerClarify(nameClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(result.current.authorizing).toBe(false));
    expect(applyLegalEntity).not.toHaveBeenCalled();
  });
});

describe("useClarifyAnswers — honest failures", () => {
  // The request path (mutationFn) already wraps a server problem in
  // problemMessage before it becomes the shown detail; anything that
  // reaches onError WITHOUT going through that wrap (a client-side bug, a
  // network layer throwing something unexpected) must never hand its raw
  // .message to the reader.
  it("never surfaces a raw exception message when something unexpected breaks the round trip", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError(
          "Cannot read properties of undefined (reading 'x')",
        );
      }),
    );
    const { result } = setupHook([]);

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(result.current.failure).not.toBeNull());
    expect(result.current.failure).toEqual({ kind: "unconfirmed" });
  });
});
