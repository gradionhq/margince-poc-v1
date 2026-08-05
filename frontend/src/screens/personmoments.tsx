import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, Card } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";

type Moment = components["schemas"]["PersonMoment"];
type Evidence = components["schemas"]["PersonMomentEvidence"];
type Action = components["schemas"]["PersonMomentAction"];
type Person360 = components["schemas"]["Person360"];

/**
 * PersonMoments is the first thing on the page, and what it shows is a REASON
 * rather than a record. "Warm, 73" describes a contact; "they replied after 41
 * quiet days and nobody has answered" is something to do.
 *
 * It renders at first paint with no loading state, because every moment is
 * derived deterministically by the same read that fetched the page. There is
 * no model in the path, so there is nothing to wait for and no placeholder
 * pretending to be an answer.
 */
export function PersonMoments({
  personId,
  view,
}: Readonly<{ personId: string; view: Person360 }>) {
  const moments = view.moments ?? [];
  if (moments.length === 0) {
    // Deliberately nothing. A card reading "no reasons found" is an absence
    // inventory: it takes the most prominent position on the page in order to
    // report that it has nothing to report.
    return null;
  }
  return (
    <div
      style={{
        display: "grid",
        gap: "var(--space-3)",
        marginBottom: "var(--space-4)",
      }}
    >
      {moments.map((moment) => (
        <MomentCard
          key={moment.claim_key}
          personId={personId}
          moment={moment}
        />
      ))}
    </div>
  );
}

function MomentCard({
  personId,
  moment,
}: Readonly<{ personId: string; moment: Moment }>) {
  const t = useT();
  const queryClient = useQueryClient();

  // Dismissal writes a verdict keyed on what the moment is ABOUT, so it
  // survives the evidence moving underneath it and the moment being
  // re-derived tomorrow.
  const dismiss = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST("/ai/feedback", {
        body: {
          subject_type: "person",
          subject_id: personId,
          claim_kind: "signal",
          claim_path: moment.claim_key,
          verdict: "suppressed",
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    // Re-read rather than dropping the card locally: a dismissal can uncover a
    // moment that sat below the served cap, and deciding that in the client
    // would be a second implementation of the server's ranking.
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ["person360", personId] }),
  });

  return (
    <Card>
      <div style={{ padding: "var(--space-4)" }}>
        <div
          style={{
            display: "flex",
            justifyContent: "space-between",
            gap: "var(--space-3)",
          }}
        >
          <div>
            <h3 style={{ margin: 0, fontSize: "1.05rem", lineHeight: 1.35 }}>
              {moment.headline}
            </h3>
            <p
              style={{
                margin: "var(--space-1) 0 0",
                lineHeight: 1.5,
                opacity: 0.85,
              }}
            >
              {moment.why_now}
            </p>
          </div>
          <Badge>{t(`person.moment.kind.${moment.kind}`)}</Badge>
        </div>

        <ul
          style={{
            margin: "var(--space-3) 0 0",
            padding: 0,
            listStyle: "none",
            display: "grid",
            gap: "var(--space-1)",
          }}
        >
          {moment.evidence.map((item) => (
            <li key={`${item.type}:${item.label}`}>
              <EvidenceLine item={item} />
            </li>
          ))}
        </ul>

        <div
          style={{
            display: "flex",
            gap: "var(--space-3)",
            marginTop: "var(--space-3)",
            alignItems: "center",
          }}
        >
          <RecommendedAction action={moment.recommended_action} />
          <Button
            small
            disabled={dismiss.isPending}
            onClick={() => dismiss.mutate()}
          >
            {t("person.moment.dismiss")}
          </Button>
        </div>
        {dismiss.isError && (
          <p
            role="alert"
            style={{ margin: "var(--space-2) 0 0", color: "var(--danger)" }}
          >
            {dismiss.error.message}
          </p>
        )}
      </div>
    </Card>
  );
}

/**
 * EvidenceLine shows what the moment was derived from. It is never a summary:
 * a reason whose evidence the reader cannot check is an opinion, and the whole
 * argument for deriving these deterministically is that each one knows exactly
 * what it fired on.
 */
function EvidenceLine({ item }: Readonly<{ item: Evidence }>) {
  const when = item.observed_at
    ? new Date(item.observed_at).toLocaleDateString()
    : undefined;
  return (
    <span style={{ fontSize: "0.9rem", opacity: 0.8 }}>
      {item.label}
      {when ? ` · ${when}` : ""}
    </span>
  );
}

/**
 * RecommendedAction states the move and what taking it would cost.
 *
 * It is TEXT and not a button, deliberately. The action flows themselves —
 * drafting a reply, completing a task — live on the surfaces that own them and
 * are not wired to this card yet. A control that looks live and does nothing
 * is worse than a sentence that is true, and the states already ride the wire,
 * so this becomes a button later without the card changing shape.
 */
function RecommendedAction({ action }: Readonly<{ action: Action }>) {
  const t = useT();
  if (action.state === "blocked") {
    return (
      <span style={{ fontSize: "0.9rem", opacity: 0.7 }}>
        {action.blocked_reason ?? t("person.moment.blocked")}
      </span>
    );
  }
  return (
    <span style={{ fontSize: "0.9rem" }}>
      <strong>{t("person.moment.recommended")}</strong> {action.label}
      {action.state === "will_confirm"
        ? ` · ${t("person.moment.willConfirm")}`
        : ""}
    </span>
  );
}
