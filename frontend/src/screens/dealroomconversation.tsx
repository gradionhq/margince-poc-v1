import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { useT } from "../i18n";
import { QueryStates, throwProblem } from "./common";
import { ThreadPanel } from "./dealroomthreads";

// The seller's side of the conversation and the buyer's decisions, in the
// deal's rail. The verbs are the seller's: reply, open, resolve.

type DealRoom = components["schemas"]["DealRoom"];

export function DealRoomConversation({
  room,
  refusal,
}: Readonly<{ room: DealRoom; refusal: string | undefined }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const threads = useQuery({
    queryKey: ["deal-room-threads", room.id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deal-rooms/{id}/threads", {
        params: { path: { id: room.id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const docs = useQuery({
    queryKey: ["deal-room-documents", room.id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deal-rooms/{id}/documents", {
        params: { path: { id: room.id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: ["deal-room-threads", room.id] });
  const open = useMutation({
    mutationKey: ["deal-room-thread-open"],
    mutationFn: async (input: {
      documentId: string | null;
      body: string;
      requiredChange: boolean;
    }) => {
      const { data, error } = await api.POST("/deal-rooms/{id}/threads", {
        params: { path: { id: room.id } },
        body: {
          document_id: input.documentId,
          body: input.body,
          required_change: input.requiredChange,
          source: "ui",
        },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: refresh,
  });
  const reply = useMutation({
    mutationKey: ["deal-room-thread-reply"],
    mutationFn: async (input: { threadId: string; body: string }) => {
      const { data, error } = await api.POST(
        "/deal-rooms/{id}/threads/{threadId}/comments",
        {
          params: { path: { id: room.id, threadId: input.threadId } },
          body: { body: input.body, source: "ui" },
        },
      );
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: refresh,
  });
  const resolve = useMutation({
    mutationKey: ["deal-room-thread-resolve"],
    mutationFn: async (threadId: string) => {
      const { data, error } = await api.POST(
        "/deal-rooms/{id}/threads/{threadId}/resolve",
        { params: { path: { id: room.id, threadId } } },
      );
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: refresh,
  });
  const documents = (docs.data?.data ?? []).map((d) => ({
    id: d.id,
    title: d.title,
  }));
  const titles = Object.fromEntries(documents.map((d) => [d.id, d.title]));
  const mayWrite = refusal === undefined;
  return (
    <>
      <QueryStates query={threads} pendingLines={3}>
        {threads.data ? (
          <ThreadPanel
            threads={threads.data.data}
            documentTitles={titles}
            verbs={{
              documents,
              mayRequireChange: false,
              refusal,
              open: mayWrite ? (input) => open.mutateAsync(input) : undefined,
              reply: mayWrite
                ? (threadId, body) => reply.mutateAsync({ threadId, body })
                : undefined,
              resolve: mayWrite
                ? (threadId) => resolve.mutateAsync(threadId)
                : undefined,
            }}
          />
        ) : null}
      </QueryStates>
      <DealRoomDecisions room={room} titles={titles} />
    </>
  );
}

function DealRoomDecisions({
  room,
  titles,
}: Readonly<{ room: DealRoom; titles: Record<string, string> }>) {
  const t = useT();
  const decisions = useQuery({
    queryKey: ["deal-room-decisions", room.id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deal-rooms/{id}/decisions", {
        params: { path: { id: room.id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  return (
    <Panel title={t("room.decisions.title")}>
      <QueryStates query={decisions} pendingLines={2}>
        {decisions.data ? (
          decisions.data.data.length === 0 ? (
            <PanelBody>
              <p className="t-small">{t("room.decisions.empty")}</p>
            </PanelBody>
          ) : (
            decisions.data.data.map((d) => (
              <PanelRow key={d.id}>
                <p>
                  <strong>{d.participant_name}</strong>{" "}
                  {t(
                    d.kind === "confirm_version"
                      ? "room.decisions.confirm_version"
                      : "room.decisions.request_changes",
                  )}
                  : {titles[d.document_id] ?? d.document_id}
                </p>
                {d.note ? <p className="t-small">{d.note}</p> : null}
              </PanelRow>
            ))
          )
        ) : null}
      </QueryStates>
    </Panel>
  );
}
