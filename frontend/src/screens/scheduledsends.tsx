// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import {
  Badge,
  Button,
  Card,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { SurfaceState } from "../design-system/surfacestate";
import { localDateTimeValue } from "../format/calendarday";
import { formatDateTime } from "../format/format";
import { viewerZone } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  isVersionSkewOf,
  logUnexpectedError,
  problemMessageOf,
  QueryStates,
  throwProblem,
} from "./common";
import { scheduleFields } from "./compose";

// The queue behind "send later", and the reason it has to exist: a rep who can
// schedule a message must be able to reach it again. Without this page the
// endpoints, the store and the fire path all existed and nothing in the client
// called them, so "send later" was a one-way door — the message a rep scheduled
// by mistake went out.
//
// It is the SENDER's own list, not the workspace's (ADR-0104/A155): an unsent
// body and its blind-copy list are not workspace-readable the way a sent
// activity is, and the server lists only what the caller scheduled. So there is
// no owner column here and no sharing affordance — the page is one person's.

type ScheduledSend = components["schemas"]["ScheduledSend"];
type Status = ScheduledSend["status"];

/** The route this screen answers on, spelled where the screen lives. */
export const SCHEDULED_SCREEN = "scheduled" as const;

/**
 * The three groups a rep reads this page in, which are NOT the five wire
 * statuses.
 *
 * `held` is first because it is the only group that is waiting on a person: a
 * gate refused at fire, or the moment passed while nothing was running, and the
 * message will not send itself. `waiting` is the queue proper. `closed` is
 * everything that is no longer going to change on its own — released, sent,
 * cancelled — kept on the page because a rep who cancelled a message wants to
 * see that it is cancelled rather than that it is gone.
 */
const GROUPS = ["held", "waiting", "closed"] as const;
type Group = (typeof GROUPS)[number];

function groupOf(status: Status): Group {
  if (status === "held") return "held";
  if (status === "scheduled") return "waiting";
  return "closed";
}

const GROUP_TITLE: Record<Group, MessageKey> = {
  held: "sched.group.held",
  waiting: "sched.group.waiting",
  closed: "sched.group.closed",
};

// Each group says what there is none OF in its own words, because "there is
// none" is the one state that has to be specific: a shared sentence under three
// headings tells the reader the same thing three times and none of it about the
// heading it sits under.
//
// A rep with nothing scheduled at all never reads any of them — the page prints
// ONE empty state instead of three, which is the difference between an inbox
// that is clear and a page that looks broken.
const GROUP_EMPTY: Record<Group, MessageKey> = {
  held: "sched.group.heldEmpty",
  waiting: "sched.group.waitingEmpty",
  closed: "sched.group.closedEmpty",
};

const STATUS_LABEL: Record<Status, MessageKey> = {
  scheduled: "sched.status.scheduled",
  released: "sched.status.released",
  sent: "sched.status.sent",
  cancelled: "sched.status.cancelled",
  held: "sched.status.held",
};

// Why a human has to look at it, in the contract's own closed set. Rendered as
// words because `held_reason` is a wire token and a rep cannot act on
// `timer_exhausted`.
const HELD_REASON_LABEL: Record<string, MessageKey> = {
  consent_withdrawn: "sched.held.consentWithdrawn",
  sender_inactive: "sched.held.senderInactive",
  missed_window: "sched.held.missedWindow",
  timer_exhausted: "sched.held.timerExhausted",
  send_refused: "sched.held.sendRefused",
};

// Which statuses the server will still let a rep move or withdraw. It mirrors
// the store's own `status IN ('scheduled','held')` predicate, and it is spelled
// here so a row cannot offer a verb the write is about to refuse.
const STILL_MOVABLE: ReadonlySet<Status> = new Set<Status>([
  "scheduled",
  "held",
]);

/**
 * The zone a send's moment is rendered in.
 *
 * The wire carries the IANA zone the human picked the moment in, kept so it
 * re-renders as meant. `formatDateTime` refuses anything that is not an IANA
 * name — a fixed offset included, because an offset freezes the DST rules of the
 * day it was picked — and it does so by throwing, which on this page would take
 * the whole list down over one row.
 *
 * So a zone this runtime cannot resolve falls back to the reader's own and is
 * logged. That is a claim about the SERVER, not about the reader: a conforming
 * one cannot produce it, and swallowing it silently is how a fork's malformed
 * zone would go unnoticed while every moment on the page quietly shifted.
 */
function renderZone(wireZone: string, readerZone: string): string {
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: wireZone }).format();
    return wireZone;
  } catch (error) {
    logUnexpectedError(error);
    return readerZone;
  }
}

/**
 * One row's moment, in the zone it was chosen in, naming that zone when it is
 * not the reader's own.
 *
 * A rep who scheduled 09:00 from Berlin and reads the page from Hanoi must not
 * be told the message goes at 14:00 with no explanation, and must not be told
 * 09:00 with no zone either — the first reads as a bug, the second as a message
 * that will land at the wrong end of the recipient's morning.
 */
function Moment({
  send,
  readerZone,
}: Readonly<{ send: ScheduledSend; readerZone: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const zone = renderZone(send.scheduled_tz, readerZone);
  return (
    <span className="t-caption">
      {formatDateTime(send.scheduled_at, locale, zone)}
      {zone !== readerZone && ` · ${t("sched.inZone", { zone })}`}
    </span>
  );
}

/** The To line, with the rest counted rather than listed. */
function recipientLine(
  send: ScheduledSend,
  t: ReturnType<typeof useT>,
): string {
  const [first, ...rest] = send.to;
  if (!first) {
    // Required on the wire and non-empty in every path that writes it. Stated
    // rather than rendered as an empty cell, which reads as a message addressed
    // to nobody.
    return t("sched.recipientsUnknown");
  }
  return rest.length === 0
    ? first
    : t("sched.recipientsMore", { first, count: rest.length });
}

// The whole write, row identity included. The `mutationFn` takes it as a
// VARIABLE and never closes over the render's own state: the click belongs to
// the committed render, so what it passes cannot be older than the control the
// reader pressed, while what it closes over can be — and here the stale value
// would be the `If-Match` version, so the reader would be told somebody else
// moved the message when nobody did.
type Move = Readonly<{ id: string; version: number; at: string }>;
type Withdraw = Readonly<{ id: string }>;

/**
 * The reschedule control: a moment picker that unfolds in place.
 *
 * Time only, which is the contract's own rule — the content is what the
 * approval bound to, so changing it is cancel-and-recompose. The picker is a
 * `datetime-local`, seeded from the send's current moment in the READER's zone
 * and read back in that same zone by `scheduleFields`; the composer's own
 * send-later control is the same pairing, and it is imported rather than
 * respelled so the two cannot come to disagree about what a picked moment means.
 */
function MoveControl({
  send,
  pending,
  onMove,
}: Readonly<{
  send: ScheduledSend;
  pending: boolean;
  onMove: (move: Move) => void;
}>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");

  if (!open) {
    return (
      <Button
        small
        onClick={() => {
          setDraft(localDateTimeValue(send.scheduled_at));
          setOpen(true);
        }}
      >
        {t("sched.move")}
      </Button>
    );
  }
  // Empty or unparseable yields neither field, and the wire requires both. The
  // button refuses rather than sending half a schedule the server would 422.
  const fields = scheduleFields(draft);
  return (
    <>
      <TextInput
        type="datetime-local"
        aria-label={t("sched.moveTo", { subject: send.subject })}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        style={{ maxWidth: 220 }}
      />
      <Button
        small
        variant="primary"
        disabled={fields.scheduled_at === undefined}
        pending={pending}
        onClick={() => {
          if (fields.scheduled_at === undefined) return;
          onMove({
            id: send.id,
            version: send.version,
            at: fields.scheduled_at,
          });
          setOpen(false);
        }}
      >
        {t("sched.moveSave")}
      </Button>
      <Button small onClick={() => setOpen(false)}>
        {t("sched.moveCancel")}
      </Button>
    </>
  );
}

function SendRow({
  send,
  readerZone,
  movePending,
  onMove,
  onWithdraw,
}: Readonly<{
  send: ScheduledSend;
  readerZone: string;
  movePending: boolean;
  onMove: (move: Move) => void;
  onWithdraw: (send: ScheduledSend) => void;
}>) {
  const t = useT();
  const actionable = STILL_MOVABLE.has(send.status);
  const heldReasonKey = send.held_reason
    ? HELD_REASON_LABEL[send.held_reason]
    : undefined;
  return (
    <Card as="div" style={{ marginBottom: "var(--space-2)" }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: "var(--space-2)",
          flexWrap: "wrap",
        }}
      >
        <span style={{ flex: 1, minWidth: "12rem" }}>
          <strong>{send.subject}</strong>
          <span className="t-caption"> · {recipientLine(send, t)}</span>
          <br />
          <Moment send={send} readerZone={readerZone} />
        </span>
        {send.status === "held" ? (
          <Badge tone="warn">{t(STATUS_LABEL[send.status])}</Badge>
        ) : (
          <Badge quiet>{t(STATUS_LABEL[send.status])}</Badge>
        )}
        {actionable && (
          <MoveControl send={send} pending={movePending} onMove={onMove} />
        )}
        {actionable && (
          <Button small variant="danger" onClick={() => onWithdraw(send)}>
            {t("sched.withdraw")}
          </Button>
        )}
      </div>
      {/* A held message says WHY on the row rather than in a tooltip: it is the
          only thing that tells the rep whether to move it or to give up on it,
          and a reason a rep has to hover for is a reason they do not read. An
          unmapped token prints nothing rather than the token — a reason nobody
          can act on is worse than the sentence above it standing alone. */}
      {heldReasonKey && (
        <p className="t-caption" style={{ marginTop: "var(--space-1)" }}>
          {t(heldReasonKey)}
        </p>
      )}
    </Card>
  );
}

/**
 * The caller's own queue, read once.
 *
 * No status filter on the request: the three groups this page reads in ARE the
 * reading, and one unfiltered read fills all of them — asking per group would be
 * three requests to answer one question.
 *
 * Exported because the Tasks page carries this queue's front door and needs the
 * same answer to decide whether to offer it. One cache entry, one request, both
 * consumers — the shape `useRoster` uses for the same reason.
 */
export function useScheduledSends() {
  const t = useT();
  return useQuery({
    queryKey: ["scheduled-sends"],
    queryFn: async () => {
      const { data, error } = await api.GET("/scheduled-sends", {});
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    staleTime: 60_000,
  });
}

/** How many of the caller's messages are still waiting on something. */
export function waitingCount(sends: readonly ScheduledSend[]): number {
  return sends.filter((send) => STILL_MOVABLE.has(send.status)).length;
}

export function ScheduledSendsScreen() {
  const t = useT();
  const queryClient = useQueryClient();
  // A moment a rep chose is a personal one, so the page reads in the viewer's
  // own zone unless the send names another (see Moment).
  const readerZone = viewerZone();
  const [withdrawing, setWithdrawing] = useState<ScheduledSend | null>(null);

  const query = useScheduledSends();

  const move = useMutation({
    mutationFn: async ({ id, version, at }: Move) => {
      const { error } = await api.PATCH("/scheduled-sends/{id}", {
        // Through `ifMatch`, not a hand-written header: it is the one spelling of
        // the precondition, and `src/api/if-match-coverage.test.ts` reads the
        // AST for that call rather than for the header's text, so a second
        // spelling reads to the gate as no precondition at all.
        params: { path: { id }, ...ifMatch(version) },
        // The zone travels as a NAME beside the instant, which is what makes a
        // message scheduled across a DST boundary arrive at the wall time the
        // rep meant rather than an hour either side of it.
        body: { scheduled_at: at, scheduled_tz: readerZone },
      });
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ["scheduled-sends"] }),
  });

  const withdraw = useMutation({
    mutationFn: async ({ id }: Withdraw) => {
      const { error } = await api.POST("/scheduled-sends/{id}/cancel", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error, t);
      }
    },
    onSuccess: () => {
      setWithdrawing(null);
      queryClient.invalidateQueries({ queryKey: ["scheduled-sends"] });
    },
  });

  // A 409 on either write says one thing: the row on screen is not the row on
  // the server. It already fired, somebody withdrew it, or another surface moved
  // it first — and all three want the same next step, which is to read the list
  // again rather than to retry a write against a version that is gone. It is
  // told apart from every other failure because "reload and look" is useless
  // advice for a 500 and the only useful advice here.
  const skew =
    isVersionSkewOf(move.error) || isVersionSkewOf(withdraw.error)
      ? t("sched.skew")
      : null;
  const writeError =
    skew === null
      ? ((move.isError ? problemMessageOf(move.error, t) : null) ??
        (withdraw.isError ? problemMessageOf(withdraw.error, t) : null))
      : null;

  return (
    // No heading of its own: the shell's PageTitle names this page from
    // OFF_RAIL_TITLE_KEYS and prints its subtitle from PAGE_SUB_KEYS, and a
    // second h1 here would leave a screen reader picking between two page
    // titles.
    <div className="wrap">
      {skew && (
        <Callout
          tone="warn"
          live="status"
          actions={
            <Button small onClick={() => void query.refetch()}>
              {t("sched.reload")}
            </Button>
          }
        >
          {skew}
        </Callout>
      )}
      {writeError && <Callout tone="danger">{writeError}</Callout>}
      <QueryStates query={query}>
        {query.data && query.data.length === 0 ? (
          // One sentence for the whole page, not one per group: a rep who has
          // never scheduled anything is not reading three findings, and three
          // empty blocks make a page that is simply clear look broken.
          <SurfaceState state="empty" emptyLabel={t("sched.empty")}>
            {null}
          </SurfaceState>
        ) : (
          query.data && (
            <div className="arrive-stack">
              {GROUPS.map((group) => {
                const rows = query.data.filter(
                  (send) => groupOf(send.status) === group,
                );
                const title = t(GROUP_TITLE[group]);
                return (
                  <section key={group} aria-label={title}>
                    <SectionHeader title={title} />
                    <SurfaceState
                      label={title}
                      state={rows.length === 0 ? "empty" : "ready"}
                      emptyLabel={t(GROUP_EMPTY[group])}
                    >
                      {rows.map((send) => (
                        <SendRow
                          key={send.id}
                          send={send}
                          readerZone={readerZone}
                          movePending={move.isPending}
                          onMove={(input) => move.mutate(input)}
                          onWithdraw={setWithdrawing}
                        />
                      ))}
                    </SurfaceState>
                  </section>
                );
              })}
            </div>
          )
        )}
      </QueryStates>
      {/* Withdrawing is confirmed and moving is not, and the difference is what
          each one costs to undo: a moved message can be moved back, while a
          withdrawn one is gone and has to be written again from nothing — the
          approval it carried does not survive it. */}
      <ConfirmModal
        open={withdrawing !== null}
        onClose={() => setWithdrawing(null)}
        title={t("sched.withdrawTitle")}
        confirmLabel={t("sched.withdrawConfirm")}
        confirmVariant="danger"
        pending={withdraw.isPending}
        error={withdraw.isError ? problemMessageOf(withdraw.error, t) : null}
        onConfirm={() => {
          if (withdrawing) {
            withdraw.mutate({ id: withdrawing.id });
          }
        }}
      >
        <p>
          {t("sched.withdrawBody", { subject: withdrawing?.subject ?? "" })}
        </p>
      </ConfirmModal>
    </div>
  );
}
