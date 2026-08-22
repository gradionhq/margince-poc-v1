import { CalendarClock, Sparkles } from "lucide-react";
import { useState } from "react";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { formatDateTime } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import { PersonMeetingBrief } from "./persondrawers";
import "./dealmeeting.css";

// The deal's next booked meeting, with the brief one click away. The brief is
// the thing a rep opens in the ninety seconds before a room; burying it behind
// the person page, where it used to live alone, is how it went unopened. The
// meeting comes from the timeline the page already reads — no second query —
// and the drawer is the person page's own, so one brief renders everywhere.

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
    if (a.meeting_status && a.meeting_status !== "booked") {
      continue;
    }
    if (!best || new Date(a.occurred_at) < new Date(best.occurred_at)) {
      best = a;
    }
  }
  return best;
}

export function DealNextMeeting({
  activities,
}: Readonly<{ activities: readonly Activity[] }>) {
  const t = useT();
  const { locale } = useLocale();
  const [open, setOpen] = useState(false);
  const meeting = nextBookedMeeting(activities, new Date());
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
