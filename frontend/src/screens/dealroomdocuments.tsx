import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { Button, EmptyState, Field } from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryStates, throwProblem } from "./common";
import "./dealroomdocuments.css";

// The seller's side of a Deal Room's documents: which of the deal's files the
// buyer gets to read, under which of the four fixed groups. Editorial like the
// to-do list — nothing here reaches the buyer until the room is published.

type DealRoom = components["schemas"]["DealRoom"];
type DealRoomDocument = components["schemas"]["DealRoomDocument"];

// The four groups are machine keys on the wire; these are their names.
export const DOCUMENT_GROUPS: readonly {
  key: string;
  labelKey: MessageKey;
}[] = [
  { key: "commercial", labelKey: "room.docs.group.commercial" },
  { key: "legal", labelKey: "room.docs.group.legal" },
  { key: "security_privacy", labelKey: "room.docs.group.security_privacy" },
  {
    key: "delivery_operations",
    labelKey: "room.docs.group.delivery_operations",
  },
];

export function groupLabelKey(key: string): MessageKey {
  return (
    DOCUMENT_GROUPS.find((g) => g.key === key)?.labelKey ??
    "room.docs.group.commercial"
  );
}

export function DealRoomDocuments({
  room,
  refusal,
}: Readonly<{ room: DealRoom; refusal: string | undefined }>) {
  const t = useT();
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
  return (
    <Panel title={t("room.docs.title")} sub={t("room.docs.sub")}>
      <QueryStates query={docs} pendingLines={3}>
        {docs.data ? (
          <DocumentList
            room={room}
            docs={docs.data.data ?? []}
            refusal={refusal}
          />
        ) : null}
      </QueryStates>
      <PanelBody>
        <AddDocument room={room} refusal={refusal} />
      </PanelBody>
    </Panel>
  );
}

function DocumentList({
  room,
  docs,
  refusal,
}: Readonly<{
  room: DealRoom;
  docs: DealRoomDocument[];
  refusal: string | undefined;
}>) {
  const t = useT();
  if (docs.length === 0) {
    return (
      <PanelBody>
        <EmptyState>
          <p className="t-small">{t("room.docs.empty")}</p>
        </EmptyState>
      </PanelBody>
    );
  }
  return (
    <>
      {docs.map((doc) => (
        <DocumentRow key={doc.id} room={room} doc={doc} refusal={refusal} />
      ))}
    </>
  );
}

function DocumentRow({
  room,
  doc,
  refusal,
}: Readonly<{
  room: DealRoom;
  doc: DealRoomDocument;
  refusal: string | undefined;
}>) {
  const t = useT();
  const remove = useRemoveDocument(room.id);
  return (
    <PanelRow>
      <div className="room-doc">
        <div>
          <p>{doc.title}</p>
          <p className="t-small">
            {t(groupLabelKey(doc.group_key))}
            {doc.filename && doc.filename !== doc.title
              ? ` · ${doc.filename}`
              : ""}
          </p>
        </div>
        <Button
          small
          iconOnly
          aria-label={t("room.docs.remove", { title: doc.title })}
          reason={refusal}
          pending={remove.isPending}
          onClick={() =>
            remove.mutate({ documentId: doc.id, version: doc.version })
          }
        >
          <Trash2 aria-hidden />
        </Button>
      </div>
      {remove.isError ? (
        <p className="t-small t-danger">{problemMessageOf(remove.error, t)}</p>
      ) : null}
    </PanelRow>
  );
}

function AddDocument({
  room,
  refusal,
}: Readonly<{ room: DealRoom; refusal: string | undefined }>) {
  const t = useT();
  const [attachmentId, setAttachmentId] = useState("");
  const [group, setGroup] = useState(DOCUMENT_GROUPS[0].key);
  const files = useQuery({
    queryKey: ["deal-attachments", room.deal_id],
    enabled: refusal === undefined,
    queryFn: async () => {
      const { data, error } = await api.GET("/attachments", {
        params: {
          query: { entity_type: "deal", entity_id: room.deal_id, limit: 100 },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const add = useAddDocument(room.id);
  if (refusal !== undefined) {
    return <p className="t-small">{refusal}</p>;
  }
  const options = (files.data?.data ?? []).map((file) => ({
    value: file.id,
    label: file.filename,
  }));
  return (
    <>
      <Field label={t("room.docs.fileLabel")} hint={t("room.docs.fileHint")}>
        {(control) => (
          <Select
            id={control.id}
            options={options}
            value={attachmentId}
            onChange={setAttachmentId}
            placeholder={
              options.length === 0
                ? t("room.docs.noFiles")
                : t("room.docs.pickFile")
            }
            disabled={options.length === 0}
          />
        )}
      </Field>
      <Field label={t("room.docs.groupLabel")}>
        {(control) => (
          <Select
            id={control.id}
            options={DOCUMENT_GROUPS.map((g) => ({
              value: g.key,
              label: t(g.labelKey),
            }))}
            value={group}
            onChange={setGroup}
          />
        )}
      </Field>
      <div className="card-actions">
        <Button
          small
          disabled={attachmentId === ""}
          pending={add.isPending}
          onClick={() =>
            add.mutate(
              { attachmentId, group },
              { onSuccess: () => setAttachmentId("") },
            )
          }
        >
          {t("room.docs.add")}
        </Button>
      </div>
      <p className="t-small">{t("room.tasks.editorial")}</p>
      {add.isError ? (
        <p className="t-small t-danger">{problemMessageOf(add.error, t)}</p>
      ) : null}
    </>
  );
}

function useAddDocument(roomId: string) {
  const t = useT();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: ["deal-room-document-add"],
    mutationFn: async (input: { attachmentId: string; group: string }) => {
      const { data, error } = await api.POST("/deal-rooms/{id}/documents", {
        params: { path: { id: roomId } },
        body: {
          attachment_id: input.attachmentId,
          group_key: input.group,
          source: "ui",
        },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["deal-room-documents", roomId],
      });
    },
  });
}

function useRemoveDocument(roomId: string) {
  const t = useT();
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: ["deal-room-document-remove"],
    mutationFn: async (input: {
      documentId: string;
      version: number | undefined;
    }) => {
      const { data, error } = await api.DELETE(
        "/deal-rooms/{id}/documents/{documentId}",
        {
          params: {
            path: { id: roomId, documentId: input.documentId },
            ...ifMatch(requireVersion(input.version)),
          },
        },
      );
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["deal-room-documents", roomId],
      });
    },
  });
}
