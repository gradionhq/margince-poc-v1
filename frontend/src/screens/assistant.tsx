import { Badge, SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { AskSection, BriefSection } from "./company360";

/**
 * AssistantPanel is everything Margince has to say about this account, in one
 * place.
 *
 * The standing brief, what the account looks like it needs next, and the
 * answers to the prepared questions were three cards of equal weight in the
 * middle column. They are one voice written from one set of records, and three
 * separate frames made the reader treat them as three separate opinions — and
 * repeated the AI-assisted disclosure three times over.
 *
 * Each part keeps its own state and its own provenance line: a model-written
 * brief sitting above a deterministic answer is a real combination, and
 * flattening the two into one panel-level claim about who wrote what would be
 * the one thing this panel must never do.
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
      <BriefSection
        orgId={orgId}
        enabled={enabled}
        onOpenRecord={onOpenRecord}
      />
      <AskSection orgId={orgId} enabled={enabled} onOpenRecord={onOpenRecord} />
    </section>
  );
}
