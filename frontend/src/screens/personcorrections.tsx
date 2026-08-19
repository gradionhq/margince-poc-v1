import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Badge, Button, Card, SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";

type Person360 = components["schemas"]["Person360"];
type ProfileField = components["schemas"]["PersonProfileField"];

/**
 * EnrichedFields is where the page stops asserting and starts asking.
 *
 * Every value here was read by a machine out of a page or a signature, and the
 * verbatim text it came from sits underneath it. What this adds is the other
 * half: the reader can say the machine got it wrong, and be believed next time.
 *
 * A corrected field shows the HUMAN's value with a marker, and the snippet the
 * machine read stays visible beneath it. Hiding the snippet would make the
 * correction unexplainable — what was misread is the reason the correction
 * exists.
 *
 * What the reader may SAY is a separate question from what they may see. The
 * evidence is a read and renders for anyone who can open the contact; recording
 * a verdict is a write, and `POST /ai/feedback` demands `update` on the subject
 * (ai/feedback.go, `auth.Require(subjectType, ActionUpdate)`), so the controls
 * are asked for once here and passed down rather than offered to every reader
 * and refused by the server.
 */
export function EnrichedFields({
  personId,
  view,
}: Readonly<{ personId: string; view: Person360 }>) {
  const t = useT();
  const mayCorrect = useCanWrite("person", "update");
  const fields = view.profile_fields ?? [];
  if (fields.length === 0) {
    return null;
  }
  return (
    <Card>
      <div style={{ padding: "var(--space-4)" }}>
        <SectionHeader
          title={t("person.enriched.title")}
          sub={t("person.enriched.sub")}
        />
        <ul
          style={{
            margin: "var(--space-3) 0 0",
            padding: 0,
            listStyle: "none",
            display: "grid",
            gap: "var(--space-3)",
          }}
        >
          {fields.map((field) => (
            <li key={field.field}>
              <EnrichedField
                personId={personId}
                field={field}
                mayCorrect={mayCorrect}
              />
            </li>
          ))}
        </ul>
      </div>
    </Card>
  );
}

function EnrichedField({
  personId,
  field,
  mayCorrect,
}: Readonly<{
  personId: string;
  field: ProfileField;
  mayCorrect: boolean;
}>) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(field.value);
  const queryClient = useQueryClient();

  const record = useMutation({
    mutationFn: async (input: {
      verdict: "corrected" | "confirmed";
      value?: string;
    }) => {
      const { error } = await api.POST("/ai/feedback", {
        body: {
          subject_type: "person",
          subject_id: personId,
          claim_kind: "profile_field",
          // The server's own key for this claim, echoed back rather than
          // rebuilt here: a path this client spelled differently would file
          // the verdict against a claim nothing ever consults again.
          claim_path: field.claim_key ?? `profile_field:${field.field}`,
          verdict: input.verdict,
          corrected_value: input.value,
        },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      setEditing(false);
      queryClient.invalidateQueries({ queryKey: ["person360", personId] });
    },
  });

  return (
    <div>
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
          alignItems: "baseline",
          flexWrap: "wrap",
        }}
      >
        <strong>{t(`person.enriched.field.${field.field}`)}</strong>
        {editing ? (
          <input
            aria-label={t(`person.enriched.field.${field.field}`)}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
          />
        ) : (
          <span>{field.value}</span>
        )}
        {field.verdict === "corrected" && (
          <Badge>{t("person.enriched.correctedByYou")}</Badge>
        )}
        {field.verdict === "confirmed" && (
          <Badge>{t("person.enriched.confirmed")}</Badge>
        )}
      </div>

      {/* The evidence stays visible after a correction, not instead of it:
          what the machine read is the reason the correction was needed. */}
      <p
        style={{
          margin: "var(--space-1) 0 0",
          fontSize: "0.85rem",
          opacity: 0.75,
        }}
      >
        {t("person.enriched.readFrom", {
          source: field.source,
          when: new Date(field.captured_at).toLocaleDateString(),
        })}{" "}
        — “{field.evidence_snippet}”
      </p>

      {mayCorrect && (
        <div
          style={{
            display: "flex",
            gap: "var(--space-2)",
            marginTop: "var(--space-2)",
          }}
        >
          {editing ? (
            <>
              {/* Pending is not refusal: the write the reader just started keeps
                  their focus, while an empty draft is the one thing that really
                  is refused. Disabling on both would move focus off the control
                  they pressed, at the moment they are waiting on it. */}
              <Button
                small
                pending={record.isPending}
                disabled={draft.trim() === ""}
                onClick={() =>
                  record.mutate({ verdict: "corrected", value: draft.trim() })
                }
              >
                {t("person.enriched.save")}
              </Button>
              <Button
                small
                disabled={record.isPending}
                onClick={() => setEditing(false)}
              >
                {t("person.enriched.cancel")}
              </Button>
            </>
          ) : (
            <>
              {/* The editor opens on what the field says NOW. `draft` survives
                  a cancel and a save alike, so without this a second open
                  reloads text the reader abandoned — and saving it would
                  overwrite the correction they just made with the one they
                  threw away. */}
              <Button
                small
                onClick={() => {
                  setDraft(field.value);
                  setEditing(true);
                }}
              >
                {t("person.enriched.correct")}
              </Button>
              {/* Confirm is offered while nobody has ruled on the claim. A
                  field a human already corrected has been ruled on — theirs is
                  the value on display — so a second verdict on it would ask
                  them to confirm a machine reading the page no longer shows,
                  and `suppressed` is a decision to stop being asked at all. */}
              {!field.verdict && (
                <Button
                  small
                  disabled={record.isPending}
                  onClick={() => record.mutate({ verdict: "confirmed" })}
                >
                  {t("person.enriched.confirm")}
                </Button>
              )}
            </>
          )}
        </div>
      )}
      {record.isError && (
        <p
          role="alert"
          style={{ margin: "var(--space-2) 0 0", color: "var(--danger)" }}
        >
          {problemMessageOf(record.error, t)}
        </p>
      )}
    </div>
  );
}
