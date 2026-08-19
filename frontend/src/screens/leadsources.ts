import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import type { useT } from "../i18n";
import { throwProblem } from "./common";

// The lead vocabularies and lead handling, read from the server: which
// sources a lead may come from (and what each is worth to the score), the
// reasons a lead may be closed with, and whether the first-response target
// is tracked at all. Every lead surface reads these through here so the
// create form, the filter chip, the detail page and the settings cards
// cannot disagree about the list.

export type LeadSource = components["schemas"]["LeadSource"];
export type DiscoveredLeadSource =
  components["schemas"]["DiscoveredLeadSource"];
export type LeadSourceIntent = components["schemas"]["LeadSourceIntent"];
export type LeadDisqualifyReason =
  components["schemas"]["LeadDisqualifyReason"];
export type LeadSettings = components["schemas"]["LeadSettings"];

export const LEAD_SOURCES_KEY = ["lead-sources"] as const;
export const LEAD_DISQUALIFY_REASONS_KEY = ["lead-disqualify-reasons"] as const;
export const LEAD_SETTINGS_KEY = ["lead-settings"] as const;

export function useLeadSources() {
  return useQuery({
    queryKey: LEAD_SOURCES_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.GET("/lead-sources");
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
  });
}

export function useLeadDisqualifyReasons() {
  return useQuery({
    queryKey: LEAD_DISQUALIFY_REASONS_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/lead-disqualify-reasons",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data.data;
    },
  });
}

export function useLeadSettings() {
  return useQuery({
    queryKey: LEAD_SETTINGS_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.GET("/leads/settings");
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
  });
}

export function useUpdateLeadSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (
      body: components["schemas"]["UpdateLeadSettingsRequest"],
    ) => {
      const { data, error } = await api.PATCH("/leads/settings", { body });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: LEAD_SETTINGS_KEY });
      // The list renders the SLA column and the Overdue view off this
      // setting, so the leads it already holds are stale the moment it flips.
      void queryClient.invalidateQueries({ queryKey: ["leads"] });
    },
  });
}

// The six shipped keys keep a client-side label so a lead renders sensibly
// before the list has loaded (and in a test that never serves it); anything
// the server labels wins over these.
const SHIPPED_SOURCE_LABELS: Readonly<
  Record<string, Parameters<ReturnType<typeof useT>>[0]>
> = {
  manual: "lead.source.manual",
  inbound: "lead.source.inbound",
  webform: "lead.source.webform",
  referral: "lead.source.referral",
  import: "lead.source.import",
  crawl: "lead.source.crawl",
};

// administeredOf is the absent-body guard every reader of the list shares: a
// payload that is not the contract's array, or a row without a key, is
// treated as nothing rather than crashing the render that reads it.
function administeredOf(
  sources: readonly LeadSource[] | undefined,
): LeadSource[] {
  if (!Array.isArray(sources)) return [];
  return sources.filter(
    (s) => typeof s?.key === "string" && typeof s?.label === "string",
  );
}

// sourceLabelFor names a stored source value for a reader: the server's own
// label when the row carries one, else the administered list's, else the
// shipped label, else a connector value's family ("Apollo") — never the
// raw `connector:apollo:a-1` token.
export function sourceLabelFor(
  lead: Readonly<{ source?: string | null; source_label?: string | null }>,
  sources: readonly LeadSource[] | undefined,
  t: ReturnType<typeof useT>,
): string {
  if (lead.source_label) return lead.source_label;
  return sourceKeyLabel(lead.source, sources, t);
}

export function sourceKeyLabel(
  source: string | null | undefined,
  sources: readonly LeadSource[] | undefined,
  t: ReturnType<typeof useT>,
): string {
  if (!source) return t("lead.shortfall.noSource");
  const known = administeredOf(sources);
  const administered = known.find((s) => s.key === source);
  if (administered) return administered.label;
  const shipped = SHIPPED_SOURCE_LABELS[source];
  if (shipped) return t(shipped);
  const parts = source.split(":").filter(Boolean);
  const family =
    parts[0] === "connector"
      ? known.find((s) => s.key === `${parts[0]}:${parts[1]}`)
      : undefined;
  if (family) return family.label;
  const meaningful = parts[0] === "connector" ? parts[1] : parts[0];
  return meaningful
    ? meaningful.charAt(0).toUpperCase() + meaningful.slice(1)
    : t("lead.source.unknown");
}

// The options a human may pick from when creating or correcting a lead:
// the ACTIVE administered sources, in the administrator's order. A lead that
// already carries a value outside that list (a connector's, a retired one)
// keeps it as a first, non-removable option so the control never shows a
// value it cannot name.
export function sourcePickOptions(
  sources: readonly LeadSource[] | undefined,
  current: string | null | undefined,
  t: ReturnType<typeof useT>,
): { value: string; label: string }[] {
  const active = administeredOf(sources).filter((s) => s.active);
  const options = active.map((s) => ({ value: s.key, label: s.label }));
  if (current && !active.some((s) => s.key === current)) {
    options.unshift({
      value: current,
      label: sourceKeyLabel(current, sources, t),
    });
  }
  return options;
}

// The filter chip offers everything a lead could carry: every administered
// source (retired ones included — the leads carrying them still exist) and
// every discovered value.
export function sourceFilterOptions(
  list:
    | Readonly<{
        data: readonly LeadSource[];
        discovered: readonly DiscoveredLeadSource[];
      }>
    | undefined,
  t: ReturnType<typeof useT>,
): { value: string; text: string }[] {
  // A body that lost a contract-required array is treated as the empty
  // list it claims rather than a crash in the render that reads it.
  const data = administeredOf(list?.data);
  const discovered = Array.isArray(list?.discovered)
    ? list.discovered.filter((d) => typeof d?.key === "string")
    : [];
  return [
    ...data.map((s) => ({ value: s.key, text: s.label })),
    ...discovered.map((d) => ({
      value: d.key,
      text: sourceKeyLabel(d.key, data, t),
    })),
  ];
}
