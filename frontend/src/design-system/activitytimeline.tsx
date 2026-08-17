// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { emailSummaryText } from "../format/emailtext";
import { provenanceOf } from "../screens/common";
import {
  isTranscriptActivity,
  TranscriptReadCard,
} from "../screens/transcriptread";
import type { TimelineEntry } from "./composed";

type Activity = components["schemas"]["Activity"];

// The adapter that turns the contract's activities into the shell's timeline
// rows. It lives beside the shell that renders them rather than on the person
// screen, where five other screens had to import it from — a screen exporting
// a primitive is how the design system grows a second copy of one.

// TIMELINE_KINDS is the backend's activity vocabulary, and the adapter
// passes a kind straight through when the timeline can draw it. Collapsing
// everything but email and meeting into "note" told the reader a call was a
// note; anything genuinely outside the set still falls back to note rather
// than rendering no icon at all.
const TIMELINE_KINDS: readonly TimelineEntry["kind"][] = [
  "email",
  "meeting",
  "note",
  "call",
  "task",
  "message",
];

function timelineKind(kind: string): TimelineEntry["kind"] {
  const known = TIMELINE_KINDS.find((candidate) => candidate === kind);
  return known ?? "note";
}

// A timeline row is one line, so a body used as its title has its whitespace
// collapsed and is cut at this many characters.
const TIMELINE_TITLE_MAX = 140;

// timelineTitle is what the row says the activity WAS. A subject is the natural
// title, but a channel message has none — a chat carries text, not a subject
// line — so a subject-only title rendered the literal word "message" on every
// row and made the conversation invisible on the record it belongs to. The
// body is the title for anything that has no subject, which is also why the
// connector fills it for a wordless message ("photo", "voice"): capture's
// messageBody names the kind precisely so this row has something to show.
function timelineTitle(activity: Activity): string {
  const subject = activity.subject?.trim();
  if (subject) {
    return subject;
  }
  // Collapsed rather than trusted: a pasted multi-line message would otherwise
  // break the row's single-line layout. A mail is read for its message first:
  // titling the row from the raw body puts the From/To preamble, or a sign-off,
  // where the reason for the mail should be.
  const raw = activity.body ?? "";
  const body =
    activity.kind === "email"
      ? emailSummaryText(raw)
      : raw.replace(/\s+/g, " ").trim();
  if (!body) {
    return activity.kind;
  }
  return body.length > TIMELINE_TITLE_MAX
    ? `${body.slice(0, TIMELINE_TITLE_MAX - 1)}…`
    : body;
}

export function activityTimeline(
  // Optional because a 200 with no body is a shape the contract permits and
  // the mirror actually returns: `isSuccess` is true while `data.data` is
  // undefined, and a caller that trusted the flag crashed the record page it
  // was rendering. An absent list is an empty timeline, not an error.
  activities: Activity[] | undefined,
  // Who is reading, so a row this user logged reads as theirs and a
  // colleague's does not. Absent while the session is still resolving.
  viewerUserId?: string,
  renderActions?: (activity: Activity) => ReactNode,
): TimelineEntry[] {
  return (activities ?? []).map((activity) => ({
    id: activity.id,
    kind: timelineKind(activity.kind),
    title: timelineTitle(activity),
    // Carried beside the rendered title, which may be the body or the kind:
    // bulk grouping needs the message's OWN subject or it folds unrelated
    // subjectless rows together.
    subject: activity.subject,
    // The body is already in the composite read this row came from, so a
    // timeline of unreadable subject lines was a rendering choice, not a
    // limit of what the page knew.
    body: activity.body,
    direction: activity.direction,
    threadKey: activity.thread_key,
    bulkAttested: activity.bulk_mail_attested,
    atIso: activity.occurred_at,
    provenance: provenanceOf(activity.captured_by, viewerUserId),
    // Offered on the row rather than by each caller: a transcript is readable
    // wherever it is listed, and a per-screen opt-in is how the same affordance
    // ends up on the deal and missing on the person who was in the meeting.
    detail: isTranscriptActivity(activity) ? (
      <TranscriptReadCard activityId={activity.id} />
    ) : undefined,
    actions: renderActions?.(activity),
  }));
}
