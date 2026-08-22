import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { EyeOff, Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Badge, Button, OverflowMenu } from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelRow } from "../design-system/panel";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { formatDateAbbrev } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { AddDocumentDialog } from "./adddocument";
import { problemMessageOf, throwProblem } from "./common";
import "./dealfiles.css";

// The deal's Files area: what a rep uploaded on the deal, and what arrived
// with the messages linked to it. The second half is why this exists — an
// emailed contract lives on the email, and before this read it was reachable
// from the timeline alone.
//
// Two kinds of row, two verbs. A file uploaded HERE belongs to the deal and
// can be deleted. A captured file belongs to its message: the deal can only
// stop listing it (hide), and the file stays on the activity and in the
// company library. The copy on the hide confirm says exactly that, because
// "Delete" beside "Hide" is the first thing a rep will ask about.

type DealDocument = components["schemas"]["DealDocument"];
type Category = NonNullable<DealDocument["attachment"]["category"]>;

const CATEGORY_LABELS: Record<Category, MessageKey> = {
  contract: "docs.category.contract",
  offer: "docs.category.offer",
  legal: "docs.category.legal",
  email_attachment: "docs.category.email",
  message_attachment: "docs.category.message",
  other: "docs.category.other",
};

// Enough for a working set; the area is not a library somebody pages through.
const PAGE_LIMIT = 100;

export function dealDocumentsKey(dealId: string, includeHidden: boolean) {
  return ["deal-documents", dealId, includeHidden] as const;
}

export function DealFiles({ dealId }: Readonly<{ dealId: string }>) {
  const t = useT();
  const mayWrite = useCanWrite("deal", "update");
  const [adding, setAdding] = useState(false);
  const [showHidden, setShowHidden] = useState(false);
  const query = useQuery({
    queryKey: dealDocumentsKey(dealId, showHidden),
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/documents", {
        params: {
          path: { id: dealId },
          query: { limit: PAGE_LIMIT, include_hidden: showHidden },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const files = query.data?.data ?? [];

  let state: SectionState;
  if (query.isPending) {
    state = "loading";
  } else if (files.length === 0) {
    state = query.isError ? "failed" : "empty";
  } else {
    state = "ready";
  }

  return (
    <Panel
      title={t("files.title")}
      sub={t("files.sub")}
      titleAction={
        mayWrite ? (
          <Button small onClick={() => setAdding(true)}>
            {t("docs.add.action")}
          </Button>
        ) : undefined
      }
      footer={
        <Button small variant="ghost" onClick={() => setShowHidden((s) => !s)}>
          {showHidden ? t("files.hideHidden") : t("files.showHidden")}
        </Button>
      }
    >
      <AddDocumentDialog
        anchor={{ record: "deal", id: dealId }}
        open={adding}
        onClose={() => setAdding(false)}
      />
      <SurfaceState
        state={state}
        emptyLabel={t("files.empty")}
        detail={
          state === "failed"
            ? { onRetry: () => void query.refetch() }
            : undefined
        }
      >
        {files.map((doc) => (
          <FileRow
            key={doc.attachment.id}
            dealId={dealId}
            doc={doc}
            mayWrite={mayWrite}
          />
        ))}
      </SurfaceState>
    </Panel>
  );
}

function FileRow({
  dealId,
  doc,
  mayWrite,
}: Readonly<{ dealId: string; doc: DealDocument; mayWrite: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  const file = doc.attachment;
  const [confirming, setConfirming] = useState<"delete" | "hide" | null>(null);
  const verbs = useFileVerbs(dealId, file.id);
  return (
    <PanelRow
      className={doc.hidden ? "deal-file deal-file-hidden" : "deal-file"}
    >
      <div className="deal-file-main">
        <a
          className="link-button"
          href={`/v1/attachments/${file.id}`}
          download={file.filename}
        >
          {file.title || file.filename}
        </a>
        <p className="t-small deal-file-origin">
          {doc.origin
            ? t("files.origin", {
                who: doc.origin.counterparty_email ?? t("files.originUnknown"),
                when: formatDateAbbrev(
                  doc.origin.occurred_at,
                  locale,
                  RECORD_ZONE,
                ),
              })
            : t("files.uploaded", {
                when: formatDateAbbrev(file.created_at, locale, RECORD_ZONE),
              })}
        </p>
      </div>
      <div className="deal-file-side">
        {file.category ? (
          <Badge>{t(CATEGORY_LABELS[file.category])}</Badge>
        ) : null}
        {doc.hidden ? <Badge>{t("files.hiddenBadge")}</Badge> : null}
        {mayWrite ? (
          <FileMenu
            doc={doc}
            unhide={verbs.unhide}
            onHide={() => setConfirming("hide")}
            onDelete={() => setConfirming("delete")}
          />
        ) : null}
      </div>
      <ConfirmModal
        open={confirming === "hide"}
        onClose={() => setConfirming(null)}
        title={t("files.hideTitle", { name: file.filename })}
        confirmLabel={t("files.hide")}
        pending={verbs.hide.isPending}
        error={
          verbs.hide.isError ? problemMessageOf(verbs.hide.error, t) : null
        }
        onConfirm={() =>
          verbs.hide.mutate(undefined, { onSuccess: () => setConfirming(null) })
        }
      >
        <p>{t("files.hideBody")}</p>
      </ConfirmModal>
      <ConfirmModal
        open={confirming === "delete"}
        onClose={() => setConfirming(null)}
        title={t("files.deleteTitle", { name: file.filename })}
        confirmLabel={t("files.delete")}
        confirmVariant="danger"
        pending={verbs.remove.isPending}
        error={
          verbs.remove.isError ? problemMessageOf(verbs.remove.error, t) : null
        }
        onConfirm={() =>
          verbs.remove.mutate(undefined, {
            onSuccess: () => setConfirming(null),
          })
        }
      >
        <p>{t("files.deleteBody")}</p>
      </ConfirmModal>
    </PanelRow>
  );
}

// The row's verbs: a captured file can be hidden or shown again, an upload
// deleted. The menu is its own component so the row stays readable.
function FileMenu({
  doc,
  unhide,
  onHide,
  onDelete,
}: Readonly<{
  doc: DealDocument;
  unhide: ReturnType<typeof useFileVerbs>["unhide"];
  onHide: () => void;
  onDelete: () => void;
}>) {
  const t = useT();
  const captured = doc.origin !== undefined;
  return (
    <OverflowMenu
      label={t("files.rowActions", { name: doc.attachment.filename })}
    >
      {captured && !doc.hidden ? (
        <Button small variant="ghost" onClick={onHide}>
          <EyeOff aria-hidden />
          {t("files.hide")}
        </Button>
      ) : null}
      {captured && doc.hidden ? (
        <Button
          small
          variant="ghost"
          pending={unhide.isPending}
          onClick={() => unhide.mutate()}
        >
          {t("files.unhide")}
        </Button>
      ) : null}
      {!captured ? (
        <Button small variant="ghost" onClick={onDelete}>
          <Trash2 aria-hidden />
          {t("files.delete")}
        </Button>
      ) : null}
    </OverflowMenu>
  );
}

// The three writes a row offers. Each refreshes both views of the area (with
// and without hidden rows), since a hide moves a row from one to the other.
function useFileVerbs(dealId: string, attachmentId: string) {
  const t = useT();
  const queryClient = useQueryClient();
  // Both spellings of "the deal's files": this area's own key and the one the
  // Deal Room's picker reads, so a delete here never leaves a ghost there.
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["deal-documents", dealId] }),
      queryClient.invalidateQueries({ queryKey: ["deal-attachments", dealId] }),
    ]);
  const hide = useMutation({
    mutationFn: async () => {
      const { error } = await api.PUT(
        "/deals/{id}/documents/{attachmentId}/hide",
        {
          params: { path: { id: dealId, attachmentId } },
        },
      );
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: refresh,
  });
  const unhide = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE(
        "/deals/{id}/documents/{attachmentId}/hide",
        {
          params: { path: { id: dealId, attachmentId } },
        },
      );
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/attachments/{id}", {
        params: { path: { id: attachmentId } },
      });
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: refresh,
  });
  return { hide, unhide, remove };
}
