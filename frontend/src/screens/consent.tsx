import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { type Provenance, ProvenanceTag } from "../design-system/trust";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { humanizeToken } from "./audit";
import { problemMessage, QueryStates, throwProblem } from "./common";
import "./consent.css";

// The Art. 7 proof log (G-4) + the double-opt-in redeem field (G-5) for the
// Person 360. GET /people/{id}/consent already returns {state, events}; this
// is the only surface that reads events — the 360 previously rendered state
// alone and silently dropped the append-only trail. requires_double_opt_in
// lives on ConsentPurpose, not on the person's per-purpose state, so this
// section also reads GET /consent-purposes and joins on purpose_id to know
// which rows need a token before a grant can take effect.

type ConsentPurpose = components["schemas"]["ConsentPurpose"];
type PersonConsentState = components["schemas"]["PersonConsentState"];
type ConsentEvent = components["schemas"]["ConsentEvent"];
type PersonConsentPayload = {
  state: PersonConsentState[];
  events: ConsentEvent[];
};

function usePersonConsent(personId: string) {
  return useQuery({
    queryKey: ["person-consent", personId],
    queryFn: async () => {
      const { data, error } = await api.GET("/people/{id}/consent", {
        params: { path: { id: personId } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

// Same cache key settings.tsx's ConsentPurposesCard uses, so the two
// surfaces share one fetch. No pagination — the endpoint hardcodes
// has_more:false, so there is no second page to walk.
function useConsentPurposes() {
  return useQuery({
    queryKey: ["consent-purposes"],
    queryFn: async () => {
      const { data, error } = await api.GET("/consent-purposes");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

// An event's actor is a typed enum (human/agent/system/connector), not the
// captured_by string provenanceOf (common.tsx) parses — so this is its own
// mapping rather than a reuse of that one.
function eventProvenance(event: ConsentEvent): Provenance {
  if (!event.actor_type || event.actor_type === "human") {
    return { kind: "human" };
  }
  return {
    kind: "agent",
    agent: event.actor_id
      ? `${event.actor_type}:${event.actor_id}`
      : event.actor_type,
  };
}

// The per-purpose Art. 7 proof log: newest first, one row per transition.
// Never renders policy_text/policy_version — wireEvent never projects them
// (they're NOT NULL in consent_event but genuinely absent on the wire), so
// showing wording here would be fabricated evidence on a GDPR proof surface.
function ConsentProofLog({ events }: Readonly<{ events: ConsentEvent[] }>) {
  const t = useT();
  const { locale } = useLocale();
  const viewerZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (events.length === 0) {
    return <EmptyState>{t("consent.proofEmpty")}</EmptyState>;
  }
  const ordered = [...events].sort((a, b) =>
    b.occurred_at.localeCompare(a.occurred_at),
  );
  return (
    <ul className="timeline">
      {ordered.map((event) => (
        <li key={event.id}>
          <span className="tl-body">
            <span className="tl-title">
              <Badge tone={event.new_state === "granted" ? "success" : "warn"}>
                {humanizeToken(event.new_state)}
              </Badge>{" "}
              {event.source ?? t("consent.sourceUnknown")}
            </span>
            <span className="tl-meta">
              <ProvenanceTag provenance={eventProvenance(event)} />
              <span>
                {formatDateTime(event.occurred_at, locale, viewerZone)}
              </span>
            </span>
          </span>
        </li>
      ))}
    </ul>
  );
}

// The ternary state badge: unknown gets no tone (it isn't a withdrawal — the
// noRecord subtitle carries that distinction instead).
function stateTone(
  state: PersonConsentState["state"],
): "success" | "warn" | undefined {
  if (state === "granted") return "success";
  if (state === "withdrawn") return "warn";
  return undefined;
}

// A mutation's own error, rendered verbatim rather than a generic failure —
// a DOI-required purpose 422s here, and the human needs to see exactly why
// the toggle didn't take.
function MutationError({ error }: Readonly<{ error: unknown }>) {
  if (!(error instanceof Error)) {
    return null;
  }
  return (
    <p className="t-caption" style={{ color: "var(--danger)" }}>
      {error.message}
    </p>
  );
}

// One consent-purpose row on the Person 360 (P-8/P-9): the state badge, a
// Grant/Withdraw toggle that writes an append-only consent_event through
// POST /people/{id}/consent, the token field a DOI purpose needs before a
// grant takes effect, and a toggleable proof log. lawful_basis is
// intentionally omitted from the toggle body — it's optional in
// RecordConsentRequest and this control has no field for it yet. Errors
// surface verbatim (a DOI-required purpose 422s here rather than silently
// no-opping) so the human sees exactly why the toggle didn't take.
function ConsentRow({
  personId,
  entry,
  purpose,
  events,
}: Readonly<{
  personId: string;
  entry: PersonConsentState;
  purpose: ConsentPurpose | undefined;
  events: ConsentEvent[];
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const granted = entry.state === "granted";
  const [token, setToken] = useState("");
  const [showLog, setShowLog] = useState(false);
  const requiresDoi = purpose?.requires_double_opt_in ?? false;

  const setState = useMutation({
    mutationFn: async (newState: "granted" | "withdrawn") => {
      const trimmedToken = token.trim();
      const { data, error } = await api.POST("/people/{id}/consent", {
        params: { path: { id: personId } },
        body: {
          purpose_id: entry.purpose_id,
          new_state: newState,
          ...(trimmedToken ? { double_opt_in_token: trimmedToken } : {}),
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    // Patches the cached row in place from the mutation's own response
    // rather than invalidating: invalidateQueries would refetch immediately
    // (react-query refetches active observers by default), a round-trip the
    // response we already hold makes unnecessary. The proof log itself only
    // grows on the next real fetch — the write endpoint doesn't echo the new
    // event row, so this patch honestly updates state without inventing one.
    onSuccess: (updated) => {
      queryClient.setQueryData<PersonConsentPayload>(
        ["person-consent", personId],
        (previous) =>
          previous && {
            ...previous,
            state: previous.state.map((row) =>
              row.purpose_id === updated.purpose_id ? updated : row,
            ),
          },
      );
    },
  });

  const issueDoi = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        "/people/{id}/consent/double-opt-in",
        {
          params: { path: { id: personId } },
          // deliver:true would queue a confirmation email per the contract,
          // but the server has no queue call behind it (doi.go mints only) —
          // asking for delivery here would silently lose the token, so this
          // surface takes ownership of disclosing it below instead.
          body: { purpose_id: entry.purpose_id, deliver: false },
        },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  return (
    <div className="consent-row">
      <div className="consent-row-head">
        <strong>
          {purpose?.label ?? entry.purpose_key ?? entry.purpose_id}
        </strong>
        <Badge tone={stateTone(entry.state)}>
          {humanizeToken(entry.state)}
        </Badge>
        {entry.state === "unknown" && (
          <span className="t-caption">{t("consent.noRecord")}</span>
        )}
      </div>
      <div className="consent-row-actions">
        <Button
          small
          disabled={setState.isPending}
          onClick={() => setState.mutate(granted ? "withdrawn" : "granted")}
        >
          {granted ? t("consent.withdraw") : t("consent.grant")}
        </Button>
        {requiresDoi && (
          <>
            <div className="consent-token-field">
              <label htmlFor={`doi-token-${entry.purpose_id}`}>
                {t("consent.tokenLabel")}
              </label>
              <TextInput
                id={`doi-token-${entry.purpose_id}`}
                value={token}
                onChange={(event) => setToken(event.target.value)}
              />
            </div>
            <Button
              small
              disabled={issueDoi.isPending}
              onClick={() => issueDoi.mutate()}
            >
              {t("consent.doubleOptIn")}
            </Button>
          </>
        )}
        {/* Unknown means no consent record ever existed for this purpose —
            proof rows only ever record transitions to granted/withdrawn, so
            an unknown-state purpose is guaranteed to have zero events. */}
        {entry.state !== "unknown" && (
          <Button small onClick={() => setShowLog((value) => !value)}>
            {t("consent.proofLog")}
          </Button>
        )}
      </div>
      {requiresDoi && <p className="t-caption">{t("consent.tokenHint")}</p>}
      {setState.isError && <MutationError error={setState.error} />}
      {issueDoi.isError && <MutationError error={issueDoi.error} />}
      {issueDoi.data && (
        <p className="t-caption">
          {t("consent.doiIssued")} <code>{issueDoi.data.token}</code> ·{" "}
          {t("consent.doiExpires")}: {issueDoi.data.expires_at}
        </p>
      )}
      {showLog && <ConsentProofLog events={events} />}
    </div>
  );
}

export function ConsentSection({ personId }: Readonly<{ personId: string }>) {
  const t = useT();
  const consentQuery = usePersonConsent(personId);
  const purposesQuery = useConsentPurposes();
  const purposes = purposesQuery.data?.data ?? [];
  // Only trust "no purposes" once the purposes fetch itself has actually
  // succeeded — while it's still pending this would otherwise flash the
  // empty state before the real list ever arrives.
  const noPurposes = purposesQuery.isSuccess && purposes.length === 0;
  const consent = consentQuery.data;

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <SectionHeader
        title={t("person.consent")}
        sub={t("consent.defaultDeny")}
      />
      <QueryStates query={consentQuery}>
        {consent &&
          (noPurposes ? (
            <EmptyState>{t("consent.noPurposes")}</EmptyState>
          ) : (
            <div>
              {consent.state.map((entry) => (
                <ConsentRow
                  key={entry.purpose_id}
                  personId={personId}
                  entry={entry}
                  purpose={purposes.find(
                    (purpose) => purpose.id === entry.purpose_id,
                  )}
                  events={consent.events.filter(
                    (event) => event.purpose_id === entry.purpose_id,
                  )}
                />
              ))}
            </div>
          ))}
      </QueryStates>
    </section>
  );
}
