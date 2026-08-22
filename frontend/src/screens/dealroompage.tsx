import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Eye, Pause, Play, Send, Square } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { useCanWrite } from "../app/capability";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  Field,
  OverflowMenu,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { formatDateAbbrev } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryStates, throwProblem } from "./common";
import {
  FINISHED_STATES,
  refusalFor,
  STATE_LABELS,
  useDealRoom,
} from "./dealroom";
import { buyerLink, DealRoomAccess, useParticipants } from "./dealroomaccess";
import { DealRoomConversation } from "./dealroomconversation";
import { DealRoomDocuments, useRoomDocuments } from "./dealroomdocuments";
import "./dealroompage.css";

// The seller's Deal Room page: one place to decide who may enter, what they
// read, when it goes out, and to hold the conversation. Reached from the deal
// page; the deal page keeps only a card that points here.
//
// Publish is the one verb that reaches a buyer. It is disabled with the reason
// when there is nothing to publish, and the list of pending changes above it
// is computed by the server from the frozen release, so the button and the
// list can never disagree.

type DealRoom = components["schemas"]["DealRoom"];
type DealRoomChange = components["schemas"]["DealRoomChange"];

const CHANGE_LABELS: Record<string, MessageKey> = {
  title_changed: "publish.change.title",
  welcome_changed: "publish.change.welcome",
  document_added: "publish.change.added",
  document_removed: "publish.change.removed",
  document_retitled: "publish.change.retitled",
  document_regrouped: "publish.change.regrouped",
  document_reordered: "publish.change.reordered",
  document_ineligible: "publish.change.ineligible",
};

export function changesKey(roomId: string) {
  return ["deal-room-changes", roomId] as const;
}

export function useRoomChanges(roomId: string, enabled = true) {
  return useQuery({
    queryKey: changesKey(roomId),
    enabled,
    queryFn: async () => {
      const { data, error } = await api.GET("/deal-rooms/{id}/changes", {
        params: { path: { id: roomId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

export function DealRoomPage({ dealId }: Readonly<{ dealId: string }>) {
  const t = useT();
  const roomQuery = useDealRoom(dealId);
  const room = roomQuery.data?.data?.[0];
  return (
    <QueryStates query={roomQuery} pendingLines={6}>
      {room ? (
        <RoomPage room={room} dealId={dealId} />
      ) : roomQuery.isSuccess ? (
        <Callout tone="info">{t("roompage.none")}</Callout>
      ) : null}
    </QueryStates>
  );
}

function RoomPage({
  room,
  dealId,
}: Readonly<{ room: DealRoom; dealId: string }>) {
  const t = useT();
  const mayWrite = useCanWrite("deal_room", "update");
  const finished = FINISHED_STATES.has(room.state);
  const refusal = refusalFor(finished, mayWrite, t);
  const docs = useRoomDocuments(room.id);
  const hasDocuments = (docs.data?.data?.length ?? 0) > 0;
  return (
    <div className="roompage">
      <header className="roompage-head">
        <div>
          <p className="t-small">
            <button
              type="button"
              className="link-button"
              onClick={() => navigate({ screen: "deals", id: dealId })}
            >
              {t("roompage.backToDeal")}
            </button>
          </p>
          <h1>{room.title}</h1>
        </div>
        <div className="roompage-verbs">
          <Badge>{t(STATE_LABELS[room.state])}</Badge>
          {mayWrite ? <ViewAsBuyerButton room={room} /> : null}
          {mayWrite ? <LifecycleMenu room={room} /> : null}
          {mayWrite ? <PublishButton room={room} /> : null}
        </div>
      </header>
      <StateBanner room={room} />
      <div className="roompage-grid">
        <div className="roompage-main">
          <RoomText room={room} refusal={refusal} />
          <DealRoomDocuments room={room} state={null} refusal={refusal} />
          <DealRoomConversation room={room} refusal={refusal} />
        </div>
        <div className="roompage-side">
          <DealRoomAccess
            room={room}
            mayManage={mayWrite}
            hasDocuments={hasDocuments}
          />
          <PublishPanel room={room} />
        </div>
      </div>
    </div>
  );
}

// "View as buyer": a real buyer session, minted for the rep's own hidden
// preview seat and opened through the public screen — so what the rep sees
// is what the buyer gets, release and all. The credential rides in the new
// tab's fragment exactly as a mailed link would, and is never kept here.
function ViewAsBuyerButton({ room }: Readonly<{ room: DealRoom }>) {
  const t = useT();
  const preview = useMutation({
    mutationFn: async (roomId: string) => {
      const { data, error } = await api.POST("/deal-rooms/{id}/preview", {
        params: { path: { id: roomId } },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: (data) => {
      if (data) {
        window.open(buyerLink(data.credential), "_blank", "noopener");
      }
    },
  });
  const reason =
    room.state === "draft"
      ? t("roompage.previewDraft")
      : room.state === "archived"
        ? t("roompage.previewArchived")
        : undefined;
  return (
    <>
      <Button
        reason={reason}
        pending={preview.isPending}
        onClick={() => preview.mutate(room.id)}
      >
        <ExternalLink aria-hidden />
        {t("roompage.viewAsBuyer")}
      </Button>
      {preview.isError ? (
        <span className="t-small t-danger">
          {problemMessageOf(preview.error, t)}
        </span>
      ) : null}
    </>
  );
}

// The state, said in the words a rep needs: what the buyer sees right now,
// and the way back where there is one.
function StateBanner({ room }: Readonly<{ room: DealRoom }>) {
  const t = useT();
  const { locale } = useLocale();
  switch (room.state) {
    case "draft":
      return <Callout tone="info">{t("roompage.banner.draft")}</Callout>;
    case "paused":
      return <Callout tone="warn">{t("roompage.banner.paused")}</Callout>;
    case "closed":
      return <Callout tone="info">{t("roompage.banner.closed")}</Callout>;
    case "expired":
      return <Callout tone="warn">{t("roompage.banner.expired")}</Callout>;
    case "archived":
      return <Callout tone="danger">{t("roompage.banner.archived")}</Callout>;
    default:
      return room.expires_at ? (
        <Callout tone="info">
          {t("roompage.banner.liveUntil", {
            when: formatDateAbbrev(room.expires_at, locale, RECORD_ZONE),
          })}
        </Callout>
      ) : null;
  }
}

function useRoomVerb(roomId: string, dealId: string) {
  const t = useT();
  const queryClient = useQueryClient();
  return {
    t,
    refresh: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: ["deal-rooms", dealId] }),
        queryClient.invalidateQueries({ queryKey: changesKey(roomId) }),
        queryClient.invalidateQueries({
          queryKey: ["deal-room-releases", roomId],
        }),
      ]),
  };
}

function LifecycleMenu({ room }: Readonly<{ room: DealRoom }>) {
  const { t, refresh } = useRoomVerb(room.id, room.deal_id);
  const [expiring, setExpiring] = useState(false);
  const [closing, setClosing] = useState(false);
  const move = useMutation({
    mutationFn: async (verb: "pause" | "resume" | "close") => {
      const path = `/deal-rooms/{id}/${verb}` as const;
      const { error } = await api.POST(path, {
        params: { path: { id: room.id } },
      });
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: () => {
      refresh();
      setClosing(false);
    },
  });
  return (
    <>
      <OverflowMenu label={t("roompage.accessMenu")}>
        {room.state === "live" ? (
          <Button
            small
            variant="ghost"
            pending={move.isPending}
            onClick={() => move.mutate("pause")}
          >
            <Pause aria-hidden />
            {t("roompage.pause")}
            <span className="t-small roompage-menu-hint">
              {t("roompage.pauseHint")}
            </span>
          </Button>
        ) : null}
        {room.state === "paused" ? (
          <Button
            small
            variant="ghost"
            pending={move.isPending}
            onClick={() => move.mutate("resume")}
          >
            <Play aria-hidden />
            {t("roompage.resume")}
          </Button>
        ) : null}
        {room.state === "live" || room.state === "paused" ? (
          <Button small variant="ghost" onClick={() => setClosing(true)}>
            <Square aria-hidden />
            {t("roompage.close")}
            <span className="t-small roompage-menu-hint">
              {t("roompage.closeHint")}
            </span>
          </Button>
        ) : null}
        {!FINISHED_STATES.has(room.state) ? (
          <Button small variant="ghost" onClick={() => setExpiring(true)}>
            {t("roompage.setExpiry")}
            <span className="t-small roompage-menu-hint">
              {t("roompage.setExpiryHint")}
            </span>
          </Button>
        ) : null}
      </OverflowMenu>
      {move.isError ? (
        <p className="t-small t-danger">{problemMessageOf(move.error, t)}</p>
      ) : null}
      <ConfirmModal
        open={closing}
        onClose={() => setClosing(false)}
        title={t("roompage.closeTitle")}
        confirmLabel={t("roompage.close")}
        confirmVariant="danger"
        pending={move.isPending}
        error={move.isError ? problemMessageOf(move.error, t) : null}
        onConfirm={() => move.mutate("close")}
      >
        <p>{t("roompage.closeBody")}</p>
      </ConfirmModal>
      <ExpiryDialog
        room={room}
        open={expiring}
        onClose={() => setExpiring(false)}
      />
    </>
  );
}

function ExpiryDialog({
  room,
  open,
  onClose,
}: Readonly<{ room: DealRoom; open: boolean; onClose: () => void }>) {
  const { t, refresh } = useRoomVerb(room.id, room.deal_id);
  const [date, setDate] = useState(
    room.expires_at ? room.expires_at.slice(0, 10) : "",
  );
  const set = useMutation({
    // The version rides in the variables: a click between a room refresh and
    // the mutation re-arming would otherwise pin a version already gone.
    mutationFn: async (input: { day: string; version: number }) => {
      const { error } = await api.PUT("/deal-rooms/{id}/expiry", {
        params: { path: { id: room.id }, ...ifMatch(input.version) },
        body: {
          expires_at:
            input.day === ""
              ? null
              : new Date(`${input.day}T23:59:59Z`).toISOString(),
        },
      });
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: () => {
      refresh();
      onClose();
    },
  });
  return (
    <ConfirmModal
      open={open}
      onClose={onClose}
      title={t("roompage.setExpiry")}
      confirmLabel={t("access.save")}
      pending={set.isPending}
      error={set.isError ? problemMessageOf(set.error, t) : null}
      onConfirm={() =>
        set.mutate({ day: date, version: requireVersion(room.version) })
      }
    >
      <Field label={t("roompage.expiryLabel")} hint={t("roompage.expiryHint")}>
        {(control) => (
          <TextInput
            {...control}
            type="date"
            value={date}
            onChange={(e) => setDate(e.target.value)}
          />
        )}
      </Field>
    </ConfirmModal>
  );
}

// Title and welcome text, edited in place. Editorial: reaches the buyer at
// the next publish, and the changes list below says so.
function RoomText({
  room,
  refusal,
}: Readonly<{ room: DealRoom; refusal: string | undefined }>) {
  const { t, refresh } = useRoomVerb(room.id, room.deal_id);
  const [title, setTitle] = useState(room.title);
  const [welcome, setWelcome] = useState(room.welcome_message ?? "");
  const save = useMutation({
    mutationFn: async (input: {
      title: string;
      welcome: string;
      version: number;
    }) => {
      const { error } = await api.PATCH("/deal-rooms/{id}", {
        params: { path: { id: room.id }, ...ifMatch(input.version) },
        body: {
          title: input.title,
          welcome_message: input.welcome === "" ? null : input.welcome,
        },
      });
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: refresh,
  });
  const dirty =
    title !== room.title || welcome !== (room.welcome_message ?? "");
  return (
    <Panel title={t("roompage.text.title")} sub={t("roompage.text.sub")}>
      <PanelBody>
        <div className="form-stack">
          <Field label={t("roompage.text.titleLabel")}>
            {(control) => (
              <TextInput
                {...control}
                value={title}
                disabled={refusal !== undefined}
                onChange={(e) => setTitle(e.target.value)}
              />
            )}
          </Field>
          <Field label={t("roompage.text.welcomeLabel")}>
            {(control) => (
              <Textarea
                {...control}
                rows={3}
                value={welcome}
                disabled={refusal !== undefined}
                onChange={(e) => setWelcome(e.target.value)}
              />
            )}
          </Field>
          {refusal ? (
            <p className="t-small">{refusal}</p>
          ) : (
            <div className="card-actions">
              <Button
                small
                disabled={!dirty || title.trim() === ""}
                pending={save.isPending}
                onClick={() =>
                  save.mutate({
                    title: title.trim(),
                    welcome: welcome.trim(),
                    version: requireVersion(room.version),
                  })
                }
              >
                {t("access.save")}
              </Button>
              {save.isError ? (
                <span className="t-small t-danger">
                  {problemMessageOf(save.error, t)}
                </span>
              ) : null}
            </div>
          )}
        </div>
      </PanelBody>
    </Panel>
  );
}

function PublishButton({ room }: Readonly<{ room: DealRoom }>) {
  const { t, refresh } = useRoomVerb(room.id, room.deal_id);
  const changes = useRoomChanges(room.id);
  const [confirming, setConfirming] = useState(false);
  const [note, setNote] = useState("");
  const publish = useMutation({
    mutationFn: async (releaseNote: string) => {
      const { error } = await api.POST("/deal-rooms/{id}/publish", {
        params: { path: { id: room.id } },
        body: releaseNote === "" ? {} : { release_note: releaseNote },
      });
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: () => {
      refresh();
      setConfirming(false);
      setNote("");
    },
  });
  const finished = FINISHED_STATES.has(room.state);
  // Enabled only once the server has said there IS something to publish: a
  // click before that answer, or after a failed one, would go out unreviewed.
  const reason = finished
    ? t("roompage.publishFinished")
    : changes.isPending
      ? t("roompage.publishChecking")
      : changes.isError
        ? t("roompage.publishUnknown")
        : !changes.data?.has_changes
          ? t("roompage.publishNothing")
          : undefined;
  return (
    <>
      <Button
        variant="primary"
        reason={reason}
        onClick={() => setConfirming(true)}
      >
        <Send aria-hidden />
        {t("roompage.publish")}
      </Button>
      <ConfirmModal
        open={confirming}
        onClose={() => setConfirming(false)}
        title={t("roompage.publishTitle")}
        confirmLabel={t("roompage.publish")}
        pending={publish.isPending}
        error={publish.isError ? problemMessageOf(publish.error, t) : null}
        onConfirm={() => publish.mutate(note.trim())}
      >
        <ChangeList changes={changes.data?.changes ?? []} />
        <Field label={t("roompage.publishNoteLabel")}>
          {(control) => (
            <TextInput
              {...control}
              value={note}
              onChange={(e) => setNote(e.target.value)}
            />
          )}
        </Field>
      </ConfirmModal>
    </>
  );
}

function ChangeList({ changes }: Readonly<{ changes: DealRoomChange[] }>) {
  const t = useT();
  if (changes.length === 0) {
    return <p className="t-small">{t("publish.noChanges")}</p>;
  }
  return (
    <ul className="roompage-changes">
      {changes.map((c, i) => (
        <li key={`${c.kind}-${c.document_id ?? i}`}>
          {t(CHANGE_LABELS[c.kind] ?? "publish.change.other", {
            title: c.title ?? "",
          })}
        </li>
      ))}
    </ul>
  );
}

function PublishPanel({ room }: Readonly<{ room: DealRoom }>) {
  const t = useT();
  const { locale } = useLocale();
  const changes = useRoomChanges(room.id);
  const releases = useQuery({
    queryKey: ["deal-room-releases", room.id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deal-rooms/{id}/releases", {
        params: { path: { id: room.id }, query: { limit: 10 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const participants = useParticipants(room.id);
  const active = (participants.data?.data ?? []).filter(
    (p) => !p.revoked_at && p.has_signed_in,
  ).length;
  return (
    <Panel
      title={t("publish.title")}
      sub={t("publish.sub")}
      titleAction={
        changes.data?.has_changes ? (
          <Badge>{t("publish.pendingBadge")}</Badge>
        ) : undefined
      }
    >
      <PanelBody>
        <QueryStates query={changes} pendingLines={2}>
          <ChangeList changes={changes.data?.changes ?? []} />
        </QueryStates>
      </PanelBody>
      <QueryStates query={releases} pendingLines={1}>
        {(releases.data?.data ?? []).map((rel) => (
          <PanelRow key={rel.id}>
            <p>
              {t("publish.release", { no: String(rel.release_no) })}
              <span className="t-small">
                {" "}
                · {formatDateAbbrev(rel.published_at, locale, RECORD_ZONE)}
              </span>
            </p>
            {rel.release_note ? (
              <p className="t-small">{rel.release_note}</p>
            ) : null}
          </PanelRow>
        ))}
      </QueryStates>
      <PanelBody>
        <p className="t-small">
          <Eye aria-hidden /> {t("publish.readers", { count: String(active) })}
        </p>
      </PanelBody>
    </Panel>
  );
}
