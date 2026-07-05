import { useMutation } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import { useId, useState } from "react";
import { api } from "../api/client";
import {
  Badge,
  Button,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";

// Client surfaces (B-EP09.13a): the rail-less extension chrome — the fixed
// dark "Back to Margince" bar, a sender lookup that renders a mini-360 for a
// recognized contact and the HONEST unknown-sender state otherwise, and the
// load-bearing isolation footer: this surface talks only to the user's OWN
// workspace API (S-E12.5) — no third-party egress.

export function ClientSurfaceScreen() {
  const t = useT();
  const [email, setEmail] = useState("");
  const emailId = useId();

  const lookup = useMutation({
    mutationFn: async (query: string) => {
      const { data, error } = await api.GET("/search", {
        params: { query: { q: query, limit: 5 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data.filter((hit) => hit.type === "person");
    },
  });

  return (
    <div className="client-chrome">
      <header className="client-bar">
        <a href="#/home" className="client-back">
          <ArrowLeft aria-hidden size={15} />
          {t("client.back")}
        </a>
      </header>
      <div className="wrap narrow">
        <SectionHeader title={t("client.title")} sub={t("client.sub")} />
        <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
          <span className="t-label" id={emailId}>
            {t("client.sender")}
          </span>
          <TextInput
            aria-labelledby={emailId}
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            style={{ flex: 1 }}
          />
          <Button
            variant="primary"
            small
            disabled={email.trim() === "" || lookup.isPending}
            onClick={() => lookup.mutate(email.trim())}
          >
            {t("client.lookup")}
          </Button>
        </div>

        {lookup.isSuccess && lookup.data.length > 0 && (
          <section className="card" style={{ marginTop: 14 }}>
            {lookup.data.map((hit) => (
              <div
                key={hit.id}
                style={{ display: "flex", gap: 8, alignItems: "center" }}
              >
                <strong>{hit.title}</strong>
                {hit.snippet && (
                  <span className="t-caption">{hit.snippet}</span>
                )}
                <a className="t-caption" href={`#/contacts/${hit.id}`}>
                  {t("client.open360")}
                </a>
              </div>
            ))}
          </section>
        )}

        {lookup.isSuccess && lookup.data.length === 0 && (
          <section className="card card-inset" style={{ marginTop: 14 }}>
            <p className="t-label">{t("client.unknown")}</p>
            <p className="t-caption" style={{ marginTop: 4 }}>
              {t("client.unknownDetail")}
            </p>
            <a
              className="btn btn-ghost btn-sm"
              style={{ marginTop: 10 }}
              href="#/leads"
            >
              {t("client.createLead")}
            </a>
          </section>
        )}

        {lookup.isError && (
          <p
            className="t-caption"
            style={{ color: "var(--danger)", marginTop: 10 }}
          >
            {lookup.error instanceof Error ? lookup.error.message : null}
          </p>
        )}

        <footer className="client-footer">
          <Badge>{t("client.isolation")}</Badge>
          <span className="t-small">{t("client.attribution")}</span>
        </footer>
      </div>
    </div>
  );
}
