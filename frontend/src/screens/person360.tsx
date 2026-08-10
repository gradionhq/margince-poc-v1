import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Card, Disclosure, SectionHeader } from "../design-system/atoms";
import { EvidenceMark } from "../design-system/evidencemark";
import type { ConfidenceLevel } from "../design-system/trust";
import { useT } from "../i18n";
import { provenanceOf, throwProblem } from "./common";
import { EntityRef } from "./entityref";

export type Person360 = components["schemas"]["Person360"];
type ProfileField = components["schemas"]["PersonProfileField"];
type Colleague = components["schemas"]["PersonNetworkColleague"];

/**
 * usePerson360 is the person page's ONE read. It replaces the seven
 * per-card queries the screen used to fire, so every section describes the
 * same moment rather than a stack of independently-timed round trips.
 */
export function usePerson360(id: string) {
  return useQuery({
    queryKey: ["person360", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/people/{id}/360", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

/** omitted reports whether a section was withheld for lack of a grant. */
export function omitted(view: Person360 | undefined, section: string): boolean {
  return Boolean(view?.sections_omitted?.some((s) => s === section));
}

/**
 * thinRecord decides whether the page shows its authored thin state instead
 * of a stack of empty modules.
 *
 * The predicate is deliberately about the RELATIONSHIP, not about field
 * completeness: a contact with a full address block and no correspondence is
 * still someone nobody here has spoken to, which is the thing the page has
 * to say out loud.
 */
export function thinRecord(view: Person360 | undefined): boolean {
  if (!view) {
    return false;
  }
  // A WITHHELD section is not an empty one. A caller who cannot read
  // activities or the network would otherwise be told this relationship is
  // thin — and the thin state suppresses the ordinary modules, so they would
  // lose the rest of the page over data that may well exist. Absent for want
  // of a grant and absent for want of data are different facts (the same
  // distinction sections_omitted exists to carry).
  if (omitted(view, "activities") || omitted(view, "network")) {
    return false;
  }
  if (!view.activities || !view.network) {
    return false;
  }
  return (
    view.activities.data.length === 0 && view.network.colleagues.length === 0
  );
}

/**
 * ThinState is the whole answer when a record is thin: one honest sentence
 * about what IS known, and ONE way forward. It replaces the six empty cards
 * that used to render — an absence inventory tells the reader six times that
 * the CRM knows nothing, which is both true and useless.
 */
export function ThinState({
  view,
  onLogActivity,
}: Readonly<{ view: Person360; onLogActivity?: () => void }>) {
  const t = useT();
  const employer = view.employments?.data.find((e) => e.is_current_primary);
  const email = view.person.emails?.[0]?.email;

  // The remediation is chosen by what is MISSING, so the page offers the one
  // step that would actually change the answer rather than a menu.
  const remediation = employer
    ? t("person.thin.remediation.capture")
    : t("person.thin.remediation.employer");

  return (
    <Card>
      <div style={{ padding: "var(--space-6)" }}>
        <SectionHeader title={t("person.thin.title")} />
        <p style={{ margin: "8px 0 0", lineHeight: 1.55 }}>
          {t("person.thin.known", {
            name: view.person.full_name,
            what: [email, employer?.organization_name]
              .filter(Boolean)
              .join(" · "),
          })}
        </p>
        <p style={{ margin: "10px 0 0", lineHeight: 1.55 }}>{remediation}</p>
        {onLogActivity && (
          <button
            type="button"
            className="btn"
            style={{ marginTop: "var(--space-3)" }}
            onClick={onLogActivity}
          >
            {t("person.thin.logFirst")}
          </button>
        )}
      </div>
    </Card>
  );
}

/**
 * RelationshipPulse is the person's warmth in WORDS, following the company
 * pattern (ADR-0079 arc): no verdict number on the face of the card.
 *
 * The two directions are shown side by side and never folded. A contact we
 * mailed a fortnight ago with no reply and one who wrote to us this morning
 * share a last-touch date and mean opposite things, and that difference is
 * the one a rep acts on.
 *
 * The score, its three factors and the literal arithmetic live one
 * disclosure away — computed, inspectable, just not leading.
 */
export function RelationshipPulse({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const s = view.strength;
  const warmest = view.network?.colleagues[0];

  return (
    <Card>
      <div style={{ padding: "var(--space-4)" }}>
        <SectionHeader title={t("person.pulse.title")} />
        <p style={{ margin: "8px 0 0", lineHeight: 1.5 }}>
          {warmest
            ? t("person.pulse.warmestIs", { name: warmest.display_name })
            : t("person.pulse.nobodyYet")}
        </p>
        <RelationshipChanges view={view} />
        <dl className="fact-list" style={{ marginTop: "var(--space-3)" }}>
          <div>
            <dt>{t("person.pulse.lastInbound")}</dt>
            <dd>
              {view.last_inbound_at
                ? new Date(view.last_inbound_at).toLocaleDateString()
                : t("person.pulse.neverInbound")}
            </dd>
          </div>
          <div>
            <dt>{t("person.pulse.lastOutbound")}</dt>
            <dd>
              {view.last_outbound_at
                ? new Date(view.last_outbound_at).toLocaleDateString()
                : t("person.pulse.neverOutbound")}
            </dd>
          </div>
        </dl>
        {s && (
          <Disclosure summary={t("person.pulse.why")}>
            <p style={{ margin: 0, lineHeight: 1.55 }}>
              {t("person.pulse.arithmetic", {
                score: String(s.score),
                recency: s.factors.recency.toFixed(2),
                frequency: s.factors.frequency.toFixed(2),
                reciprocity: s.factors.reciprocity.toFixed(2),
              })}
            </p>
          </Disclosure>
        )}
      </div>
    </Card>
  );
}

/**
 * RelationshipChanges says what HAPPENED to the relationship, beneath what it
 * currently is.
 *
 * The two belong together and are deliberately not folded. "Warm" is a
 * description the reader can already infer from the two dates above; "they
 * replied after 41 quiet days" is what makes those dates mean something.
 */
function RelationshipChanges({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const changes = view.relationship_changes ?? [];
  if (changes.length === 0) {
    return null;
  }
  return (
    <ul
      style={{
        margin: "var(--space-2) 0 0",
        padding: 0,
        listStyle: "none",
        display: "grid",
        gap: "var(--space-1)",
      }}
    >
      {changes.map((c) => (
        <li key={c.kind} style={{ fontSize: "0.9rem", opacity: 0.85 }}>
          {changeSentence(c, t)}
        </li>
      ))}
    </ul>
  );
}

/**
 * changeSentence writes each derived change as a sentence. A band move names
 * BOTH bands: "the relationship moved" without saying from what to what is a
 * claim the reader has to take on trust.
 */
function changeSentence(
  c: components["schemas"]["PersonRelationshipChange"],
  t: ReturnType<typeof useT>,
): string {
  const days = String(c.days ?? 0);
  if (c.kind === "replied_after_gap") {
    return t("person.change.repliedAfterGap", { days });
  }
  if (c.kind === "went_quiet") {
    return t("person.change.wentQuiet", { days });
  }
  return t(`person.change.${c.kind}`, {
    from: t(`person.band.${c.from_bucket ?? "none"}`),
    to: t(`person.band.${c.to_bucket ?? "none"}`),
  });
}

/**
 * IdentityRail is what the record IS: contact methods, current employment,
 * buying roles, and the career history that a re-read must never overwrite.
 *
 * Every enriched value carries its receipt — the verbatim text it was read
 * from — because a field the reader cannot check is a claim, not a fact.
 */
export function IdentityRail({
  view,
  children,
}: Readonly<{ view: Person360; children?: ReactNode }>) {
  const t = useT();
  const byField = new Map<string, ProfileField>(
    (view.profile_fields ?? []).map((f) => [f.field, f]),
  );
  const current = view.employments?.data.find((e) => e.is_current_primary);
  const former = (view.employments?.data ?? []).filter(
    (e) => !e.is_current_primary,
  );

  return (
    <>
      <Card>
        <div style={{ padding: "var(--space-4)" }}>
          <SectionHeader title={t("person.identity.title")} />
          <dl className="fact-list">
            {view.person.emails?.map((e) => (
              <div key={e.id}>
                <dt>{t("person.identity.email")}</dt>
                <dd>{e.email}</dd>
              </div>
            ))}
            {view.person.phones?.map((p) => (
              <div key={p.id}>
                <dt>{t("person.identity.phone")}</dt>
                <dd>
                  <Evidenced value={p.phone} field={byField.get("phone")} />
                </dd>
              </div>
            ))}
            {current && (
              <div>
                <dt>{t("person.identity.currentRole")}</dt>
                <dd>
                  <Evidenced
                    value={current.role ?? view.person.title ?? "—"}
                    field={byField.get("role") ?? byField.get("title")}
                  />
                  {current.organization_name && (
                    <>
                      {" · "}
                      <EntityRef
                        kind="organization"
                        id={current.organization_id}
                      />
                    </>
                  )}
                </dd>
              </div>
            )}
            {view.deal_roles?.data.map((r) => (
              <div key={r.relationship_id}>
                <dt>{t("person.identity.buyingRole")}</dt>
                <dd>
                  <Badge tone="accent">{r.role}</Badge>
                  {r.deal_title && <> · {r.deal_title}</>}
                </dd>
              </div>
            ))}
          </dl>
        </div>
      </Card>

      {former.length > 0 && (
        <Card>
          <div style={{ padding: "var(--space-4)" }}>
            <SectionHeader title={t("person.career.title")} />
            <ul style={{ margin: 0, paddingLeft: "var(--space-4)" }}>
              {former.map((e) => (
                <li
                  key={e.relationship_id}
                  style={{ marginTop: "var(--space-1)" }}
                >
                  {e.organization_name ?? "—"}
                  {e.role && <> · {e.role}</>}
                </li>
              ))}
            </ul>
          </div>
        </Card>
      )}

      <ConsentGuard view={view} />
      {children}
    </>
  );
}

/**
 * ConsentGuard compresses the consent module to what the reader needs
 * BEFORE acting: whether an outbound message is allowed. The per-purpose
 * proof log stays one click away — it is the ledger, this is the guard.
 */
function ConsentGuard({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  if (!view.consent) {
    return null;
  }
  const granted = view.consent.state.filter((s) => s.state === "granted");
  const blocked = view.consent.state.filter((s) => s.state !== "granted");
  return (
    <Card>
      <div style={{ padding: "var(--space-4)" }}>
        <SectionHeader title={t("person.consent.title")} />
        <p style={{ margin: "8px 0 0", lineHeight: 1.5 }}>
          {granted.length > 0
            ? t("person.consent.allowed", {
                purposes: granted.map((g) => g.purpose_key ?? "").join(", "),
              })
            : t("person.consent.noneGranted")}
        </p>
        {blocked.length > 0 && (
          <p style={{ margin: "6px 0 0", lineHeight: 1.5 }}>
            {t("person.consent.blocked", {
              purposes: blocked.map((b) => b.purpose_key ?? "").join(", "),
            })}
          </p>
        )}
      </div>
    </Card>
  );
}

/**
 * Evidenced renders a value with its receipt when one exists. Without
 * evidence it renders the plain value — the mark is never decoration, it
 * means "there is a source and you can read it".
 */
function Evidenced({
  value,
  field,
}: Readonly<{ value: string; field?: ProfileField }>) {
  if (!field) {
    return <>{value}</>;
  }
  return (
    <EvidenceMark
      value={value}
      source={{
        provenance: provenanceOf(field.captured_by, undefined),
        confidence: confidenceBand(field.confidence),
        snippet: field.evidence_snippet,
        at: field.captured_at,
      }}
    />
  );
}

/**
 * confidenceBand renders the stored 0..1 score in the three words the design
 * system speaks. A number on screen would invite arithmetic the reader
 * cannot check; the band says how much to lean on the value.
 */
function confidenceBand(score?: number | null): ConfidenceLevel | undefined {
  if (score === undefined || score === null) {
    return undefined;
  }
  if (score >= 0.8) {
    return "high";
  }
  return score >= 0.5 ? "med" : "low";
}

/** WhoKnowsThem ranks colleagues by warmth — the ordering IS the answer. */
export function WhoKnowsThem({ view }: Readonly<{ view: Person360 }>) {
  const t = useT();
  const colleagues = view.network?.colleagues ?? [];
  if (colleagues.length === 0) {
    return null;
  }
  return (
    <Card>
      <div style={{ padding: "var(--space-4)" }}>
        <SectionHeader title={t("person.network.title")} />
        <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
          {colleagues.map((c) => (
            <li key={c.user_id} style={{ padding: "8px 0" }}>
              <strong>{c.display_name}</strong>
              <div style={{ fontSize: 12, opacity: 0.75 }}>
                {proofLine(c, t)}
              </div>
            </li>
          ))}
        </ul>
      </div>
    </Card>
  );
}

/**
 * proofLine says WHY this colleague is the route, not just that they are.
 * Two-way traffic is named as such: six unanswered sends and six real
 * exchanges are different relationships wearing the same count.
 */
function proofLine(c: Colleague, t: ReturnType<typeof useT>): string {
  const twoWay = (c.inbound_90d ?? 0) > 0 && (c.outbound_90d ?? 0) > 0;
  const parts = [
    twoWay
      ? t("person.network.twoWay", { count: String(c.interactions_90d) })
      : t("person.network.oneSided", { count: String(c.interactions_90d) }),
  ];
  if (c.last_inbound_at) {
    parts.push(
      t("person.network.replied", {
        when: new Date(c.last_inbound_at).toLocaleDateString(),
      }),
    );
  }
  return parts.join(" · ");
}
