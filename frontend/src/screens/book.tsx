import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
// duration toggle, live availability, and HONEST degradation when the
// booking backend is unavailable — the page never fabricates a
// confirmation. Two variants share the shell: the session-authed page
// (#/book) and the anonymous public page (#/book/<host_slug>) over the
// B-E04.16 /public/booking surface, where consent is mandatory and the
// wording the visitor sees is byte-for-byte what is submitted (the
// consent-passthrough invariant, EP07 capture contract).

const DURATIONS = ["15", "30", "60"] as const;

// The public page's consent policy. The contract exposes no anonymous
// purpose read, so the published host page embeds its purpose + policy
// version at publish time — these are that embedded config's dev-default
// stand-ins, passed through verbatim, never recomputed at submit.
export const PUBLIC_BOOKING_CONSENT = {
  purpose_id: "00000000-0000-4000-8000-00000000b001",
  policy_version: "2026-07",
} as const;

export function BookingScreen({ hostSlug }: Readonly<{ hostSlug?: string }>) {
  if (hostSlug) {
    return <PublicBookingScreen hostSlug={hostSlug} />;
  }
  return <SessionBookingScreen />;
}

function useBookingWindow() {
  const from = new Date();
  const to = new Date(from.getTime() + 7 * 86_400_000);
  return { from, to };
}

function SessionBookingScreen() {
  const t = useT();
  const { locale } = useLocale();
  const [duration, setDuration] = useState<(typeof DURATIONS)[number]>("30");
  const [attendee, setAttendee] = useState("");
  const [recognized, setRecognized] = useState<string | null>(null);
  const attendeeId = useId();
  const { from, to } = useBookingWindow();

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

function PublicBookingScreen({ hostSlug }: Readonly<{ hostSlug: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const [duration, setDuration] = useState<(typeof DURATIONS)[number]>("30");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [consented, setConsented] = useState(false);
  const fieldId = useId();
  const { from, to } = useBookingWindow();

  const availability = useQuery({
    queryKey: ["public-availability", hostSlug, duration],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/public/booking/{host_slug}/availability",
        {
          params: {
            path: { host_slug: hostSlug },
            query: {
              from: from.toISOString(),
              to: to.toISOString(),
              duration_minutes: Number(duration),
            },
          },
        },
      );
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  // The wording shown at the checkbox IS the wording submitted — one
  // variable, no re-derivation (the consent-passthrough test pins it).
  const consentWording = t("book.consentWording");

  const book = useMutation({
    mutationFn: async (slot: { start: string; end: string }) => {
      const { data, error } = await api.POST("/public/booking/{host_slug}", {
        params: { path: { host_slug: hostSlug } },
        body: {
          start: slot.start,
          end: slot.end,
          subject: t("book.subject"),
          booker: { name: name.trim(), email: email.trim() },
          consent: { ...PUBLIC_BOOKING_CONSENT, wording: consentWording },
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    // a 409 slot_taken means the world changed — re-ask for availability
    onError: () => {
      queryClient.invalidateQueries({
        queryKey: ["public-availability", hostSlug],
      });
    },
  });

  const ready = consented && name.trim().length > 0 && email.trim().length > 0;

  return (
    <div className="wrap narrow">
      <SectionHeader title={t("book.title")} sub={t("book.publicSub")} />
      <div
        style={{
          display: "flex",
          gap: 10,
          alignItems: "center",
          flexWrap: "wrap",
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
        <span className="t-label" id={`${fieldId}-name`}>
          {t("book.name")}
        </span>
        <TextInput
          aria-labelledby={`${fieldId}-name`}
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
        <span className="t-label" id={`${fieldId}-email`}>
          {t("book.email")}
        </span>
        <TextInput
          type="email"
          aria-labelledby={`${fieldId}-email`}
          value={email}
          onChange={(event) => setEmail(event.target.value)}
        />
      </div>
      <label
        className="t-caption"
        style={{
          display: "flex",
          gap: 8,
          alignItems: "flex-start",
          marginBottom: 12,
        }}
      >
        <input
          type="checkbox"
          checked={consented}
          onChange={(event) => setConsented(event.target.checked)}
        />
        <span data-consent-wording>{consentWording}</span>
      </label>
      {book.isSuccess ? (
        <div className="card">
          <p className="t-label">{t("book.confirmed")}</p>
          <p className="t-caption" style={{ marginTop: 4 }}>
            {formatDateTime(book.data.start, locale, "Europe/Berlin")}
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
                  disabled={!ready || book.isPending}
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
