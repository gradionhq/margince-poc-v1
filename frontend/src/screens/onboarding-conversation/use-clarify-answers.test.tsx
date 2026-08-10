/** @vitest-environment jsdom */
import { QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { createQueryClient } from "../../app/queryclient";
import { translate } from "../../i18n";
import type { CompanyDraft } from "../onboarding";
import { changeDraftField, EMPTY_DRAFT } from "../onboarding";
import type { SuggestedCompanyChange } from "../onboarding-read";
import { draftWithLegalEntity } from "./company-proposal";
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

// The application's OWN client: a failure this hook does not describe to the
// reader is reported by the client's mutation sink and by nothing else
// (app/queryclient.ts, FE-PARAM-4), so the cases below can only count those
// reports honestly against the same wiring the browser runs.
function wrapper({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <QueryClientProvider client={createQueryClient()}>
      {children}
    </QueryClientProvider>
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  // Unconditionally, not at the end of the case that installed a spy: a case
  // that fails its assertion never reaches its own teardown, and a leaked
  // console spy would silently answer the next case's question for it.
  vi.restoreAllMocks();
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
// changes, and an entity pick goes through draftWithLegalEntity.
// Stubbing either would leave the one thing these tests are about — which
// path a decision takes, and what provenance it leaves behind — unexercised.
function setupHook(
  legalEntities: readonly LegalEntity[],
  clarify: Clarify = entityClarify,
  applyLegalEntityOverride?: (entity: LegalEntity) => void,
  startingDraft: CompanyDraft = EMPTY_DRAFT,
  siblings: readonly Clarify[] = [],
) {
  const proposalRef: { current: Proposal } = {
    current: { ready: true, open_questions: [clarify, ...siblings] },
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
        draftRef.current = draftWithLegalEntity(draftRef.current, entity);
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

  // The option a human clicks is a normalized copy of the candidate's name
  // (the server trims and collapses it before offering it), while the read
  // still carries the name as the page printed it. The server matches the two
  // trimmed when it decides whether the confirmed legal block keeps the site's
  // provenance, so a client comparing them raw calls "picked" absent: address
  // and registration number stay unfilled, and the name the human chose off
  // the page ends up marked as one they typed.
  it("fills from the candidate the server would call picked, whitespace and all", async () => {
    stubAuthorizedReply();
    const padded: LegalEntity = {
      ...gradionEntity,
      name: `  ${gradionEntity.name}\n`,
    };
    const { result, draftRef, applyLegalEntity } = setupHook([padded]);

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(applyLegalEntity).toHaveBeenCalledWith(padded));
    expect(draftRef.current.values.registered_address).toBe(
      padded.registered_address,
    );
    expect(draftRef.current.values.register_vat).toBe(padded.register_number);
    expect(draftRef.current.edited.has("legal_name")).toBe(false);
  });

  // Whitespace INSIDE the name, which trimming cannot reach: the option was
  // built by collapsing every run of it to one space, so the two strings only
  // ever meet under that same rule. Compared trimmed alone, this pick loses
  // the address, the registration number and the imprint's provenance with
  // them, and the chosen name is stamped as one the human typed.
  it("fills from a candidate whose printed name carries doubled whitespace of its own", async () => {
    stubAuthorizedReply();
    const doubled: LegalEntity = {
      ...gradionEntity,
      name: "Gradion  Co.,\tLtd.",
    };
    const doubledClarify: Clarify = {
      ...entityClarify,
      options: [
        { value: "Gradion Co., Ltd.", label: "Gradion Co., Ltd." },
        { value: "Some Other GmbH", label: "Some Other GmbH" },
      ],
    };
    const { result, draftRef, applyLegalEntity } = setupHook(
      [doubled],
      doubledClarify,
    );

    act(() => {
      result.current.answerClarify(doubledClarify.id, "Gradion Co., Ltd.");
    });

    await waitFor(() => expect(applyLegalEntity).toHaveBeenCalledWith(doubled));
    expect(draftRef.current.values.registered_address).toBe(
      doubled.registered_address,
    );
    expect(draftRef.current.values.register_vat).toBe(doubled.register_number);
    expect(draftRef.current.grounded.registered_address).toMatchObject({
      source_kind: "url",
      source_url: doubled.source_url,
    });
    expect(draftRef.current.edited.has("legal_name")).toBe(false);
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

  // A candidate the read could not name is no candidate at all: the fill
  // deliberately settles nothing about legal_name for one (it must never
  // unmark a name the human typed themselves), so taking it would record the
  // clarify as answered while the legal name it answers stays exactly as it
  // was — and would fill address and registration number from an entity
  // nothing on screen identifies.
  it("treats a candidate with no usable name as no candidate, and lets the ordinary change path carry the choice", async () => {
    stubAuthorizedReply();
    const nameless: LegalEntity = {
      name: "   ",
      registered_address:
        "Level 12, Bitexco Tower, District 1, Ho Chi Minh City",
      register_number: "0318 447 291",
      source_url: "https://gradion.com/legal-notice",
    };
    const namelessClarify: Clarify = {
      id: "clarify:legal_name:2",
      question: "Which legal entity is this installation for?",
      field: "legal_name",
      options: [{ value: nameless.name, label: "The entity on the imprint" }],
    };
    const { result, draftRef, applyChanges, applyLegalEntity } = setupHook(
      [nameless],
      namelessClarify,
    );

    act(() => {
      result.current.answerClarify(namelessClarify.id, nameless.name);
    });

    await waitFor(() => expect(result.current.authorizing).toBe(false));
    expect(applyLegalEntity).not.toHaveBeenCalled();
    expect(applyChanges).toHaveBeenCalledWith([
      expect.objectContaining({ field: "legal_name", value: nameless.name }),
    ]);
    // Nothing of the nameless candidate's own block reaches the draft.
    expect(draftRef.current.values.registered_address).toBe("");
    expect(draftRef.current.values.register_vat).toBe("");
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

// One pick settles the whole legal block, so the sibling questions about that
// block stop being open decisions. What matters is WHEN: the retirement is a
// consequence of the server authorizing the choice, so it may not outlive a
// choice the server refuses — a sibling retired at click time and never
// un-retired hides a field nobody decided and lets Continue ride on the old
// draft.
describe("useClarifyAnswers — the siblings one legal pick settles", () => {
  const addressClarify: Clarify = {
    id: "clarify:registered_address:1",
    question: "The imprint lists two addresses. Which one is registered?",
    field: "registered_address",
    options: [
      { value: gradionEntity.registered_address ?? "", label: "The first" },
      { value: "Somewhere else", label: "The second" },
    ],
  };

  it("retires them once the pick is authorized, marked as the machine's conclusion rather than the reader's", async () => {
    stubAuthorizedReply();
    const { result } = setupHook(
      [gradionEntity],
      entityClarify,
      undefined,
      EMPTY_DRAFT,
      [addressClarify],
    );

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() =>
      expect(result.current.answers).toContainEqual(
        expect.objectContaining({ clarifyId: addressClarify.id }),
      ),
    );
    expect(result.current.answers).toContainEqual(
      expect.objectContaining({
        clarifyId: addressClarify.id,
        field: "registered_address",
        dismissed: true,
        autoResolved: true,
      }),
    );
  });

  it("leaves them open when the authorization is refused, so the review still asks what nothing answered", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { title: "conflict", detail: "That option is no longer open." },
          409,
        ),
      ),
    );
    const { result } = setupHook(
      [gradionEntity],
      entityClarify,
      undefined,
      EMPTY_DRAFT,
      [addressClarify],
    );

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(result.current.failure).not.toBeNull());
    // The refused pick rolled back, and the sibling was never touched: both
    // questions are open again, which is the only honest state after a choice
    // the server would not confirm.
    expect(result.current.answers).toEqual([]);
  });

  it("never overwrites what a person already said about one of them", async () => {
    stubAuthorizedReply();
    const { result } = setupHook(
      [gradionEntity],
      entityClarify,
      undefined,
      EMPTY_DRAFT,
      [addressClarify],
    );

    act(() => {
      result.current.dismissClarify(addressClarify.id);
    });
    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() =>
      expect(result.current.answers).toContainEqual(
        expect.objectContaining({ clarifyId: entityClarify.id }),
      ),
    );
    const sibling = result.current.answers.filter(
      (answer) => answer.clarifyId === addressClarify.id,
    );
    // One record, and it is theirs: the review's skipped tail names a question
    // the reader declined and stays silent about one the pick retired, so a
    // retirement written over a human dismissal loses the reader's own answer.
    expect(sibling).toHaveLength(1);
    expect(sibling[0]?.autoResolved).toBeUndefined();
    expect(sibling[0]?.dismissed).toBe(true);
  });
});

describe("useClarifyAnswers — honest failures", () => {
  // The request path (mutationFn) carries the server's own problem body, so
  // the detail the reader sees is a sentence composed for them. Anything
  // reaching onError WITHOUT that body (a client-side bug, a network layer
  // throwing something unexpected) must never hand its raw .message over.
  it("shows the server's own words when the authorization is refused", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          {
            code: "validation_error",
            title: "invalid_selection",
            detail: "That option no longer matches the dossier.",
          },
          422,
        ),
      ),
    );
    const { result } = setupHook([]);

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(result.current.failure).not.toBeNull());
    expect(result.current.failure).toEqual({
      kind: "request",
      detail: "That option no longer matches the dossier.",
    });
  });

  it("falls back to the shared line when the refusal carried no words for a reader", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ status: 502 }, 502)),
    );
    const { result } = setupHook([]);

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(result.current.failure).not.toBeNull());
    // Catalog copy in the reader's own language — never the placeholder a
    // problem body with no detail carries into a stack trace.
    expect(result.current.failure).toEqual({
      kind: "request",
      detail: translate("en", "common.errorNoCause"),
    });
  });

  it("never surfaces a raw exception message when something unexpected breaks the round trip, and reports it exactly once", async () => {
    const crash = new TypeError("Cannot read properties of undefined");
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw crash;
      }),
    );
    const errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
    const { result } = setupHook([]);

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(result.current.failure).not.toBeNull());
    expect(result.current.failure).toEqual({ kind: "unconfirmed" });
    // The reader is told the choice did not stick; the thing that actually
    // broke is kept once, by the client's own sink, so an operator reading
    // the console sees one failure rather than two spellings of it.
    expect(errorLog).toHaveBeenCalledTimes(1);
    expect(errorLog).toHaveBeenCalledWith(crash);
  });

  it("keeps nothing at all for a refusal the reader can already read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { title: "conflict", detail: "That option is no longer open." },
          409,
        ),
      ),
    );
    const errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
    const { result } = setupHook([]);

    act(() => {
      result.current.answerClarify(entityClarify.id, gradionEntity.name);
    });

    await waitFor(() => expect(result.current.failure).not.toBeNull());
    expect(result.current.failure).toEqual({
      kind: "request",
      detail: "That option is no longer open.",
    });
    expect(errorLog).not.toHaveBeenCalled();
  });
});
