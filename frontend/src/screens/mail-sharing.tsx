import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import { useCanWrite } from "../app/capability";
import { Button } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { Switch } from "../design-system/switch";
import { useT } from "../i18n";
import { problemMessageOf, QueryGate, throwProblem } from "./common";

// The workspace mail-sharing posture: ON by default, captured mail is
// readable by every colleague who can see the contact — the thing that makes
// the pipeline shared. Switching it OFF holds every email captured from then
// on to its participants, which makes shared CRM work hard, so the change is
// a deliberate act: a switch plus a Save button plus a warning that says the
// cost out loud, never a silent instant toggle. Every role sees the posture;
// only admin/ops may change it (same gating as the auto-enrich card).

function useMailSharing() {
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

export function MailSharingCard() {
  const t = useT();
  const canManage = useCanWrite("capture_settings", "update");
  const query = useMailSharing();
  const queryClient = useQueryClient();
  // null = no unsaved change; the switch renders the stored posture.
  const [pending, setPending] = useState<boolean | null>(null);
  const save = useMutation({
    mutationFn: async (mailSharing: boolean) => {
      const { data, error } = await api.PATCH("/capture/settings", {
        body: { mail_sharing: mailSharing },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["capture-settings"], data);
      setPending(null);
    },
  });

  return (
    <Panel title={t("mailSharing.title")}>
      <PanelBody>
        <p className="settings-panel-sub">{t("mailSharing.sub")}</p>
        <QueryGate query={query}>
          {(settings) => {
            const shown = pending ?? settings.mail_sharing;
            const dirty = pending !== null && pending !== settings.mail_sharing;
            return (
              <>
                <SettingList>
                  {/* The row draws the naming, so the switch carries the same
                      words as its hidden label rather than a second heading and
                      a hint of its own beside them. */}
                  <SettingRow
                    label={t("mailSharing.label")}
                    description={t("mailSharing.help")}
                    // The function form, so the row's description reaches the
                    // switch: the sentence saying what sharing DOES used to be
                    // the switch's own `hint`, and moving it into the row would
                    // otherwise take it away from every reader who cannot see
                    // it. `labelHidden` still keeps the naming the row's.
                    control={(control) => (
                      <Switch
                        describedBy={control["aria-describedby"]}
                        testId="mail-sharing-toggle"
                        label={t("mailSharing.label")}
                        labelHidden
                        reason={
                          canManage ? undefined : t("captureSettings.adminOnly")
                        }
                        checked={shown}
                        disabled={!canManage || save.isPending}
                        onChange={(next) => setPending(next)}
                      />
                    )}
                  />
                </SettingList>
                {/* The cost of the posture and the verb that commits it belong
                    to the CARD, not to the row: an unsaved flip is a state of
                    the whole card, and a callout squeezed into a row's right
                    column would read as the switch's own answer. */}
                {(!shown || dirty || save.isError) && (
                  <div className="settings-panel-commit">
                    {!shown && (
                      <Callout tone="danger" live="alert">
                        {t("mailSharing.danger")}
                      </Callout>
                    )}
                    {save.isError && (
                      <Callout tone="danger" live="alert">
                        {problemMessageOf(save.error, t)}
                      </Callout>
                    )}
                    {dirty && (
                      <Button
                        small
                        variant="primary"
                        disabled={save.isPending}
                        onClick={() => {
                          if (pending !== null) {
                            save.mutate(pending);
                          }
                        }}
                      >
                        {t("mailSharing.save")}
                      </Button>
                    )}
                  </div>
                )}
              </>
            );
          }}
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}
