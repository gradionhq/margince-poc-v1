import { useMutation, useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import {
  Button,
  SectionHeader,
  SegmentedControl,
  TextInput,
} from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// Booking page shell (B-EP09.14): rail-less (a test asserts no rail),
// duration toggle, live availability, recognized-contact welcome via the
// search seam, and HONEST degradation when the booking backend is
// unavailable — the page never fabricates a confirmation. The public
// (anonymous) variant and consent passthrough ride the B-E04.16 public
// backend (filed in feedback/14); this shell is the session-authed page.

const DURATIONS = ["15", "30", "60"] as const;

export function BookingScreen() {
  const t = useT();
  const { locale } = useLocale();
  const [duration, setDuration] = useState<(typeof DURATIONS)[number]>("30");
  const [attendee, setAttendee] = useState("");
  const [recognized, setRecognized] = useState<string | null>(null);
  const attendeeId = useId();

  const from = new Date();
  const to = new Date(from.getTime() + 7 * 86_400_000);

  const availability = useQuery({
    queryKey: ["availability", duration],
    queryFn: async () => {
      const { data, error } = await api.GET("/availability", {
        params: {
          query: {
            from: from.toISOString(),
            to: to.toISOString(),
            duration_minutes: Number(duration),
          },
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const checkAttendee = useMutation({
    mutationFn: async (query: string) => {
      const { data, error } = await api.GET("/search", {
        params: { query: { q: query, limit: 3 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data.find((hit) => hit.type === "person") ?? null;
    },
    onSuccess: (hit) => setRecognized(hit?.title ?? null),
  });

  const book = useMutation({
    mutationFn: async (slot: { start: string; end: string }) => {
      const { data, error } = await api.POST("/bookings", {
        body: {
          start: slot.start,
          end: slot.end,
          subject: t("book.subject"),
          attendee_emails: attendee.trim() ? [attendee.trim()] : [],
          links: [],
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  return (
    <div className="wrap narrow">
      <SectionHeader title={t("book.title")} sub={t("book.sub")} />
      <div
        style={{
          display: "flex",
          gap: 10,
          alignItems: "center",
          marginBottom: 12,
        }}
      >
        <SegmentedControl
          options={DURATIONS}
          value={duration}
          onChange={setDuration}
          labels={{
            "15": t("book.min15"),
            "30": t("book.min30"),
            "60": t("book.min60"),
          }}
        />
        <span className="t-label" id={attendeeId}>
          {t("book.attendee")}
        </span>
        <TextInput
          aria-labelledby={attendeeId}
          value={attendee}
          onChange={(event) => setAttendee(event.target.value)}
          onBlur={() =>
            attendee.trim() && checkAttendee.mutate(attendee.trim())
          }
        />
      </div>
      {recognized && (
        <p className="t-caption" style={{ marginBottom: 10 }}>
          {t("book.welcomeBack", { name: recognized })}
        </p>
      )}
      {book.isSuccess ? (
        <div className="card">
          <p className="t-label">{t("book.confirmed")}</p>
          <p className="t-caption" style={{ marginTop: 4 }}>
            {book.data.occurred_at &&
              formatDateTime(book.data.occurred_at, locale, "Europe/Berlin")}
          </p>
        </div>
      ) : (
        <QueryGate
          query={availability}
          empty={(data) => data.slots.length === 0}
        >
          {(data) => (
            <div style={{ display: "flex", flexWrap: "wrap", gap: 8 }}>
              {data.slots.slice(0, 12).map((slot) => (
                <Button
                  key={slot.start}
                  small
                  disabled={book.isPending}
                  onClick={() => book.mutate(slot)}
                >
                  {formatDateTime(slot.start, locale, "Europe/Berlin")}
                </Button>
              ))}
            </div>
          )}
        </QueryGate>
      )}
      {book.isError && (
        <div className="card card-inset" style={{ marginTop: 12 }}>
          <p className="t-label">{t("book.failed")}</p>
          <p className="t-caption" style={{ marginTop: 4 }}>
            {book.error instanceof Error ? book.error.message : null}
          </p>
        </div>
      )}
    </div>
  );
}
