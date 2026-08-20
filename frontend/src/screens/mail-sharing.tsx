import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mails } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import { useCanWrite } from "../app/capability";
import { Button } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
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
      <PanelBody className="form-stack">
        <p className="t-caption">{t("mailSharing.sub")}</p>
        <QueryGate query={query}>
          {(settings) => {
            const shown = pending ?? settings.mail_sharing;
            const dirty = pending !== null && pending !== settings.mail_sharing;
            return (
              <>
                <Switch
                  testId="mail-sharing-toggle"
                  label={
                    <>
                      <Mails aria-hidden size={16} />
                      {t("mailSharing.label")}
                    </>
                  }
                  hint={t("mailSharing.help")}
                  reason={
                    canManage ? undefined : t("captureSettings.adminOnly")
                  }
                  checked={shown}
                  disabled={!canManage || save.isPending}
                  onChange={(next) => setPending(next)}
                />
                {!shown && (
                  <Callout tone="danger" live="alert">
                    {t("mailSharing.danger")}
                  </Callout>
                )}
                {dirty && (
                  <Button
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
                {save.isError && (
                  <Callout tone="danger" live="alert">
                    {problemMessageOf(save.error, t)}
                  </Callout>
                )}
              </>
            );
          }}
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}
