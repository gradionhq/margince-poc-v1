import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Badge, Button, Card, Skeleton } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { AutonomyDot } from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";

type Activity = components["schemas"]["Activity"];
type TranscriptReadReport = components["schemas"]["TranscriptReadReport"];

// A transcript is a spoken conversation the rep pasted or uploaded, so only a
// call or a meeting can carry one — `source_system` is an idempotency-key part
// on every kind, and a mail connector stamping some future value there must not
// put a "read this transcript" offer on an email.
export function isTranscriptActivity(activity: Activity): boolean {
  return (
    (activity.kind === "call" || activity.kind === "meeting") &&
    activity.source_system === "transcript"
  );
}

const READ_STATUS_LABELS: Record<TranscriptReadReport["status"], MessageKey> = {
  queued: "transcriptread.statusQueued",
  running: "transcriptread.statusRunning",
  done: "transcriptread.statusDone",
  failed: "transcriptread.statusFailed",
};

// The outcome of a finished reading, as the three answers a rep must be able to
// tell apart. "It stated nothing" and "it could not be read" are different
// facts about the transcript, and one drawn as the other either buries a
// correct empty answer or dresses a failure as one.
function TranscriptReadOutcome({
  report,
}: Readonly<{ report: TranscriptReadReport }>) {
  const t = useT();
  if (report.status === "failed") {
    return (
      <Callout tone="danger" live="status">
        {report.status_detail ?? t("transcriptread.failedFallback")}
      </Callout>
    );
  }
  if (report.proposal_ids.length === 0) {
    return (
      <Callout tone="info" live="status">
        {report.status_detail ?? t("transcriptread.nothingStated")}
      </Callout>
    );
  }
  return (
    <p
      style={{
        display: "flex",
        alignItems: "center",
        gap: "var(--space-2)",
        flexWrap: "wrap",
        margin: "var(--space-3) 0 0",
      }}
    >
      <AutonomyDot tier="confirm" />
      <span className="t-small">
        {report.proposal_ids.length === 1
          ? t("transcriptread.proposalsOne")
          : t("transcriptread.proposals", {
              count: report.proposal_ids.length,
            })}
      </span>
      <Button small onClick={() => navigate({ screen: "inbox" })}>
        {t("enrich.toInbox")}
      </Button>
    </p>
  );
}

// The polled half of the reading: a live status badge while the model works
// (3s poll, stopping the moment the status is terminal), then how much was read
// and what came of it.
function TranscriptReadPanel({
  activityId,
  readId,
}: Readonly<{ activityId: string; readId: string }>) {
  const t = useT();
  const reportQuery = useQuery({
    queryKey: ["transcript-read", activityId, readId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/activities/{id}/transcript-proposals/{readId}",
        { params: { path: { id: activityId, readId } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status === "queued" || status === "running" ? 3000 : false;
    },
  });

  if (reportQuery.isPending) {
    return <Skeleton width="60%" />;
  }
  if (reportQuery.isError) {
    return (
      <p className="t-caption" style={{ color: "var(--danger)" }}>
        {problemMessageOf(reportQuery.error, t)}
      </p>
    );
  }

  const report = reportQuery.data;
  const terminal = report.status === "done" || report.status === "failed";

  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <p
        style={{
          display: "flex",
          alignItems: "center",
          gap: "var(--space-2)",
          flexWrap: "wrap",
          margin: 0,
        }}
      >
        <Badge tone={report.status === "failed" ? "danger" : undefined}>
          {t(READ_STATUS_LABELS[report.status])}
        </Badge>
        {terminal && (
          <span className="t-small">
            {t(
              report.line_count === 1
                ? "transcriptread.lineCount.one"
                : "transcriptread.lineCount.other",
              { count: report.line_count },
            )}
          </span>
        )}
      </p>
      {terminal && <TranscriptReadOutcome report={report} />}
    </div>
  );
}

/**
 * TranscriptReadCard offers one reading of one transcript (S-E04.3): the model
 * reads the normalized lines and stages every next step the conversation
 * STATES as a 🟡 proposal, citing the lines it read them from. Nothing reaches
 * the timeline until a human confirms in the inbox.
 *
 * 422 (this activity carries no transcript) and 501 (no model configured)
 * surface their honest cause rather than a generic failure.
 */
export function TranscriptReadCard({
  activityId,
}: Readonly<{ activityId: string }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [readId, setReadId] = useState<string | null>(null);
  // A read id lives only in the tab that started the reading, so one that
  // finished after the rep navigated away would be unfindable — and a
  // transcript nobody had read would look exactly like one whose reading
  // failed. 404 is the honest "never read" and leaves the card offering a
  // first reading.
  const latest = useQuery({
    queryKey: ["transcript-read-latest", activityId],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/activities/{id}/transcript-proposals/latest",
        { params: { path: { id: activityId } } },
      );
      if (response.status === 404) {
        return null;
      }
      if (error) {
        throwProblem(error);
      }
      return data ?? null;
    },
  });
  const shownReadId = readId ?? latest.data?.read_id ?? null;
  const start = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.POST(
        "/activities/{id}/transcript-proposals",
        { params: { path: { id: activityId } } },
      );
      if (error) {
        // 501 means no model is wired on this server, which the card states in
        // the reader's terms. Either way this stays a problem, so the render
        // below can tell it from a bug in here.
        throwProblem(
          response.status === 501
            ? { title: t("transcriptread.unavailable") }
            : error,
        );
      }
      return data;
    },
    onSuccess: (started) => {
      setReadId(started.read_id);
      // The started reading IS the latest one, so say so rather than leaving
      // the cached answer to expire: without this the card holds a stale
      // "never read" that a rep who navigates away and back inside the window
      // still sees.
      queryClient.invalidateQueries({
        queryKey: ["transcript-read-latest", activityId],
      });
    },
  });

  return (
    <Card
      title={t("transcriptread.title")}
      sub={t("transcriptread.sub")}
      actions={
        <Button small disabled={start.isPending} onClick={() => start.mutate()}>
          {start.isPending
            ? t("transcriptread.starting")
            : t("transcriptread.cta")}
        </Button>
      }
      style={{ marginTop: "var(--space-3)" }}
    >
      {latest.isPending && <Skeleton width="40%" />}
      {start.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(start.error, t)}
        </p>
      )}
      {shownReadId && (
        <TranscriptReadPanel activityId={activityId} readId={shownReadId} />
      )}
    </Card>
  );
}
