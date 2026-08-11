import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import { useCanUpsert, useCanWrite } from "../app/capability";
import { isOption } from "../app/options";
import { SectionHeader } from "../design-system/atoms";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryGate, throwProblem } from "./common";
import "./settings.css";

// The workspace's own consumer-mail list (CAP-PARAM-5). Mail from a consumer
// domain still creates the person; what it never creates is a company. The
// shipped baseline is a third-party dataset of some 8 700 domains, right far
// more often than a hand-typed list and still wrong sometimes in both
// directions — so this is where an operator adds what it missed and takes back
// what it wrongly claimed. Every role reads it, and every role may search the
// shipped baseline itself, so the capture posture stays legible to the people
// whose mail it governs. The write split mirrors the server's: any seat with
// capture_settings:create adds a consumer domain the baseline missed (`extra`),
// while `never` carve-outs and removal stay on capture_settings:update
// (admin/ops) — those controls disable rather than hide.

// The two things an entry can say, as ONE list: the type is derived from it and
// the control's options are built from it, so the offered choices, their labels
// and the runtime narrowing cannot drift apart (same shape as overlay.tsx's
// region list).
const KINDS = ["extra", "never"] as const;
type Kind = (typeof KINDS)[number];
const kindLabel: Record<Kind, MessageKey> = {
  extra: "consumerMail.kind.extra",
  never: "consumerMail.kind.never",
};

function useConsumerMailBaseline(q: string) {
  return useQuery({
    queryKey: ["consumer-mail-baseline", q],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/capture/consumer-mail-baseline",
        { params: { query: q ? { q } : {} } },
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
  });
}

function useConsumerMailDomains() {
  return useQuery({
    queryKey: ["consumer-mail-domains"],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/capture/consumer-mail-domains",
      );
      if (error || !response.ok) {
        throwProblem(error);
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
        throwProblem(error);
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
        throwProblem(error);
      }
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ["consumer-mail-domains"],
      });
    },
  });
}

// The shipped baseline, searchable in place: an operator deciding whether a
// domain needs an entry first sees what the shipped list already says about
// it. Results render only once a filter is typed — the first 50 of 8 700
// alphabetical rows answer no question anyone is asking.
function BaselineSection() {
  const t = useT();
  const [q, setQ] = useState("");
  const needle = q.trim();
  const query = useConsumerMailBaseline(needle);
  const result = query.data;
  return (
    <div
      style={{
        marginTop: "var(--space-3)",
        paddingTop: "var(--space-3)",
        borderTop: "1px solid var(--borderSubtle)",
      }}
    >
      <p className="t-label">{t("consumerMail.baselineTitle")}</p>
      {result && (
        <p style={{ color: "var(--text-muted)", fontSize: "var(--text-sm)" }}>
          {t("consumerMail.baselineCount", { total: result.total })}
        </p>
      )}
      <input
        aria-label={t("consumerMail.baselineSearchLabel")}
        data-testid="consumer-mail-baseline-search"
        placeholder={t("consumerMail.baselinePlaceholder")}
        value={q}
        onChange={(e) => setQ(e.target.value)}
        style={{ marginTop: "var(--space-2)" }}
      />
      {needle !== "" && result && result.matched === 0 && (
        <p style={{ color: "var(--text-muted)", fontSize: "var(--text-sm)" }}>
          {t("consumerMail.baselineNone")}
        </p>
      )}
      {needle !== "" && result && result.matched > 0 && (
        <>
          <ul
            data-testid="consumer-mail-baseline-list"
            style={{
              listStyle: "none",
              margin: "var(--space-2) 0 0",
              padding: 0,
              display: "flex",
              flexWrap: "wrap",
              gap: "var(--space-1) var(--space-2)",
            }}
          >
            {result.data.map((domain) => (
              <li key={domain} className="t-mono t-small">
                {domain}
              </li>
            ))}
          </ul>
          {result.matched > result.data.length && (
            <p
              style={{ color: "var(--text-muted)", fontSize: "var(--text-sm)" }}
            >
              {t("consumerMail.baselineMore", {
                shown: result.data.length,
                matched: result.matched,
              })}
            </p>
          )}
        </>
      )}
    </div>
  );
}

export function ConsumerMailDomainsCard() {
  const t = useT();
  // The write split mirrors the server's two demands. `canManage`
  // (capture_settings:update) covers what rewrites workspace posture: the
  // `never` carve-out, overwriting an entry, removal. `canAdd` mirrors the
  // server's upsert admission (create OR update) — a rep holding only create
  // may still contribute a new `extra` domain, and the server demands the
  // specific grant once it knows which half the write is.
  const canManage = useCanWrite("capture_settings", "update");
  const canAdd = useCanUpsert("capture_settings");
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
          if (!canAdd || domain.trim() === "") return;
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
          disabled={!canAdd}
          onChange={(e) => setDomain(e.target.value)}
        />
        {/* The kind stays on the update grant: `never` overrides the shipped
            baseline for the whole workspace, so a create-only seat submits the
            initial `extra` and never reaches the carve-out. */}
        <Select
          className="consumer-mail-kind"
          aria-label={t("consumerMail.kindLabel")}
          value={kind}
          disabled={!canManage}
          onChange={(value) => {
            if (isOption(value, KINDS)) {
              setKind(value);
            }
          }}
          options={KINDS.map((value) => ({
            value,
            label: t(kindLabel[value]),
          }))}
        />
        <button type="submit" disabled={!canAdd || add.isPending}>
          {t("consumerMail.add")}
        </button>
        {add.isError && (
          <span
            role="alert"
            style={{ color: "var(--danger)", fontSize: "var(--text-sm)" }}
          >
            {problemMessageOf(add.error, t)}
          </span>
        )}
      </form>
      {!canAdd && (
        <p style={{ color: "var(--text-muted)", fontSize: "var(--text-sm)" }}>
          {t("consumerMail.adminOnly")}
        </p>
      )}
      {canAdd && !canManage && (
        <p style={{ color: "var(--text-muted)", fontSize: "var(--text-sm)" }}>
          {t("consumerMail.addOnly")}
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
          {problemMessageOf(remove.error, t)}
        </span>
      )}
      <BaselineSection />
    </section>
  );
}
