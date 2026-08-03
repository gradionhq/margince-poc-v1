import { Badge, SectionHeader } from "../design-system/atoms";
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
 */
export function AssistantPanel({
  orgId,
  enabled,
  onOpenRecord,
}: Readonly<{
  orgId: string;
  enabled: boolean;
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  if (!enabled) {
    return null;
  }
  return (
    <section className="card co-assistant">
      {/* The disclosure is the badge. The sentence beside it explained the
          panel's own epistemology to a reader who came here to sell
          something, which is the UI talking about itself. */}
      <SectionHeader title={t("co.assistant.title")} />
      <p className="co-assistant-disclosure">
        <Badge tone="ai">{t("co.assistant.aiTag")}</Badge>
      </p>
      <AskSection orgId={orgId} enabled={enabled} onOpenRecord={onOpenRecord} />
    </section>
  );
}
