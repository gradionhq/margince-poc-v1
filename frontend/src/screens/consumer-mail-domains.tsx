import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import { useCanWrite } from "../app/capability";
import { SectionHeader, Select } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";
import "./settings.css";

// The workspace's own consumer-mail list (CAP-PARAM-5). Mail from a consumer
// domain still creates the person; what it never creates is a company. The
// shipped baseline is a third-party dataset of some 8 700 domains, right far
// more often than a hand-typed list and still wrong sometimes in both
// directions — so this is where an operator adds what it missed and takes back
// what it wrongly claimed. Every role reads it; only admin/ops may change it,
// so the controls are disabled rather than hidden — a rep can see that the
// list exists and what is on it, which is what makes the capture posture
// legible to the people whose mail it governs.

type Kind = "extra" | "never";

function useConsumerMailDomains() {
  return useQuery({
    queryKey: ["consumer-mail-domains"],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/capture/consumer-mail-domains",
      );
      if (error || !response.ok) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
}

function useAddConsumerMailDomain() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (entry: { domain: string; kind: Kind }) => {
      const { data, error } = await api.POST("/capture/consumer-mail-domains", {
        body: entry,
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ["consumer-mail-domains"],
      });
    },
  });
}

function useRemoveConsumerMailDomain() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE(
        "/capture/consumer-mail-domains/{id}",
        { params: { path: { id } } },
      );
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ["consumer-mail-domains"],
      });
    },
  });
}

export function ConsumerMailDomainsCard() {
  const t = useT();
  // Both add and remove gate on capture_settings:update — there is no
  // mail-domain object, and removing an entry is an update to the workspace's
  // capture configuration rather than a delete of a record.
  const canManage = useCanWrite("capture_settings", "update");
  const query = useConsumerMailDomains();
  const add = useAddConsumerMailDomain();
  const remove = useRemoveConsumerMailDomain();
  const [domain, setDomain] = useState("");
  const [kind, setKind] = useState<Kind>("extra");

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader
        title={t("consumerMail.title")}
        sub={t("consumerMail.sub")}
      />
      <form
        style={{
          display: "flex",
          flexWrap: "wrap",
          gap: "var(--space-2)",
          alignItems: "center",
          marginBottom: "var(--space-3)",
        }}
        onSubmit={(e) => {
          e.preventDefault();
          if (!canManage || domain.trim() === "") return;
          add.mutate(
            { domain: domain.trim(), kind },
            { onSuccess: () => setDomain("") },
          );
        }}
      >
        <input
          aria-label={t("consumerMail.domainLabel")}
          data-testid="consumer-mail-domain-input"
          placeholder={t("consumerMail.domainPlaceholder")}
          value={domain}
          disabled={!canManage}
          onChange={(e) => setDomain(e.target.value)}
        />
        <Select
          className="consumer-mail-kind"
          aria-label={t("consumerMail.kindLabel")}
          data-testid="consumer-mail-kind-select"
          value={kind}
          disabled={!canManage}
          onChange={(e) => setKind(e.target.value as Kind)}
        >
          <option value="extra">{t("consumerMail.kind.extra")}</option>
          <option value="never">{t("consumerMail.kind.never")}</option>
        </Select>
        <button type="submit" disabled={!canManage || add.isPending}>
          {t("consumerMail.add")}
        </button>
        {add.isError && (
          <span
            role="alert"
            style={{ color: "var(--danger)", fontSize: "var(--text-sm)" }}
          >
            {add.error.message}
          </span>
        )}
      </form>
      {!canManage && (
        <p style={{ color: "var(--text-muted)", fontSize: "var(--text-sm)" }}>
          {t("consumerMail.adminOnly")}
        </p>
      )}
      <QueryGate query={query}>
        {(entries) =>
          entries.length === 0 ? (
            <p
              style={{ color: "var(--text-muted)", fontSize: "var(--text-sm)" }}
            >
              {t("consumerMail.none")}
            </p>
          ) : (
            <ul
              data-testid="consumer-mail-domain-list"
              style={{ listStyle: "none", margin: 0, padding: 0 }}
            >
              {entries.map((entry) => (
                <li
                  key={entry.id}
                  data-kind={entry.kind}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: "var(--space-2)",
                    padding: "var(--space-2) 0",
                  }}
                >
                  <Mail aria-hidden size={16} />
                  <span style={{ flex: 1 }}>{entry.domain}</span>
                  <span
                    style={{
                      color: "var(--text-muted)",
                      fontSize: "var(--text-sm)",
                    }}
                  >
                    {entry.kind === "never"
                      ? t("consumerMail.kind.never")
                      : t("consumerMail.kind.extra")}
                  </span>
                  <button
                    type="button"
                    aria-label={t("consumerMail.remove")}
                    disabled={!canManage || remove.isPending}
                    onClick={() => remove.mutate(entry.id)}
                  >
                    <Trash2 aria-hidden size={16} />
                  </button>
                </li>
              ))}
            </ul>
          )
        }
      </QueryGate>
      {remove.isError && (
        <span
          role="alert"
          style={{ color: "var(--danger)", fontSize: "var(--text-sm)" }}
        >
          {remove.error.message}
        </span>
      )}
    </section>
  );
}
