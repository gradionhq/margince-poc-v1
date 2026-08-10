import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  Check,
  CircleAlert,
  Globe2,
  RefreshCw,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  Radio,
  SectionHeader,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  coldFieldLabel,
  problemMessageOf,
  QueryGate,
  throwProblem,
} from "./common";

type Capabilities = components["schemas"]["CompanyContextCapabilities"];
type CompanyProfile = components["schemas"]["CompanyProfile"];
type CompanyInput = components["schemas"]["CompanyProfileInput"];
type SiteRead = components["schemas"]["CompanySiteRead"];
type Comparison = components["schemas"]["CompanySiteReadComparison"];
type Resolution = components["schemas"]["CompanySiteReadResolution"];

const EMPTY_COMPANY_INPUT: CompanyInput = {
  display_name: "",
  website: "",
  offer_summary: "",
  icp: "",
};

const PROFILE_GROUPS = [
  {
    title: "settings.companyEssentials",
    fields: ["display_name", "offer_summary", "icp"],
  },
  {
    title: "settings.companyPositioning",
    fields: [
      "value_proposition",
      "usp",
      "customer_pains",
      "desired_outcomes",
      "buying_center",
      "buying_intents",
      "common_objections",
      "sales_motion",
    ],
  },
  {
    title: "settings.companyIdentity",
    fields: [
      "legal_name",
      "registered_address",
      "register_vat",
      "industry",
      "history",
    ],
  },
] as const satisfies readonly {
  title: MessageKey;
  fields: readonly (keyof CompanyInput)[];
}[];

const MULTILINE_FIELDS = new Set<keyof CompanyInput>([
  "offer_summary",
  "icp",
  "value_proposition",
  "customer_pains",
  "desired_outcomes",
  "buying_center",
  "buying_intents",
  "common_objections",
  "sales_motion",
  "history",
]);

// The rollout answer every surface that gates on the flag shares — the page
// here, the onboarding entry, and the settings nav. Named so a caller can ask
// the cache whether the answer has LANDED, which is a different question from
// whether the request went out.
export const companyContextCapabilitiesQueryKey = [
  "company-context-capabilities",
];

export function useCompanyContextCapabilities(enabled = true) {
  return useQuery({
    queryKey: companyContextCapabilitiesQueryKey,
    enabled,
    queryFn: async (): Promise<Capabilities> => {
      const { data, error } = await api.GET("/company/context/capabilities");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

// ManualCompanySetup is the rollback-safe first-run floor below the
// `onboarding` rollout stage. It creates the same canonical profile with the
// same semantic minimum, without exposing the new five-step experience.
export function ManualCompanySetup() {
  const t = useT();
  const queryClient = useQueryClient();
  const [form, setForm] = useState<CompanyInput>(EMPTY_COMPANY_INPUT);
  const save = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.PUT("/company", {
        body: trimCompanyInput(form),
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (profile) => {
      queryClient.setQueryData(["company"], profile);
      navigate({ screen: "home" });
    },
  });
  return (
    <div className="wrap narrow company-setup-floor">
      <div className="company-context-hero">
        <div>
          <span className="company-context-kicker">
            <ShieldCheck aria-hidden /> {t("settings.companyManualKicker")}
          </span>
          <h2>{t("settings.companyManualTitle")}</h2>
          <p>{t("settings.companyManualSub")}</p>
        </div>
      </div>
      <div className="company-context-form">
        {(["display_name", "offer_summary", "icp"] as const).map((field) => (
          <div className="company-context-field" key={field}>
            <span>{coldFieldLabel(field, t)}</span>
            {field === "display_name" ? (
              <TextInput
                value={String(form[field] ?? "")}
                aria-label={coldFieldLabel(field, t)}
                onChange={(event) =>
                  setForm({ ...form, [field]: event.target.value })
                }
              />
            ) : (
              <Textarea
                rows={4}
                value={String(form[field] ?? "")}
                aria-label={coldFieldLabel(field, t)}
                onChange={(event) =>
                  setForm({ ...form, [field]: event.target.value })
                }
              />
            )}
          </div>
        ))}
        {save.isError && (
          <p className="company-context-error">
            {problemMessageOf(save.error, t)}
          </p>
        )}
        <div className="company-context-actions">
          <Button
            variant="primary"
            disabled={!requiredComplete(form) || save.isPending}
            onClick={() => save.mutate()}
          >
            {t("settings.companyCreateWorkspace")} <ArrowRight aria-hidden />
          </Button>
        </div>
      </div>
    </div>
  );
}

function profileInput(profile: CompanyProfile): CompanyInput {
  return {
    display_name: profile.display_name,
    website: profileValue(profile, "website"),
    offer_summary: profileValue(profile, "offer_summary"),
    icp: profileValue(profile, "icp"),
    value_proposition: profileValue(profile, "value_proposition"),
    usp: profileValue(profile, "usp"),
    customer_pains: profileValue(profile, "customer_pains"),
    desired_outcomes: profileValue(profile, "desired_outcomes"),
    buying_center: profileValue(profile, "buying_center"),
    buying_intents: profileValue(profile, "buying_intents"),
    common_objections: profileValue(profile, "common_objections"),
    sales_motion: profileValue(profile, "sales_motion"),
    legal_name: profileValue(profile, "legal_name"),
    registered_address: profileValue(profile, "registered_address"),
    register_vat: profileValue(profile, "register_vat"),
    industry: profileValue(profile, "industry"),
    history: profileValue(profile, "history"),
  };
}

function profileValue(
  profile: CompanyProfile,
  field: keyof CompanyProfile,
): string {
  const value = profile[field];
  return typeof value === "string" ? value : "";
}

function absoluteWebsite(raw: string): string {
  const value = raw.trim();
  return /^https?:\/\//i.test(value) ? value : `https://${value}`;
}

// What the refresh area may say when the website read goes wrong. Only the
// START failure speaks verbatim: that problem answers the URL the reader just
// typed — the site is unreachable, robots refused it, the budget is spent —
// and it is guidance they can act on. A status poll that fails answers a read
// id nobody typed, so its detail is machinery talk and the catalog sentence is
// the honest thing to show; the failure itself still reaches the console
// through the shared query cache, which reports every query failure.
function refreshProblem(
  start: Readonly<{ isError: boolean; error: unknown }>,
  poll: Readonly<{ isError: boolean; error: unknown }>,
  t: ReturnType<typeof useT>,
): string | null {
  if (start.isError) {
    return problemMessageOf(start.error, t);
  }
  return poll.isError ? t("settings.companyRefreshUnreadable") : null;
}

export function CompanyContextCard() {
  const t = useT();
  const queryClient = useQueryClient();
  const capabilities = useCompanyContextCapabilities();
  const company = useQuery({
    queryKey: ["company"],
    queryFn: async (): Promise<CompanyProfile> => {
      const { data, error } = await api.GET("/company");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const [form, setForm] = useState<CompanyInput | null>(null);
  const [readID, setReadID] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [resolutions, setResolutions] = useState<Record<string, Resolution>>(
    {},
  );

  useEffect(() => {
    if (company.data) {
      setForm(profileInput(company.data));
    }
  }, [company.data]);

  const save = useMutation({
    mutationFn: async (body: CompanyInput) => {
      const { data, error } = await api.PUT("/company", { body });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (profile) => {
      queryClient.setQueryData(["company"], profile);
      setForm(profileInput(profile));
    },
  });

  const startRefresh = useMutation({
    mutationFn: async () => {
      const website = form?.website?.trim() ?? "";
      if (!website) {
        throwProblem({ title: t("settings.companyWebsiteRequired") });
      }
      const { data, error } = await api.POST("/company/site-reads", {
        params: { header: { "Idempotency-Key": crypto.randomUUID() } },
        body: { url: absoluteWebsite(website) },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (read) => {
      setReadID(read.id);
      setSelected(new Set());
      setResolutions({});
    },
  });

  const siteRead = useQuery({
    queryKey: ["company-context-refresh", readID],
    enabled: readID !== null,
    queryFn: async (): Promise<SiteRead> => {
      const { data, error } = await api.GET("/company/site-reads/{readId}", {
        params: { path: { readId: readID ?? "" } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status === "queued" || status === "reading" ? 900 : false;
    },
  });

  useEffect(() => {
    const comparisons = siteRead.data?.comparisons;
    if (!comparisons) {
      return;
    }
    setSelected(
      new Set(
        comparisons
          .filter(
            (item) =>
              item.classification === "new" ||
              item.classification === "machine_change",
          )
          .map((item) => item.key),
      ),
    );
  }, [siteRead.data?.comparisons]);

  const confirm = useMutation({
    mutationFn: async () => {
      if (!siteRead.data || !form) {
        throwProblem({ title: t("settings.companyRefreshUnavailable") });
      }
      const body = refreshConfirmation(
        form,
        siteRead.data,
        selected,
        resolutions,
      );
      const { data, error, response } = await api.POST(
        "/company/site-reads/{readId}/confirm",
        {
          params: {
            path: { readId: siteRead.data.id },
            header: { "Idempotency-Key": crypto.randomUUID() },
          },
          body,
        },
      );
      if (error) {
        if (response.status === 409) {
          throwProblem({ title: t("settings.companyRefreshStale") });
        }
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (profile) => {
      queryClient.setQueryData(["company"], profile);
      setForm(profileInput(profile));
      setReadID(null);
      setResolutions({});
    },
  });

  const refreshFailure = refreshProblem(startRefresh, siteRead, t);

  if (capabilities.data && !capabilities.data.read_enabled) {
    return null;
  }

  return (
    <section className="company-context-shell">
      <div className="company-context-hero">
        <div>
          <span className="company-context-kicker">
            <Sparkles aria-hidden /> {t("settings.companyKicker")}
          </span>
          <h2>{t("settings.companyTitle")}</h2>
          <p>{t("settings.companySub")}</p>
        </div>
        {capabilities.data && <Badge>{capabilities.data.rollout}</Badge>}
      </div>
      <QueryGate query={company}>
        {(profile) =>
          form && (
            <>
              <div className="company-context-trust">
                <ShieldCheck aria-hidden />
                <span>{t("settings.companyTrust")}</span>
                <strong>
                  {profile.fields?.length ?? 0} {t("settings.companyConfirmed")}
                </strong>
              </div>
              <div className="company-context-form">
                <div className="company-context-field company-context-website">
                  <span>{t("settings.companyWebsite")}</span>
                  <div className="company-context-website-row">
                    <TextInput
                      value={form.website ?? ""}
                      aria-label={t("settings.companyWebsite")}
                      onChange={(event) =>
                        setForm({ ...form, website: event.target.value })
                      }
                    />
                    <Button
                      variant="primary"
                      disabled={
                        startRefresh.isPending || !(form.website ?? "").trim()
                      }
                      onClick={() => startRefresh.mutate()}
                    >
                      <RefreshCw aria-hidden /> {t("settings.companyRefresh")}
                    </Button>
                  </div>
                </div>
                {PROFILE_GROUPS.map((group) => (
                  <div className="company-context-group" key={group.title}>
                    <SectionHeader title={t(group.title)} />
                    <div className="company-context-fields">
                      {group.fields.map((field) => (
                        <CompanyField
                          key={field}
                          field={field}
                          value={String(form[field] ?? "")}
                          profile={profile}
                          onChange={(value) =>
                            setForm({ ...form, [field]: value })
                          }
                        />
                      ))}
                    </div>
                  </div>
                ))}
                <div className="company-context-actions">
                  {save.isError && (
                    <p className="company-context-error">
                      {problemMessageOf(save.error, t)}
                    </p>
                  )}
                  {save.isSuccess && (
                    <span className="company-context-saved">
                      <Check aria-hidden /> {t("settings.companySaved")}
                    </span>
                  )}
                  <Button
                    variant="primary"
                    disabled={save.isPending || !requiredComplete(form)}
                    onClick={() => save.mutate(trimCompanyInput(form))}
                  >
                    {t("settings.companySave")}
                  </Button>
                </div>
              </div>
              {refreshFailure !== null && (
                <p className="company-context-error">{refreshFailure}</p>
              )}
              {siteRead.data && (
                <RefreshReview
                  read={siteRead.data}
                  selected={selected}
                  resolutions={resolutions}
                  onToggle={(key) => setSelected(toggleSet(selected, key))}
                  onResolve={(resolution) =>
                    setResolutions({
                      ...resolutions,
                      [resolution.key]: resolution,
                    })
                  }
                  onConfirm={() => confirm.mutate()}
                  confirming={confirm.isPending}
                  error={
                    confirm.error
                      ? problemMessageOf(confirm.error, t)
                      : undefined
                  }
                />
              )}
            </>
          )
        }
      </QueryGate>
    </section>
  );
}

function CompanyField({
  field,
  value,
  profile,
  onChange,
}: Readonly<{
  field: keyof CompanyInput;
  value: string;
  profile: CompanyProfile;
  onChange: (value: string) => void;
}>) {
  const t = useT();
  const provenance = profile.fields?.find((item) => item.field === field);
  const control = MULTILINE_FIELDS.has(field) ? (
    <Textarea
      rows={3}
      value={value}
      aria-label={coldFieldLabel(field, t)}
      onChange={(event) => onChange(event.target.value)}
    />
  ) : (
    <TextInput
      value={value}
      aria-label={coldFieldLabel(field, t)}
      onChange={(event) => onChange(event.target.value)}
    />
  );
  return (
    <div className="company-context-field">
      <span>{coldFieldLabel(field, t)}</span>
      {control}
      {provenance && (
        <small>
          <Badge>{provenance.source}</Badge>
          {provenance.source_url && (
            <a href={provenance.source_url} target="_blank" rel="noreferrer">
              {t("settings.companyViewSource")}
            </a>
          )}
        </small>
      )}
    </div>
  );
}

function RefreshReview(
  props: Readonly<{
    read: SiteRead;
    selected: Set<string>;
    resolutions: Record<string, Resolution>;
    onToggle: (key: string) => void;
    onResolve: (resolution: Resolution) => void;
    onConfirm: () => void;
    confirming: boolean;
    error?: string;
  }>,
) {
  const t = useT();
  const ready =
    props.read.status === "ready" || props.read.status === "partial";
  const conflicts = props.read.comparisons.filter(
    (item) => item.classification === "human_conflict",
  );
  const unresolved = conflicts.some((item) => {
    const resolution = props.resolutions[item.key];
    return (
      !resolution ||
      (resolution.action === "use_value" && !(resolution.value ?? "").trim())
    );
  });
  const coverage =
    props.read.pages.length === 0
      ? 0
      : Math.round(
          (props.read.pages.filter((page) => page.status === "fetched").length /
            props.read.pages.length) *
            100,
        );
  return (
    <div className="company-context-review">
      <div className="company-context-review-head">
        <div>
          <span className="company-context-kicker">
            <Globe2 aria-hidden /> {t("settings.companyRefreshReview")}
          </span>
          <h3>
            {ready
              ? t("settings.companyRefreshReady")
              : t("settings.companyRefreshReading")}
          </h3>
        </div>
        <div className="company-context-coverage">
          <strong>{coverage}%</strong>
          <span>{t("settings.companyCoverage")}</span>
        </div>
      </div>
      <div className="company-context-comparisons">
        {props.read.comparisons.map((item) => (
          <ComparisonRow
            key={`${item.value_kind}:${item.key}`}
            item={item}
            selected={props.selected.has(item.key)}
            resolution={props.resolutions[item.key]}
            onToggle={() => props.onToggle(item.key)}
            onResolve={props.onResolve}
          />
        ))}
      </div>
      {props.read.warnings.map((warning) => (
        <p className="company-context-warning" key={warning}>
          <CircleAlert aria-hidden /> {warning}
        </p>
      ))}
      <div className="company-context-actions">
        {props.error && <p className="company-context-error">{props.error}</p>}
        {unresolved && (
          <span className="company-context-warning">
            <CircleAlert aria-hidden /> {t("settings.companyResolveAll")}
          </span>
        )}
        <Button
          variant="primary"
          disabled={!ready || unresolved || props.confirming}
          onClick={props.onConfirm}
        >
          {t("settings.companyApplyRefresh")} <ArrowRight aria-hidden />
        </Button>
      </div>
    </div>
  );
}

function ComparisonRow(
  props: Readonly<{
    item: Comparison;
    selected: boolean;
    resolution?: Resolution;
    onToggle: () => void;
    onResolve: (resolution: Resolution) => void;
  }>,
) {
  const t = useT();
  const { item } = props;
  const conflict = item.classification === "human_conflict";
  // The field this card is about, named once: it heads the card AND names the
  // checkbox. A row of boxes that all announce the same words is a list a
  // screen reader cannot tell apart, and picking the wrong change here is what
  // gets written to the record.
  const fieldLabel = coldFieldLabel(item.key.split("/").at(-2) ?? item.key, t);
  return (
    <article className={`company-context-comparison is-${item.classification}`}>
      <div className="company-context-comparison-title">
        <div>
          <strong>{fieldLabel}</strong>
          <Badge>
            {t(`settings.companyClass.${item.classification}` as MessageKey)}
          </Badge>
        </div>
        {!conflict && item.classification !== "unchanged" && (
          <input
            type="checkbox"
            checked={props.selected}
            onChange={props.onToggle}
            aria-label={t("settings.companySelectChange", {
              field: fieldLabel,
            })}
          />
        )}
      </div>
      {item.current_value !== null && (
        <div className="company-context-current">
          <span>{t("settings.companyCurrent")}</span>
          <p>{item.current_value}</p>
        </div>
      )}
      <div className="company-context-proposed">
        <span>{t("settings.companyWebsiteProposal")}</span>
        <p>{item.proposed_value}</p>
      </div>
      {conflict && (
        <div className="company-context-resolutions">
          {(["keep_current", "accept_proposal"] as const).map((action) => (
            <Radio
              key={action}
              name={`resolution-${item.key}`}
              checked={props.resolution?.action === action}
              onChange={() => props.onResolve({ key: item.key, action })}
              label={t(`settings.companyResolution.${action}` as MessageKey)}
            />
          ))}
          <Radio
            name={`resolution-${item.key}`}
            checked={props.resolution?.action === "use_value"}
            onChange={() =>
              props.onResolve({
                key: item.key,
                action: "use_value",
                value: item.current_value ?? "",
              })
            }
            label={t("settings.companyResolution.use_value")}
          />
          {props.resolution?.action === "use_value" && (
            <TextInput
              value={props.resolution.value ?? ""}
              onChange={(event) =>
                props.onResolve({
                  key: item.key,
                  action: "use_value",
                  value: event.target.value,
                })
              }
            />
          )}
        </div>
      )}
    </article>
  );
}

function refreshConfirmation(
  current: CompanyInput,
  read: SiteRead,
  selected: Set<string>,
  resolutions: Record<string, Resolution>,
) {
  const profile = { ...current };
  for (const comparison of read.comparisons) {
    if (comparison.value_kind !== "profile_field") {
      continue;
    }
    if (
      selected.has(comparison.key) &&
      comparison.classification !== "human_conflict"
    ) {
      profile[comparison.key as keyof CompanyInput] = comparison.proposed_value;
    }
  }
  const factKeys = read.facts
    .filter((fact) => selected.has(fact.value_key))
    .map((fact) => fact.value_key);
  return {
    draft_version: read.draft_version,
    proposal_hash: read.proposal_hash,
    profile: trimCompanyInput(profile),
    selected_fact_keys: factKeys,
    resolutions: Object.values(resolutions),
  };
}

function requiredComplete(form: CompanyInput): boolean {
  return [form.display_name, form.offer_summary, form.icp].every(
    (value) => String(value ?? "").trim() !== "",
  );
}

function trimCompanyInput(form: CompanyInput): CompanyInput {
  return Object.fromEntries(
    Object.entries(form).map(([key, value]) => [
      key,
      typeof value === "string" ? value.trim() : value,
    ]),
  ) as CompanyInput;
}

function toggleSet(source: Set<string>, key: string): Set<string> {
  const next = new Set(source);
  if (next.has(key)) {
    next.delete(key);
  } else {
    next.add(key);
  }
  return next;
}
