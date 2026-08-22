import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Link2, UserX } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  Field,
  OverflowMenu,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ChoiceList } from "../design-system/choicelist";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { formatDateAbbrev } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryStates, throwProblem } from "./common";
import "./dealroomaccess.css";

// Who may enter the room, and the verbs that change it: invite, issue a new
// link, change what a person may do, revoke. Every link a rep is handed here
// is shown ONCE, with Copy — the server never stores it in clear, and dev has
// no mail relay, so the rep pasting it into a chat is the normal path, not the
// fallback.
//
// Capability is a required, explained choice. The API defaults to `view`,
// and a lawyer invited with the default could read the contract and never
// confirm it — which the rep would discover a week later.

type DealRoom = components["schemas"]["DealRoom"];
type Participant = components["schemas"]["DealRoomParticipant"];
type Capability = components["schemas"]["DealRoomParticipantCapability"];
type Issued = components["schemas"]["DealRoomInvitationIssued"];

const CAPABILITIES: readonly Capability[] = ["view", "comment", "reviewer"];

const CAPABILITY_LABELS: Record<Capability, MessageKey> = {
  view: "access.cap.view",
  comment: "access.cap.comment",
  reviewer: "access.cap.reviewer",
};

const CAPABILITY_HINTS: Record<Capability, MessageKey> = {
  view: "access.cap.viewHint",
  comment: "access.cap.commentHint",
  reviewer: "access.cap.reviewerHint",
};

export function participantsKey(roomId: string) {
  return ["deal-room-participants", roomId] as const;
}

export function useParticipants(roomId: string) {
  return useQuery({
    queryKey: participantsKey(roomId),
    queryFn: async () => {
      const { data, error } = await api.GET("/deal-rooms/{id}/participants", {
        params: { path: { id: roomId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

/** The link a buyer opens: the public screen with the credential in the fragment. */
export function buyerLink(credential: string): string {
  return `${window.location.origin}/#/room?c=${encodeURIComponent(credential)}`;
}

export function DealRoomAccess({
  room,
  mayManage,
  hasDocuments,
}: Readonly<{ room: DealRoom; mayManage: boolean; hasDocuments: boolean }>) {
  const t = useT();
  const [inviting, setInviting] = useState(false);
  const participants = useParticipants(room.id);
  const rows = participants.data?.data ?? [];
  const live = rows.filter((p) => !p.revoked_at);
  const noReviewer =
    hasDocuments && live.every((p) => p.capability !== "reviewer");
  return (
    <Panel
      title={t("access.title")}
      sub={t("access.sub")}
      titleAction={
        mayManage ? (
          <Button small onClick={() => setInviting(true)}>
            {t("access.invite")}
          </Button>
        ) : undefined
      }
    >
      {noReviewer ? (
        <PanelBody>
          <Callout tone="warn">{t("access.noReviewer")}</Callout>
        </PanelBody>
      ) : null}
      <QueryStates query={participants} pendingLines={2}>
        {rows.length === 0 ? (
          <PanelBody>
            <p className="t-small">{t("access.empty")}</p>
          </PanelBody>
        ) : (
          rows.map((p) => (
            <ParticipantRow
              key={p.id}
              room={room}
              participant={p}
              mayManage={mayManage}
            />
          ))
        )}
      </QueryStates>
      <InviteDialog
        room={room}
        open={inviting}
        onClose={() => setInviting(false)}
      />
    </Panel>
  );
}

function ParticipantRow({
  room,
  participant,
  mayManage,
}: Readonly<{ room: DealRoom; participant: Participant; mayManage: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  const [confirming, setConfirming] = useState<
    "revoke" | "reissue" | "capability" | null
  >(null);
  const revoked =
    participant.revoked_at !== null && participant.revoked_at !== undefined;
  return (
    <PanelRow
      className={revoked ? "access-row access-row-revoked" : "access-row"}
    >
      <div className="access-row-main">
        <p>
          {participant.full_name}
          <span className="t-small access-row-email">
            {" "}
            · {participant.email}
          </span>
        </p>
        <p className="t-small access-row-facts">
          {t(CAPABILITY_LABELS[participant.capability])}
          {" · "}
          {revoked
            ? t("access.state.revoked")
            : participant.has_signed_in
              ? t("access.state.active")
              : t("access.state.invited")}
          {participant.last_seen_at
            ? ` · ${t("access.lastSeen", { when: formatDateAbbrev(participant.last_seen_at, locale, RECORD_ZONE) })}`
            : ""}
        </p>
        {participant.link_requested_at && !revoked ? (
          <p className="t-small access-row-request">
            <Link2 aria-hidden />
            {t("access.linkRequested", {
              when: formatDateAbbrev(
                participant.link_requested_at,
                locale,
                RECORD_ZONE,
              ),
            })}
          </p>
        ) : null}
      </div>
      <div className="access-row-side">
        {revoked ? <Badge>{t("access.state.revoked")}</Badge> : null}
        {mayManage && !revoked ? (
          <OverflowMenu
            label={t("access.rowActions", { name: participant.full_name })}
          >
            <Button
              small
              variant="ghost"
              onClick={() => setConfirming("reissue")}
            >
              <Link2 aria-hidden />
              {t("access.issueLink")}
            </Button>
            <Button
              small
              variant="ghost"
              onClick={() => setConfirming("capability")}
            >
              {t("access.changeCapability")}
            </Button>
            <Button
              small
              variant="ghost"
              onClick={() => setConfirming("revoke")}
            >
              <UserX aria-hidden />
              {t("access.revoke")}
            </Button>
          </OverflowMenu>
        ) : null}
      </div>
      <RevokeDialog
        room={room}
        participant={participant}
        open={confirming === "revoke"}
        onClose={() => setConfirming(null)}
      />
      <ReissueDialog
        room={room}
        participant={participant}
        open={confirming === "reissue"}
        onClose={() => setConfirming(null)}
      />
      <CapabilityDialog
        room={room}
        participant={participant}
        open={confirming === "capability"}
        onClose={() => setConfirming(null)}
      />
    </PanelRow>
  );
}

// The link, shown once. Copy is the verb; the sentence under it is the thing
// a rep most needs to know and will otherwise learn from a locked-out buyer.
function IssuedLink({ issued }: Readonly<{ issued: Issued }>) {
  const t = useT();
  const [copied, setCopied] = useState<"done" | "failed" | null>(null);
  const link = buyerLink(issued.credential);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(link);
      setCopied("done");
    } catch {
      setCopied("failed");
    }
  };
  return (
    <div className="access-issued">
      <Callout tone={issued.delivered ? "success" : "info"}>
        {issued.delivered
          ? t("access.issued.mailed", { email: issued.participant.email })
          : t("access.issued.notMailed")}
      </Callout>
      <Field label={t("access.issued.linkLabel")}>
        {(control) => <TextInput {...control} readOnly value={link} />}
      </Field>
      <div className="card-actions">
        <Button small onClick={copy}>
          <Copy aria-hidden />
          {copied === "done"
            ? t("access.issued.copied")
            : t("access.issued.copy")}
        </Button>
        {copied === "failed" ? (
          <span className="t-small t-danger">
            {t("access.issued.copyFailed")}
          </span>
        ) : null}
      </div>
      <p className="t-small">{t("access.issued.oneTime")}</p>
    </div>
  );
}

function InviteDialog({
  room,
  open,
  onClose,
}: Readonly<{ room: DealRoom; open: boolean; onClose: () => void }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [capability, setCapability] = useState<Capability>("comment");
  const [issued, setIssued] = useState<Issued | null>(null);
  const invite = useMutation({
    mutationFn: async (input: {
      name: string;
      email: string;
      capability: Capability;
    }) => {
      const { data, error } = await api.POST("/deal-rooms/{id}/participants", {
        params: { path: { id: room.id } },
        body: {
          full_name: input.name,
          email: input.email,
          capability: input.capability,
          source: "ui",
        },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: participantsKey(room.id) });
      if (data) {
        setIssued(data);
      }
    },
  });
  const close = () => {
    setIssued(null);
    setName("");
    setEmail("");
    setCapability("comment");
    invite.reset();
    onClose();
  };
  return (
    <ConfirmModal
      open={open}
      onClose={close}
      title={
        issued
          ? t("access.issued.title", { name: issued.participant.full_name })
          : t("access.inviteTitle")
      }
      confirmLabel={issued ? t("access.done") : t("access.inviteConfirm")}
      confirmDisabled={!issued && (name.trim() === "" || email.trim() === "")}
      pending={invite.isPending}
      error={invite.isError ? problemMessageOf(invite.error, t) : null}
      onConfirm={() =>
        issued
          ? close()
          : invite.mutate({
              name: name.trim(),
              email: email.trim(),
              capability,
            })
      }
      placement="right"
    >
      {issued ? (
        <IssuedLink issued={issued} />
      ) : (
        <div className="form-stack">
          <Field label={t("access.nameLabel")} required>
            {(control) => (
              <TextInput
                {...control}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <Field label={t("access.emailLabel")} required>
            {(control) => (
              <TextInput
                {...control}
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            )}
          </Field>
          <ChoiceList
            legend={t("access.capabilityLegend")}
            value={capability}
            onChange={setCapability}
            choices={CAPABILITIES.map((c) => ({
              value: c,
              label: t(CAPABILITY_LABELS[c]),
              description: t(CAPABILITY_HINTS[c]),
            }))}
          />
          <p className="t-small">{t("access.inviteNote")}</p>
        </div>
      )}
    </ConfirmModal>
  );
}

function ReissueDialog({
  room,
  participant,
  open,
  onClose,
}: Readonly<{
  room: DealRoom;
  participant: Participant;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [issued, setIssued] = useState<Issued | null>(null);
  const reissue = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        "/deal-rooms/{id}/participants/{participantId}/resend",
        { params: { path: { id: room.id, participantId: participant.id } } },
      );
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: participantsKey(room.id) });
      if (data) {
        setIssued(data);
      }
    },
  });
  const close = () => {
    setIssued(null);
    reissue.reset();
    onClose();
  };
  return (
    <ConfirmModal
      open={open}
      onClose={close}
      title={t("access.issueLinkTitle", { name: participant.full_name })}
      confirmLabel={issued ? t("access.done") : t("access.issueLink")}
      pending={reissue.isPending}
      error={reissue.isError ? problemMessageOf(reissue.error, t) : null}
      onConfirm={() => (issued ? close() : reissue.mutate())}
    >
      {issued ? (
        <IssuedLink issued={issued} />
      ) : (
        <p>{t("access.issueLinkBody")}</p>
      )}
    </ConfirmModal>
  );
}

function RevokeDialog({
  room,
  participant,
  open,
  onClose,
}: Readonly<{
  room: DealRoom;
  participant: Participant;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const revoke = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST(
        "/deal-rooms/{id}/participants/{participantId}/revoke",
        {
          params: { path: { id: room.id, participantId: participant.id } },
        },
      );
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: participantsKey(room.id) });
      onClose();
    },
  });
  return (
    <ConfirmModal
      open={open}
      onClose={onClose}
      title={t("access.revokeTitle", { name: participant.full_name })}
      confirmLabel={t("access.revoke")}
      confirmVariant="danger"
      pending={revoke.isPending}
      error={revoke.isError ? problemMessageOf(revoke.error, t) : null}
      onConfirm={() => revoke.mutate()}
    >
      <p>
        {participant.email}
        {participant.last_seen_at
          ? ` · ${t("access.lastSeen", { when: formatDateAbbrev(participant.last_seen_at, locale, RECORD_ZONE) })}`
          : ` · ${t("access.neverSignedIn")}`}
      </p>
      <p className="t-small">{t("access.revokeBody")}</p>
    </ConfirmModal>
  );
}

function CapabilityDialog({
  room,
  participant,
  open,
  onClose,
}: Readonly<{
  room: DealRoom;
  participant: Participant;
  open: boolean;
  onClose: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [capability, setCapability] = useState<Capability>(
    participant.capability,
  );
  const change = useMutation({
    mutationFn: async (next: Capability) => {
      const { error } = await api.PATCH(
        "/deal-rooms/{id}/participants/{participantId}",
        {
          params: { path: { id: room.id, participantId: participant.id } },
          body: { capability: next },
        },
      );
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: participantsKey(room.id) });
      onClose();
    },
  });
  return (
    <ConfirmModal
      open={open}
      onClose={onClose}
      title={t("access.changeCapabilityTitle", { name: participant.full_name })}
      confirmLabel={t("access.save")}
      confirmDisabled={capability === participant.capability}
      pending={change.isPending}
      error={change.isError ? problemMessageOf(change.error, t) : null}
      onConfirm={() => change.mutate(capability)}
    >
      <ChoiceList
        legend={t("access.capabilityLegend")}
        value={capability}
        onChange={setCapability}
        choices={CAPABILITIES.map((c) => ({
          value: c,
          label: t(CAPABILITY_LABELS[c]),
          description: t(CAPABILITY_HINTS[c]),
        }))}
      />
    </ConfirmModal>
  );
}
