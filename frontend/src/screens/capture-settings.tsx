import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import { useCanWrite } from "../app/capability";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { Switch } from "../design-system/switch";
import { useT } from "../i18n";
import { problemMessageOf, QueryGate, throwProblem } from "./common";

// The organization capture-settings card (CAP-WIRE-7, ADR-0072/A118): the
// captured-organization auto-enrich toggle. Every role reads it; only admin/ops
// may change it, so the toggle is refused (never hidden) for other roles — a
// rep still sees whether auto-enrich is on. Mirrors the WebhooksCard gating.

function useCaptureSettings() {
  return useQuery({
    queryKey: ["capture-settings"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/capture/settings");
      if (error || !response.ok) {
        throwProblem(error);
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
        throwProblem(error);
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
  const canManage = useCanWrite("capture_settings", "update");
  const query = useCaptureSettings();
  const update = useUpdateCaptureSettings();

  // Panel rather than Card, and the gap to the card below comes from the
  // page's own stack (`.settings-stack`) rather than from a margin this card
  // carries — a surface that spaces itself is a surface that spaces itself
  // wrong the first time it is used anywhere else.
  //
  // Panel's header holds the title alone, so the card's one line of
  // description leads the body instead of riding in the header.
  return (
    <Panel title={t("captureSettings.title")}>
      {/* `form-stack` still earns its place: the failure Callout below the list
          is a non-row child, and without the body's gap it would butt against
          the last row's hairline. `.settings-panel-sub`'s own interval is
          already corrected for a `.form-stack` body, so the description lands
          on the same 16px it does in a plain one. */}
      <PanelBody className="form-stack">
        <p className="settings-panel-sub">{t("captureSettings.sub")}</p>
        <QueryGate query={query}>
          {(settings) => (
            <SettingList>
              {/* The row draws the naming — what the setting is, and what it
                  does — so the switch carries the same words hidden: it owns
                  its own accessible name by design, and pointing it at the
                  row's label as well would name it twice. */}
              <SettingRow
                label={t("captureSettings.autoEnrich.label")}
                description={t("captureSettings.autoEnrich.help")}
                control={
                  <Switch
                    testId="capture-auto-enrich-toggle"
                    label={t("captureSettings.autoEnrich.label")}
                    labelHidden
                    // Two reasons, and only one of them is worth words: a
                    // caller who may never change this needs to know why,
                    // where a write already in flight explains itself by
                    // finishing.
                    reason={
                      canManage ? undefined : t("captureSettings.adminOnly")
                    }
                    checked={settings.auto_enrich}
                    disabled={!canManage || update.isPending}
                    onChange={(next) => update.mutate(next)}
                  />
                }
              />
            </SettingList>
          )}
        </QueryGate>
        {update.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(update.error, t)}
          </Callout>
        )}
      </PanelBody>
    </Panel>
  );
}
