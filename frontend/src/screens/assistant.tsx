import type { components } from "../api/schema";
import { Badge, SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { AskSection, BriefSection, SuggestionsSection } from "./company360";

type Organization360 = components["schemas"]["Organization360"];

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
  view,
  enabled,
  onOpenRecord,
}: Readonly<{
  orgId: string;
  view?: Organization360;
  enabled: boolean;
  onOpenRecord?: (entityType: string, entityId: string) => void;
}>) {
  const t = useT();
  if (!enabled) {
    return null;
  }
  return (
    <section className="card co-assistant">
      <SectionHeader title={t("co.assistant.title")} />
      <p className="co-assistant-disclosure">
        <Badge tone="ai">{t("co.assistant.aiTag")}</Badge>
        <span className="t-caption">{t("co.assistant.sub")}</span>
      </p>
      <BriefSection
        orgId={orgId}
        enabled={enabled}
        onOpenRecord={onOpenRecord}
      />
      <SuggestionsSection
        orgId={orgId}
        view={view}
        onOpenRecord={onOpenRecord}
      />
      <AskSection orgId={orgId} enabled={enabled} onOpenRecord={onOpenRecord} />
    </section>
  );
}
