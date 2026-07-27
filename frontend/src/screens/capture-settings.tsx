import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Sparkles } from "lucide-react";
import { api } from "../api/client";
import { SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import {
  canConfigureAutomations,
  problemMessage,
  QueryGate,
  useMe,
} from "./common";

// The workspace capture-settings card (CAP-WIRE-7, ADR-0072/A118): the
// captured-organization auto-enrich toggle. Every role reads it; only admin/ops
// may change it, so the toggle is disabled (never hidden) for other roles — a
// rep still sees whether auto-enrich is on. Mirrors the WebhooksCard gating.

function useCaptureSettings() {
  return useQuery({
    queryKey: ["capture-settings"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/capture/settings");
      if (error || !response.ok) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

function useUpdateCaptureSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (autoEnrich: boolean) => {
      const { data, error } = await api.PATCH("/capture/settings", {
        body: { auto_enrich: autoEnrich },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["capture-settings"], data);
    },
  });
}

export function CaptureSettingsCard() {
  const t = useT();
  const me = useMe();
  const canManage = canConfigureAutomations(me.data?.roles);
  const query = useCaptureSettings();
  const update = useUpdateCaptureSettings();

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader
        title={t("captureSettings.title")}
        sub={t("captureSettings.sub")}
      />
      <QueryGate query={query}>
        {(settings) => (
          <label
            style={{
              display: "flex",
              alignItems: "flex-start",
              gap: "var(--space-3)",
              cursor: canManage ? "pointer" : "default",
            }}
          >
            <input
              type="checkbox"
              data-testid="capture-auto-enrich-toggle"
              checked={settings.auto_enrich}
              disabled={!canManage || update.isPending}
              onChange={(e) => update.mutate(e.target.checked)}
            />
            <span
              style={{
                display: "flex",
                flexDirection: "column",
                gap: "var(--space-1)",
              }}
            >
              <span
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: "var(--space-2)",
                }}
              >
                <Sparkles aria-hidden size={16} />
                {t("captureSettings.autoEnrich.label")}
              </span>
              <span
                style={{
                  color: "var(--text-muted)",
                  fontSize: "var(--text-sm)",
                }}
              >
                {t("captureSettings.autoEnrich.help")}
              </span>
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
              {update.isError && (
                <span
                  role="alert"
                  style={{ color: "var(--danger)", fontSize: "var(--text-sm)" }}
                >
                  {update.error.message}
                </span>
              )}
            </span>
          </label>
        )}
      </QueryGate>
    </section>
  );
}
