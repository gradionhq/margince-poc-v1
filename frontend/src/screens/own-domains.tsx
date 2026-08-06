import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Building2, Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import { useCanWrite } from "../app/capability";
import { Button, SectionHeader, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// The workspace own-domain card (CAP-WIRE-2a, ADR-0082/A127): which domains
// this installation treats as its own, and therefore whose mail it does not
// store. Every role reads it — a rep should be able to see why a thread is
// missing — and only admin/ops may change it, so the controls are disabled
// rather than hidden, like the capture-settings card beside it.

function useOwnDomains() {
  return useQuery({
    queryKey: ["workspace-email-domains"],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/workspace/email-domains",
      );
      if (error || !response.ok) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

function useAddOwnDomain() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (domain: string) => {
      const { data, error } = await api.POST("/workspace/email-domains", {
        body: { domain },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspace-email-domains"] });
    },
  });
}

function useRemoveOwnDomain() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (domain: string) => {
      const { error } = await api.DELETE("/workspace/email-domains/{domain}", {
        params: { path: { domain } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspace-email-domains"] });
    },
  });
}

export function OwnDomainsCard() {
  const t = useT();
  const canManage = useCanWrite("capture_settings", "update");
  const query = useOwnDomains();
  const add = useAddOwnDomain();
  const remove = useRemoveOwnDomain();
  const [draft, setDraft] = useState("");

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader title={t("ownDomains.title")} sub={t("ownDomains.sub")} />
      <QueryGate query={query}>
        {(list) => (
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              gap: "var(--space-3)",
            }}
          >
            <p
              style={{
                color: "var(--text-muted)",
                fontSize: "var(--text-sm)",
                margin: 0,
              }}
            >
              {t("ownDomains.irreversible")}
            </p>

            {(list.anchor_domains ?? []).length > 0 && (
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  gap: "var(--space-1)",
                }}
              >
                <span style={{ fontSize: "var(--text-sm)" }}>
                  <Building2 aria-hidden size={14} />{" "}
                  {t("ownDomains.fromCompany")}
                </span>
                <ul
                  data-testid="own-domains-from-company"
                  style={{ margin: 0, paddingLeft: "var(--space-4)" }}
                >
                  {(list.anchor_domains ?? []).map((domain) => (
                    <li key={domain} style={{ fontSize: "var(--text-sm)" }}>
                      {domain}
                    </li>
                  ))}
                </ul>
              </div>
            )}

            {list.data.length === 0 ? (
              <p
                data-testid="own-domains-empty"
                style={{
                  color: "var(--text-muted)",
                  fontSize: "var(--text-sm)",
                  margin: 0,
                }}
              >
                {t("ownDomains.empty")}
              </p>
            ) : (
              <ul
                data-testid="own-domains-list"
                style={{ listStyle: "none", margin: 0, padding: 0 }}
              >
                {list.data.map((domain) => (
                  <li
                    key={domain.domain}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      justifyContent: "space-between",
                      gap: "var(--space-2)",
                      padding: "var(--space-2) 0",
                    }}
                  >
                    <span>
                      {domain.domain}
                      <span
                        style={{
                          color: "var(--text-muted)",
                          fontSize: "var(--text-sm)",
                          marginLeft: "var(--space-2)",
                        }}
                      >
                        {domain.verified
                          ? t("ownDomains.confirmed")
                          : t("ownDomains.candidate")}
                      </span>
                    </span>
                    <Button
                      variant="ghost"
                      aria-label={t("ownDomains.remove", {
                        domain: domain.domain,
                      })}
                      disabled={!canManage || remove.isPending}
                      onClick={() => remove.mutate(domain.domain)}
                    >
                      <Trash2 aria-hidden size={16} />
                    </Button>
                  </li>
                ))}
              </ul>
            )}

            <form
              style={{ display: "flex", gap: "var(--space-2)" }}
              onSubmit={(event) => {
                event.preventDefault();
                const domain = draft.trim();
                if (!domain) {
                  return;
                }
                add.mutate(domain, { onSuccess: () => setDraft("") });
              }}
            >
              <TextInput
                value={draft}
                aria-label={t("ownDomains.addLabel")}
                placeholder={t("ownDomains.placeholder")}
                disabled={!canManage || add.isPending}
                onChange={(event) => setDraft(event.target.value)}
              />
              <Button
                type="submit"
                variant="primary"
                disabled={!canManage || add.isPending || draft.trim() === ""}
              >
                {t("ownDomains.add")}
              </Button>
            </form>

            {!canManage && (
              <span
                style={{
                  color: "var(--text-muted)",
                  fontSize: "var(--text-sm)",
                }}
              >
                {t("captureSettings.adminOnly")}
              </span>
            )}
            {(add.isError || remove.isError) && (
              <span
                role="alert"
                style={{ color: "var(--danger)", fontSize: "var(--text-sm)" }}
              >
                {(add.error ?? remove.error)?.message}
              </span>
            )}
          </div>
        )}
      </QueryGate>
    </section>
  );
}
