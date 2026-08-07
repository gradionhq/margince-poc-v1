import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, TextInput } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { throwProblem } from "./common";
import { RECORD_ZONE } from "./company360";

// A human's verdict on a machine's claim: agree with it, or correct it.
//
// The backend for this landed with ADR-0085 — confirm and update on both
// evidence sidecars, where a correction moves the CANONICAL column and not just
// the receipt. Until now nothing in the product could call either, so a rep who
// could see that the extracted industry was wrong had no way to say so, and the
// next enrichment run wrote the same wrong value again.
//
// THE TWO VERBS ARE NOT THE SAME ACT and the surface keeps them apart:
//
//   - CONFIRM says "I read this and it is right". No value moves. The claim
//     stops being the machine's and becomes a human's, which is what stops the
//     next scrape overwriting it.
//   - CORRECT says "this is wrong, here is the right value". The company record
//     changes, not only its receipt.
//
// What neither does is erase the machine's proposal. The snippet, the source URL
// and the confidence stay on the row and the audit before-image keeps the whole
// original claim, so "what did it say before I fixed it" is always answerable.

type ProfileField = components["schemas"]["CompanyProfileField"];
type OrganizationFact = components["schemas"]["OrganizationFact"];

// A claim a human can rule on, in the one shape both sidecars reduce to. The
// two differ in how they are addressed — a profile field by its key, a fact by
// `<field>:<value_key>` — and in nothing else that matters here.
export type EvidenceClaim = {
  value: string;
  source: string;
  verifiedAt?: string | null;
  verifiedBy?: string | null;
  // Where the write goes. Built by the caller because only it knows which
  // sidecar this claim came from.
  confirmPath: () => Promise<void>;
  correctPath: (value: string) => Promise<void>;
};

export function profileFieldClaim(
  orgId: string,
  field: ProfileField,
): EvidenceClaim {
  return {
    value: field.value,
    source: field.source,
    verifiedAt: field.verified_at,
    verifiedBy: field.verified_by,
    confirmPath: async () => {
      const { error } = await api.POST(
        "/organizations/{id}/profile-fields/{field}/confirm",
        { params: { path: { id: orgId, field: field.field } } },
      );
      if (error) {
        throwProblem(error);
      }
    },
    correctPath: async (value) => {
      const { error } = await api.PATCH(
        "/organizations/{id}/profile-fields/{field}",
        {
          params: { path: { id: orgId, field: field.field } },
          body: { value },
        },
      );
      if (error) {
        throwProblem(error);
      }
    },
  };
}

export function factClaim(
  orgId: string,
  fact: OrganizationFact,
): EvidenceClaim {
  // The contract addresses a fact as `<field>:<value_key>`. A single-value fact
  // carries an empty value_key and so ends in a bare colon — which is the
  // spelling, not a missing half.
  const factKey = `${fact.field}:${fact.value_key}`;
  return {
    value: fact.value,
    source: fact.source,
    verifiedAt: fact.verified_at,
    verifiedBy: fact.verified_by,
    confirmPath: async () => {
      const { error } = await api.POST(
        "/organizations/{id}/facts/{factKey}/confirm",
        { params: { path: { id: orgId, factKey } } },
      );
      if (error) {
        throwProblem(error);
      }
    },
    correctPath: async (value) => {
      const { error } = await api.PATCH("/organizations/{id}/facts/{factKey}", {
        params: { path: { id: orgId, factKey } },
        body: { value },
      });
      if (error) {
        throwProblem(error);
      }
    },
  };
}

export function EvidenceVerdict({
  orgId,
  claim,
  canEdit,
}: Readonly<{ orgId: string; claim: EvidenceClaim; canEdit: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const [correcting, setCorrecting] = useState(false);
  const [draft, setDraft] = useState(claim.value);

  // Both verbs change the sidecar AND, for a canonical field, the organization
  // row — so the record read, the list it appears in and the 360 that summarizes
  // it all go stale together and are refetched together.
  const settle = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["organizations"] }),
      queryClient.invalidateQueries({ queryKey: ["organization360", orgId] }),
      // The keys below are the ones the CONSUMERS register. React Query matches
      // key segments exactly, so a near-miss spelling invalidates nothing and
      // the page keeps offering a verdict on a claim the human already settled.
      queryClient.invalidateQueries({ queryKey: ["organization", orgId] }),
      queryClient.invalidateQueries({ queryKey: ["org-profile-fields", orgId] }),
      queryClient.invalidateQueries({ queryKey: ["org-facts", orgId] }),
    ]);
  };
  const confirm = useMutation({
    mutationFn: claim.confirmPath,
    onSuccess: settle,
  });
  const correct = useMutation({
    mutationFn: claim.correctPath,
    onSuccess: async () => {
      setCorrecting(false);
      await settle();
    },
  });
  const failure = confirm.error ?? correct.error;

  // Already a human's word. Saying who and when is the whole point — a
  // confirmed value that does not say who confirmed it is no better evidenced
  // than an unconfirmed one.
  if (claim.source === "human") {
    return (
      <span className="evidence-verdict t-muted">
        {claim.verifiedAt
          ? t("evidence.confirmedAt", {
              when: formatDateTime(claim.verifiedAt, locale, RECORD_ZONE),
            })
          : t("evidence.humanSet")}
      </span>
    );
  }
  if (!canEdit) {
    return null;
  }

  if (correcting) {
    return (
      <span className="evidence-verdict">
        <TextInput
          value={draft}
          aria-label={t("evidence.correctedValue")}
          onChange={(event) => setDraft(event.target.value)}
        />
        <Button
          small
          disabled={correct.isPending || draft.trim() === ""}
          onClick={() => correct.mutate(draft.trim())}
        >
          {correct.isPending ? t("evidence.saving") : t("evidence.save")}
        </Button>
        <Button small onClick={() => setCorrecting(false)}>
          {t("evidence.cancel")}
        </Button>
        {/* The draft survives a failed save: the field above still holds what
            was typed, and the refusal names why. */}
        {failure && (
          <span role="alert" className="form-error">
            {failure.message}
          </span>
        )}
      </span>
    );
  }

  return (
    <span className="evidence-verdict">
      <Button
        small
        disabled={confirm.isPending}
        onClick={() => confirm.mutate()}
      >
        {confirm.isPending ? t("evidence.saving") : t("evidence.confirm")}
      </Button>
      <Button
        small
        onClick={() => {
          setDraft(claim.value);
          setCorrecting(true);
        }}
      >
        {t("evidence.correct")}
      </Button>
      {failure && (
        <span role="alert" className="form-error">
          {failure.message}
        </span>
      )}
    </span>
  );
}
