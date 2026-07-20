import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useCallback, useRef, useState } from "react";
import { api } from "../api/client";
import { ConfirmModal } from "../design-system/confirmmodal";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import { useT } from "../i18n";
import { problemMessage } from "./common";
import "./compose.css";

// The composer surface for the three already-routed ops (draftEmail /
// sendEmail / relinkActivity): a human's edit-then-confirm reply, and a
// mis-captured activity's relink. Pure frontend — every op is live, audited,
// and typed on the backend; this file only calls them.

// The four link targets a relink can point at (relinkActivity's entity_type
// enum). Reused by ComposeModal and TimelineActions so the whole surface
// speaks one vocabulary.
export type RelinkKind = "person" | "organization" | "deal" | "lead";

// The relink target is chosen via cross-object search (/search covers all four
// kinds; the per-entity list endpoints don't all expose `q`). Each candidate's
// entity_type comes from its SearchResult.type, remembered here so the confirm
// can recover it — RecordPickerCandidate itself only carries {id,name}.
// Activity results are dropped: relink's target enum has no `activity`.
function useSearchTargets() {
  const kindById = useRef(new Map<string, RelinkKind>());
  const search = useCallback(
    async (q: string): Promise<RecordPickerCandidate[]> => {
      const { data, error } = await api.GET("/search", {
        params: { query: { q, limit: 10 } },
      });
      if (error) throw new Error(problemMessage(error));
      const out: RecordPickerCandidate[] = [];
      for (const result of data.data) {
        if (result.type === "activity") continue;
        kindById.current.set(result.id, result.type);
        out.push({ id: result.id, name: result.title ?? result.id });
      }
      return out;
    },
    [],
  );
  return { search, kindOf: (id: string) => kindById.current.get(id) ?? null };
}

// A 🟢 internal association (no autonomy dot): move or also-link a captured
// activity's typed link to the right person/org/deal/lead. Idempotent on the
// backend — re-relinking the same target is a no-op that still answers 200.
export function RelinkModal({
  activityId,
  entityType,
  entityId,
  open,
  onClose,
}: Readonly<{
  activityId: string;
  entityType: RelinkKind;
  entityId: string;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const { search, kindOf } = useSearchTargets();
  const [target, setTarget] = useState<RecordPickerCandidate | null>(null);
  const [replace, setReplace] = useState(false);

  const mutation = useMutation({
    mutationFn: async () => {
      const kind = target ? kindOf(target.id) : null;
      if (!target || !kind) {
        // The confirm is disabled without a target, so this only fires if the
        // remembered kind was lost — surface it, never send an empty relink.
        throw new Error(t("compose.relinkTarget"));
      }
      const { data, error } = await api.POST("/activities/{id}/relink", {
        params: {
          path: { id: activityId },
          header: { "Idempotency-Key": crypto.randomUUID() },
        },
        body: {
          entity_type: kind,
          entity_id: target.id,
          replace_existing_of_type: replace,
        },
      });
      if (error) throw new Error(problemMessage(error));
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["activities", entityType, entityId],
      });
      onClose();
    },
  });

  return (
    <ConfirmModal
      open={open}
      onClose={onClose}
      title={t("compose.relinkTitle")}
      confirmLabel={t("compose.relinkConfirm")}
      confirmDisabled={!target}
      onConfirm={() => mutation.mutate()}
      pending={mutation.isPending}
      error={mutation.isError ? mutation.error.message : null}
    >
      <div className="compose-fields">
        <RecordPicker
          label={t("compose.relinkTarget")}
          searchTargets={search}
          onPick={setTarget}
          selected={target}
        />
        <label className="t-body compose-check">
          <input
            type="checkbox"
            checked={replace}
            onChange={(event) => setReplace(event.target.checked)}
          />{" "}
          {t("compose.relinkReplace")}
        </label>
        <p className="t-caption">{t("compose.relinkReplaceHint")}</p>
      </div>
    </ConfirmModal>
  );
}
