import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
  Skeleton,
  TextInput,
} from "../design-system/atoms";
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

// The actor line on the Art. 7 proof log: names WHO the server says captured
// this decision, verbatim. This is deliberately NOT ProvenanceTag — that
// component exists for a compose/staging context ("did *you* type this, or
// an agent") and its human branch renders "typed by you" unconditionally,
// which would misattribute every human-captured grant to the viewer instead
// of the actual actor (frequently a different teammate, or the subject
// themself via a public form). A proof log's actor is evidence, not a claim
// about who is looking at it — it always shows actor_type + actor_id
// straight from the wire, never resolved against the current session.
function ConsentEventActor({ event }: Readonly<{ event: ConsentEvent }>) {
  const t = useT();
  if (!event.actor_type) {
    return <span className="t-caption">{t("consent.actorUnknown")}</span>;
  }
  // Only the four actor kinds the wire actually names get a positive label;
  // anything else (a kind added to the enum after this code was written) is
  // reported as unrecorded rather than silently mislabelled — a proof log must
  // never assert an actor the wire did not.
  const label =
    event.actor_type === "human"
      ? t("consent.actorHuman")
      : event.actor_type === "agent"
        ? t("consent.actorAgent")
        : event.actor_type === "system"
          ? t("consent.actorSystem")
          : event.actor_type === "connector"
            ? t("consent.actorConnector")
            : t("consent.actorUnknown");
  return (
    <span className="t-caption">
      {label}
      {event.actor_id && (
        <>
          {" "}
          <span className="t-mono">{event.actor_id}</span>
        </>
      )}
    </span>
  );
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
    <div className="card card-inset consent-proof-log">
      <ul className="timeline">
        {ordered.map((event) => (
          <li key={event.id}>
            <span className="tl-body">
              <span className="tl-title">
                <Badge
                  tone={event.new_state === "granted" ? "success" : "warn"}
                >
                  {humanizeToken(event.new_state)}
                </Badge>{" "}
                {event.source ?? t("consent.sourceUnknown")}
              </span>
              <span className="tl-meta">
                <ConsentEventActor event={event} />
                <span>
                  {formatDateTime(event.occurred_at, locale, viewerZone)}
                </span>
              </span>
            </span>
          </li>
        ))}
      </ul>
    </div>
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
  const { locale } = useLocale();
  const viewerZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
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
    // The write endpoint returns only the updated state row, not the new
    // consent_event — so the proof log can only pick up the transition just
    // made by refetching, not by patching the cache from this response.
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["person-consent", personId],
      });
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
          <Button
            small
            disabled={issueDoi.isPending}
            onClick={() => issueDoi.mutate()}
          >
            {t("consent.doubleOptIn")}
          </Button>
        )}
        <Button small onClick={() => setShowLog((value) => !value)}>
          {t("consent.proofLog")}
        </Button>
      </div>
      {requiresDoi && (
        <div className="field consent-token-field">
          <label className="t-label" htmlFor={`doi-token-${entry.purpose_id}`}>
            {t("consent.tokenLabel")}
          </label>
          <TextInput
            id={`doi-token-${entry.purpose_id}`}
            value={token}
            onChange={(event) => setToken(event.target.value)}
          />
          <p className="t-caption">{t("consent.tokenHint")}</p>
        </div>
      )}
      {setState.isError && <MutationError error={setState.error} />}
      {issueDoi.isError && <MutationError error={issueDoi.error} />}
      {issueDoi.data && (
        <p className="t-caption">
          {t("consent.doiIssued")} <code>{issueDoi.data.token}</code> ·{" "}
          {t("consent.doiExpires")}:{" "}
          {formatDateTime(issueDoi.data.expires_at, locale, viewerZone)}
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

  // requires_double_opt_in lives only on ConsentPurpose, so a row's DOI gate
  // depends on purposesQuery having actually succeeded — a failed fetch that
  // fell back to `[]` here (rather than erroring loudly) would make every
  // DOI-required purpose render as freely grantable, silently dropping a
  // legal control. share.tsx's RosterPicker gates its two roster fetches the
  // same explicit way, for the same reason: a collapsed-to-empty failure
  // must never be mistaken for a real empty list.
  let body: ReactNode = null;
  if (consent) {
    if (purposesQuery.isPending) {
      body = <Skeleton width="60%" />;
    } else if (purposesQuery.isError) {
      body = (
        <EmptyState>
          <p>{t("consent.purposesUnavailable")}</p>
          <Button small onClick={() => purposesQuery.refetch()}>
            {t("common.retry")}
          </Button>
        </EmptyState>
      );
    } else if (noPurposes) {
      body = <EmptyState>{t("consent.noPurposes")}</EmptyState>;
    } else {
      body = (
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
      );
    }
  }

  return (
    <section
      className="card"
      style={{ marginBottom: 16 }}
      aria-label={t("person.consent")}
    >
      <SectionHeader
        title={t("person.consent")}
        sub={t("consent.defaultDeny")}
      />
      <QueryStates query={consentQuery}>{body}</QueryStates>
    </section>
  );
}
