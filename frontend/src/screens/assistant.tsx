import { Badge } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import type { PickableProject } from "../design-system/projectpicker";
import { useT } from "../i18n";
import { AskSection } from "./company360";

/**
 * AssistantPanel is what you can ASK about this account.
 *
 * It used to also carry a standing "Account brief", whose sentences counted
 * what the page already showed — "you currently have three contacts recorded
 * for this account" — under a heading that promised a reading. A summary that
 * restates the screen is worse than no summary: it spends the reader's trust
 * and returns nothing. The account's actual reading is the brief at the top of
 * the page, which says what the records mean and what to do about it.
 *
 * A Panel, like every other section of the overview column it stands in. Drawn
 * as a `Card` it was the one box on that column with no header band, which
 * reads as a control from a different product rather than as one more part of
 * this record.
 */
export function AssistantPanel({
  orgId,
  enabled,
  onOpenRecord,
  projects,
}: Readonly<{
  orgId: string;
  enabled: boolean;
  onOpenRecord?: (entityType: string, entityId: string) => void;
  // The account's projects, for the question to be asked about one of them.
  projects?: readonly PickableProject[];
}>) {
  const t = useT();
  if (!enabled) {
    return null;
  }
  return (
    <Panel
      title={t("co.assistant.title")}
      // The disclosure is the badge, and it rides in the header band so it is
      // read before anything under it. The sentence that used to sit beside it
      // explained the panel's own epistemology to a reader who came here to
      // sell something, which is the UI talking about itself.
      titleAction={<Badge tone="ai">{t("co.assistant.aiTag")}</Badge>}
    >
      <PanelBody>
        <AskSection
          orgId={orgId}
          enabled={enabled}
          onOpenRecord={onOpenRecord}
          projects={projects}
        />
      </PanelBody>
    </Panel>
  );
}
