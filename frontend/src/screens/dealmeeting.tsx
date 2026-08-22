import { useQuery } from "@tanstack/react-query";
import { CalendarClock, Sparkles } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { formatDateTime } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import { throwProblem } from "./common";
import { PersonMeetingBrief } from "./persondrawers";
import "./dealmeeting.css";

// The deal's next booked meeting, with the brief one click away. The brief is
// the thing a rep opens in the ninety seconds before a room; burying it behind
// the person page, where it used to live alone, is how it went unopened. The
// card reads the deal's meetings itself: the page's timeline is filtered and
// paged by the reader, and a card that went blank because they searched for
// an email would read as "no meeting". The drawer is the person page's own,
// so one brief renders everywhere.

type Activity = components["schemas"]["Activity"];

/** The nearest booked meeting still ahead, or null. */
export function nextBookedMeeting(
  activities: readonly Activity[],
  now: Date,
): Activity | null {
  let best: Activity | null = null;
  for (const a of activities) {
    if (a.kind !== "meeting" || new Date(a.occurred_at) <= now) {
      continue;
    }
    // A meeting the reader may know of but not open has no brief to offer;
    // the button would 404.
    if (a.content_state === "withheld") {
      continue;
    }
    if (a.meeting_status && a.meeting_status !== "booked") {
      continue;
    }
    if (!best || new Date(a.occurred_at) < new Date(best.occurred_at)) {
      best = a;
    }
  }
  return best;
}

// The timeline is ordered newest first, so the window holds the furthest
// scheduled rows first; this many is beyond any deal's booked horizon.
const MEETING_WINDOW = 50;

export function DealNextMeeting({ dealId }: Readonly<{ dealId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const [open, setOpen] = useState(false);
  const meetings = useQuery({
    queryKey: ["deal-meetings", dealId],
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: {
          query: {
            entity_type: "deal",
            entity_id: dealId,
            kind: "meeting",
            limit: MEETING_WINDOW,
          },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const meeting = nextBookedMeeting(meetings.data?.data ?? [], new Date());
  if (!meeting) {
    return null;
  }
  return (
    <Panel title={t("dealmeeting.title")}>
      <PanelBody>
        <p className="dealmeeting-when">
          <CalendarClock aria-hidden />
          {formatDateTime(meeting.occurred_at, locale, RECORD_ZONE)}
        </p>
        <p>{meeting.subject || t("dealmeeting.untitled")}</p>
        <div className="card-actions">
          <Button small onClick={() => setOpen(true)}>
            <Sparkles aria-hidden />
            {t("dealmeeting.openBrief")}
          </Button>
        </div>
      </PanelBody>
      <PersonMeetingBrief
        activityId={meeting.id}
        open={open}
        onClose={() => setOpen(false)}
      />
    </Panel>
  );
}
