import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, ListChecks, Mail, Sparkles } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import { problemMessageOf, QueryStates, throwProblem } from "./common";
import { ComposeModal } from "./compose";
import { PersonMeetingBrief } from "./persondrawers";
import "./dealnextaction.css";

// The deal's one next move. The READ computes it (a pure GET, retried and
// re-run like every other read on this page, so it must never write); the
// CLICK performs it through the verb the answer names — the task door, the
// compose modal, the meeting-brief drawer — so nothing here re-implements a
// write or skips its gates.

type NextBestAction = components["schemas"]["DealNextBestAction"];

export function DealNextAction({ dealId }: Readonly<{ dealId: string }>) {
  const t = useT();
  const nba = useQuery({
    queryKey: ["deal-next-action", dealId],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/next-best-action", {
        params: { path: { id: dealId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  return (
    <Panel title={t("nba.title")} sub={t("nba.sub")} tone="accent">
      <QueryStates query={nba} pendingLines={2}>
        {nba.data ? <Recommendation dealId={dealId} nba={nba.data} /> : null}
      </QueryStates>
    </Panel>
  );
}

function Recommendation({
  dealId,
  nba,
}: Readonly<{ dealId: string; nba: NextBestAction }>) {
  return (
    <PanelBody>
      <div className="nba">
        <p className="nba-reason">{nba.reason}</p>
        <ActionButton dealId={dealId} nba={nba} />
        {(nba.evidence ?? []).length > 0 ? (
          <ul className="nba-evidence t-small">
            {(nba.evidence ?? []).map((row) => (
              <li key={`${row.activity_id ?? ""}-${row.text}`}>{row.text}</li>
            ))}
          </ul>
        ) : null}
      </div>
    </PanelBody>
  );
}

// activityIdOf reads the one operand the navigation and reply verbs take. The
// arguments object is typed open on the wire; a missing id renders no button
// rather than a button that would 404.
function activityIdOf(nba: NextBestAction): string | null {
  const raw = nba.arguments?.activity_id;
  return typeof raw === "string" && raw !== "" ? raw : null;
}

function ActionButton({
  dealId,
  nba,
}: Readonly<{ dealId: string; nba: NextBestAction }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [composing, setComposing] = useState(false);
  const [briefOpen, setBriefOpen] = useState(false);
  const activityId = activityIdOf(nba);
  const createTask = useMutation({
    mutationKey: ["deal-next-action-create-task"],
    // The arguments ARE the task body the server prepared; the click sends
    // them as they came rather than re-deriving them from render state.
    mutationFn: async (body: Record<string, unknown>) => {
      const { data, error } = await api.POST("/tasks", {
        body: body as components["schemas"]["CreateTaskRequest"],
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["deal-next-action", dealId] });
      queryClient.invalidateQueries({ queryKey: ["activities"] });
      queryClient.invalidateQueries({ queryKey: ["tasks"] });
    },
  });

  const taskBody = nba.arguments;
  switch (nba.action) {
    case "create_task":
      // No body, no button: a click that sent {} would only be refused.
      if (!taskBody) {
        return null;
      }
      return (
        <>
          <Button
            variant="primary"
            pending={createTask.isPending}
            onClick={() => createTask.mutate(taskBody)}
          >
            <ListChecks aria-hidden />
            {t("nba.createTask")}
          </Button>
          {createTask.isError ? (
            <p className="t-small t-danger">
              {problemMessageOf(createTask.error, t)}
            </p>
          ) : null}
        </>
      );
    case "draft_email":
      if (!activityId) {
        return null;
      }
      return (
        <>
          <Button variant="primary" onClick={() => setComposing(true)}>
            <Mail aria-hidden />
            {t("nba.draftReply")}
          </Button>
          {composing ? (
            <ComposeModal
              activityId={activityId}
              entityType="deal"
              entityId={dealId}
              kind="email"
              open={composing}
              onClose={() => setComposing(false)}
            />
          ) : null}
        </>
      );
    case "open_meeting_brief":
      if (!activityId) {
        return null;
      }
      return (
        <>
          <Button variant="primary" onClick={() => setBriefOpen(true)}>
            <Sparkles aria-hidden />
            {t("nba.openBrief")}
          </Button>
          <PersonMeetingBrief
            activityId={activityId}
            open={briefOpen}
            onClose={() => setBriefOpen(false)}
          />
        </>
      );
    default:
      return (
        <p className="t-small nba-none">
          <ArrowRight aria-hidden />
          {t("nba.nothingToDo")}
        </p>
      );
  }
}
