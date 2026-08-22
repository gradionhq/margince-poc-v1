import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { QueryStates, throwProblem } from "./common";
import { SentenceList } from "./company360";

// The deal in a few cited sentences. Drawn with the account brief's own
// sentence list, so a citation is clickable here exactly as it is there.

type DealBrief = components["schemas"]["DealBrief"];
type SectionKind = DealBrief["sections"][number]["kind"];

const SECTION_LABELS: Record<SectionKind, MessageKey> = {
  standing: "dealbrief.standing",
  activity: "dealbrief.activity",
  open: "dealbrief.open",
  room: "dealbrief.room",
};

export function DealBriefCard({ dealId }: Readonly<{ dealId: string }>) {
  const t = useT();
  const brief = useQuery({
    queryKey: ["deal-brief", dealId],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/brief", {
        params: { path: { id: dealId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const open = (entityType: string, entityId: string) => {
    if (entityType === "deal") {
      navigate({ screen: "deals", id: entityId });
    } else if (entityType === "person") {
      navigate({ screen: "contacts", id: entityId });
    }
  };
  const sections = brief.data?.sections ?? [];
  return (
    <Panel title={t("dealbrief.title")} sub={t("dealbrief.sub")}>
      <QueryStates query={brief} pendingLines={4}>
        {brief.isSuccess && sections.length === 0 ? (
          <PanelBody>
            <p className="t-small">{t("dealbrief.empty")}</p>
          </PanelBody>
        ) : null}
        {sections.map((section) => (
          <PanelBody key={section.kind}>
            <p className="t-caption">{t(SECTION_LABELS[section.kind])}</p>
            <SentenceList sentences={section.sentences} onOpenRecord={open} />
          </PanelBody>
        ))}
      </QueryStates>
    </Panel>
  );
}
