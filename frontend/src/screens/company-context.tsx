import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  CircleAlert,
  RefreshCw,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanUpsert } from "../app/capability";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  Checkbox,
  Field,
  Radio,
  SectionHeader,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody, PanelPlate, PanelRow } from "../design-system/panel";
import { FieldDiff } from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  coldFieldLabel,
  problemMessageOf,
  QueryGate,
  throwProblem,
  useMe,
} from "./common";
import "./company-context.css";

type Capabilities = components["schemas"]["CompanyContextCapabilities"];
type CompanyProfile = components["schemas"]["CompanyProfile"];
type CompanyInput = components["schemas"]["CompanyProfileInput"];
type SiteRead = components["schemas"]["CompanySiteRead"];
type Comparison = components["schemas"]["CompanySiteReadComparison"];
type Resolution = components["schemas"]["CompanySiteReadResolution"];

/** What the reviewer had on screen at the moment they applied the refresh. */
type RefreshChoice = Readonly<{
  current: CompanyInput;
  read: SiteRead;
  selected: Set<string>;
  resolutions: Record<string, Resolution>;
}>;

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

// Joins comparison keys into the one string the default selection is keyed on.
// A NUL can appear in no key the server mints, so no key can ever split into
// two — which a separator drawn from ordinary punctuation could not promise.
const SELECTION_SEPARATOR = "\u0000";

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
    // One Panel, in the ONE lead tone, where a gradient with a decorative
    // circle and two bespoke boxes used to be. The heading is the panel's own
    // title rather than a bare <h2>: preflight leaves an unclassed heading at
    // body size, so the page's lead sentence used to render as body text
    // inside a gradient.
    <div className="wrap narrow">
      <Panel
        tone="accent"
        title={
          <>
            <ShieldCheck aria-hidden size={16} />
            {t("settings.companyManualTitle")}
          </>
        }
        actions={
          <Button
            variant="primary"
            disabled={!requiredComplete(form) || save.isPending}
            onClick={() => save.mutate()}
          >
            {t("settings.companyCreateWorkspace")} <ArrowRight aria-hidden />
          </Button>
        }
      >
        <PanelBody className="form-stack">
          <Eyebrow>{t("settings.companyManualKicker")}</Eyebrow>
          <p className="t-caption">{t("settings.companyManualSub")}</p>
          {(["display_name", "offer_summary", "icp"] as const).map((field) => (
            <Field key={field} label={coldFieldLabel(field, t)}>
              {(control) =>
                field === "display_name" ? (
                  <TextInput
                    {...control}
                    value={String(form[field] ?? "")}
                    onChange={(event) =>
                      setForm({ ...form, [field]: event.target.value })
                    }
                  />
                ) : (
                  <Textarea
                    {...control}
                    rows={4}
                    value={String(form[field] ?? "")}
                    onChange={(event) =>
                      setForm({ ...form, [field]: event.target.value })
                    }
                  />
                )
              }
            </Field>
          ))}
          {save.isError && (
            <Callout tone="danger" live="alert">
              {problemMessageOf(save.error, t)}
            </Callout>
          )}
        </PanelBody>
      </Panel>
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
  // Every seat reads this profile — it is the shared business context behind
  // drafting and search — and the settings entry that leads here opens on the
  // read grant. Writing it is an upsert: the company is one standing record
  // that the first save MINTS, so the server demands `create` when no anchor
  // exists and `update` when one does, deciding inside its own transaction. A
  // client asking for either verb alone would hide the editor from a principal
  // the server would have admitted.
  const me = useMe();
  const canEdit = useCanUpsert("organization");
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
  const seeded = useRef<string | null>(null);

  // Seed the editor from the server, and re-seed only when the server SAYS
  // something different — which is not the same question as whether react-query
  // handed over a new object.
  //
  // Every refetch mints one, and this query refetches on window focus like the
  // rest of them: an operator who tabs away to copy their positioning text out
  // of a deck and tabs back triggered a refetch that returned the profile
  // unchanged, and the effect then threw away everything they had typed since
  // the page loaded. Comparing the VALUES leaves an unsaved draft alone across
  // every refetch that changes nothing, and still shows another admin's change
  // when one really lands.
  //
  // This is also the only place the form is seeded. A save or an applied
  // refresh writes the returned profile into the query cache, which arrives
  // here — a second `setForm` at those call sites would be a second writer for
  // one piece of state.
  useEffect(() => {
    if (!company.data) {
      return;
    }
    const next = profileInput(company.data);
    const signature = JSON.stringify(next);
    if (seeded.current === signature) {
      return;
    }
    seeded.current = signature;
    setForm(next);
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
    },
  });

  const startRefresh = useMutation({
    // The website arrives as the mutation's VARIABLE rather than being read off
    // `form` through this closure, and that is a correctness fix rather than a
    // style one. react-query re-arms a mutation's options in a PASSIVE effect,
    // so between the commit that first renders this control with a loaded
    // `form` and the effect that hands the observer that render's closure there
    // is a window where the DOM offers an enabled button and the mutation still
    // holds the previous closure — the one where `form` was null. A click
    // landing in that window read "" and told a reader who has a website to add
    // one. React yields between commit and passive effects, so the window is
    // real in a browser and merely likelier on a loaded machine; it surfaced as
    // a flaky company-context suite failing on the guard below.
    //
    // A variable cannot be older than the button: the handler only exists in a
    // render where `form` is non-null, so what it passes is what the field
    // beside it shows.
    mutationFn: async (candidate: string) => {
      const website = candidate.trim();
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

  // What the reviewer starts from, as ONE comparable string, and the effect
  // below seeds from it rather than from the array.
  //
  // The poll re-fetches this read every 900ms while the crawl is still running
  // and hands back a new array each time. Keyed on the array's identity, the
  // seed therefore ran on every tick: it rebuilt the default Set roughly
  // once a second and wiped whatever the reviewer had ticked or cleared in the
  // meantime — a change they had deliberately deselected reappeared under their
  // cursor, and the box they ticked was gone by the time they read the next
  // row. What the seed is really about is the ARRIVAL of a set of proposals,
  // which is what this names: two polls that propose the same changes produce
  // the same string and the effect does not run again.
  const defaultSelection = (siteRead.data?.comparisons ?? [])
    .filter(
      (item) =>
        item.classification === "new" ||
        item.classification === "machine_change",
    )
    .map((item) => item.key)
    .join(SELECTION_SEPARATOR);

  useEffect(() => {
    setSelected(
      new Set(
        defaultSelection === ""
          ? []
          : defaultSelection.split(SELECTION_SEPARATOR),
      ),
    );
  }, [defaultSelection]);

  // Everything the confirm sends arrives as the mutation's VARIABLE, for the
  // reason spelled out on startRefresh above: react-query re-arms a mutation's
  // options in a passive effect, so a click landing between commit and that
  // effect runs the previous render's closure. Read through one, `form` and the
  // read are null and the reviewer is told their refresh is unavailable while
  // it is on the screen in front of them; `selected` and `resolutions` are
  // worse, because a stale pair sends a set of choices nobody made. The click
  // handler belongs to the committed render, so what it passes is what the
  // reviewer sees.
  const confirm = useMutation({
    mutationFn: async (choice: RefreshChoice) => {
      const body = refreshConfirmation(
        choice.current,
        choice.read,
        choice.selected,
        choice.resolutions,
      );
      const { data, error, response } = await api.POST(
        "/company/site-reads/{readId}/confirm",
        {
          params: {
            path: { readId: choice.read.id },
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
      // The editor re-seeds itself from the cache — see the seed effect above,
      // which owns that state — so this only writes what the applied refresh
      // ENDS: the read the reviewer was working through, and the choices they
      // made in it.
      queryClient.setQueryData(["company"], profile);
      setReadID(null);
      setResolutions({});
    },
  });

  const refreshFailure = refreshProblem(startRefresh, siteRead, t);
  // Bound to a const so the narrowing below survives into the confirm handler:
  // TypeScript discards a narrowed PROPERTY access inside a closure, and the
  // whole point of that handler is to carry the read rather than re-read it.
  const read = siteRead.data;

  if (capabilities.data && !capabilities.data.read_enabled) {
    return null;
  }

  return (
    <div className="company-context-shell">
      {/* The lead panel, in the one accent tone, where a gradient with a 180px
          decorative circle used to be. Its title is Panel's own <h2>: the hero
          spelled a bare <h2>, which preflight leaves at 14px/400 — the page's
          lead sentence rendered as body text. */}
      <Panel
        tone="accent"
        title={
          <>
            <Sparkles aria-hidden size={16} />
            {t("settings.companyTitle")}
          </>
        }
        // The head carries the title ALONE. It is a fixed band and this title
        // is a whole sentence, so anything beside it leaves the sentence a
        // three-word column on a phone — and the accent tone tints that band,
        // which a neutral badge sitting on it disappears into. The eyebrow and
        // the rollout stage take the body's own ground below instead, where
        // each is read rather than squeezed.
        //
        // The save, or nothing. A surface that has already stated its read-only
        // posture does not annotate the absence of each write control
        // (design-system README, "Absent, disabled, or withheld"), and the band
        // is Panel's slot for the verbs that change the panel — under the last
        // field it commits, rather than after a card boundary.
        actions={
          canEdit && form ? (
            <Button
              variant="primary"
              disabled={save.isPending || !requiredComplete(form)}
              onClick={() => save.mutate(trimCompanyInput(form))}
            >
              {t("settings.companySave")}
            </Button>
          ) : null
        }
      >
        <PanelBody className="form-stack">
          <div className="company-context-kicker">
            <Eyebrow>{t("settings.companyKicker")}</Eyebrow>
            {capabilities.data && <Badge>{capabilities.data.rollout}</Badge>}
          </div>
          <p className="t-caption">{t("settings.companySub")}</p>
          {/* The surface keeps its place and states its posture ONCE. This is a
              PERMISSION, which is why it speaks at all — the rollout flag above
              returns null instead, because a capability this installation does
              not have is not a fact about the reader. Gated on the probe having
              answered, so a reader who may edit never sees this flash while /me
              is in flight. */}
          {me.isSuccess && !canEdit && (
            <p className="t-caption">{t("settings.companyReadOnly")}</p>
          )}
        </PanelBody>
        {/* What IS, on the recessed plate, apart from what to do with it. */}
        {company.data && (
          <PanelPlate className="company-context-trust">
            <ShieldCheck aria-hidden size={16} />
            <span>{t("settings.companyTrust")}</span>
            <strong>
              {company.data.fields?.length ?? 0}{" "}
              {t("settings.companyConfirmed")}
            </strong>
          </PanelPlate>
        )}
        <PanelBody className="form-stack">
          <QueryGate query={company}>
            {(profile) =>
              form && (
                <>
                  <Field
                    label={t("settings.companyWebsite")}
                    className="company-context-website"
                  >
                    {(control) => (
                      <div className="company-context-website-row">
                        <TextInput
                          {...control}
                          value={form.website ?? ""}
                          onChange={(event) =>
                            setForm({ ...form, website: event.target.value })
                          }
                        />
                        {/* Reading the website is a write of this profile: the
                            server admits the read on the same create-or-update
                            the save needs, because a read exists to change what
                            the record says. */}
                        {canEdit && (
                          <Button
                            variant="primary"
                            disabled={
                              startRefresh.isPending ||
                              !(form.website ?? "").trim()
                            }
                            onClick={() =>
                              startRefresh.mutate(form.website ?? "")
                            }
                          >
                            <RefreshCw aria-hidden size={16} />{" "}
                            {t("settings.companyRefresh")}
                          </Button>
                        )}
                      </div>
                    )}
                  </Field>
                  {PROFILE_GROUPS.map((group) => (
                    <div className="company-context-group" key={group.title}>
                      <SectionHeader title={t(group.title)} level={3} />
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
                </>
              )
            }
          </QueryGate>
          {/* Both outcomes of a save are SPOKEN. They used to be a tinted
              paragraph and a green tick with no live region between them, which
              is the same as saying nothing to a reader who is not looking at
              the button they just pressed. */}
          {save.isError && (
            <Callout tone="danger" live="alert">
              {problemMessageOf(save.error, t)}
            </Callout>
          )}
          {save.isSuccess && (
            <Callout tone="success" live="status">
              {t("settings.companySaved")}
            </Callout>
          )}
          {refreshFailure !== null && (
            <Callout tone="danger" live="alert">
              {refreshFailure}
            </Callout>
          )}
        </PanelBody>
      </Panel>
      {read && form && (
        <RefreshReview
          read={read}
          selected={selected}
          resolutions={resolutions}
          onToggle={(key) => setSelected(toggleSet(selected, key))}
          onResolve={(resolution) =>
            setResolutions({
              ...resolutions,
              [resolution.key]: resolution,
            })
          }
          onConfirm={() =>
            confirm.mutate({
              current: form,
              read,
              selected,
              resolutions,
            })
          }
          canApply={canEdit}
          confirming={confirm.isPending}
          error={confirm.error ? problemMessageOf(confirm.error, t) : undefined}
        />
      )}
    </div>
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
  const multiline = MULTILINE_FIELDS.has(field);
  // Field, not a span above a control: it owns the id and draws a real
  // `<label for>`, so the words above the box are the box's own click target
  // and its accessible name — which is what the hand-rolled row used an
  // aria-label to fake, one per call site.
  return (
    <Field label={coldFieldLabel(field, t)}>
      {(control) => (
        <>
          {multiline ? (
            <Textarea
              {...control}
              rows={3}
              value={value}
              onChange={(event) => onChange(event.target.value)}
            />
          ) : (
            <TextInput
              {...control}
              value={value}
              onChange={(event) => onChange(event.target.value)}
            />
          )}
          {/* Where the value came from, under the value rather than in its
              name: folded into the label it would be read out with the control
              every time focus lands there. */}
          {provenance && (
            <span className="company-context-source t-small">
              <Badge>{provenance.source}</Badge>
              {provenance.source_url && (
                <a
                  href={provenance.source_url}
                  target="_blank"
                  rel="noreferrer"
                >
                  {t("settings.companyViewSource")}
                </a>
              )}
            </span>
          )}
        </>
      )}
    </Field>
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
    /** The same answer the save asks for: applying a read WRITES the profile.
     *  Carried rather than re-derived so a grant revoked while a reviewer sits
     *  on this screen takes the apply with it — the /me snapshot refreshes on
     *  window focus, and the review outlives that. */
    canApply: boolean;
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
    // The review as a Panel: the state sentence is its title (it was a bare
    // <h3>, which preflight draws at body size), the comparisons are full-bleed
    // rows, and the coverage figure sits in the footer band because it belongs
    // to the whole read rather than to any one row. It used to be 24px — larger
    // than the page's own h1 — for a number nobody acts on.
    <Panel
      title={
        ready
          ? t("settings.companyRefreshReady")
          : t("settings.companyRefreshReading")
      }
      footer={
        <span className="company-context-coverage">
          <strong>{coverage}%</strong> {t("settings.companyCoverage")}
        </span>
      }
      actions={
        <>
          {unresolved && (
            <Callout tone="warn" icon={CircleAlert}>
              {t("settings.companyResolveAll")}
            </Callout>
          )}
          {props.error && (
            <Callout tone="danger" live="alert">
              {props.error}
            </Callout>
          )}
          {props.canApply && (
            <Button
              variant="primary"
              disabled={!ready || unresolved || props.confirming}
              onClick={props.onConfirm}
            >
              {t("settings.companyApplyRefresh")} <ArrowRight aria-hidden />
            </Button>
          )}
        </>
      }
    >
      {/* What this panel IS, under the title that says where the read has got
          to. Beside the title it would squeeze a sentence into a column on a
          phone, for the same reason the profile panel's eyebrow sits here. */}
      <PanelBody className="form-stack">
        <Eyebrow>{t("settings.companyRefreshReview")}</Eyebrow>
        {props.read.warnings.map((warning) => (
          <Callout tone="warn" icon={CircleAlert} key={warning}>
            {warning}
          </Callout>
        ))}
      </PanelBody>
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
    </Panel>
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
  const selectable = !conflict && item.classification !== "unchanged";
  return (
    <PanelRow
      className={`company-context-comparison is-${item.classification}`}
    >
      <div className="company-context-comparison-title">
        {/* On a selectable row the field name IS the tick's other half, which
            is what makes the words clickable; the aria-label spells the whole
            instruction and contains those words, so the visible name and the
            announced one agree (WCAG 2.5.3). A row with nothing to choose keeps
            the name as plain text rather than a control that does nothing. */}
        {selectable ? (
          <Checkbox
            checked={props.selected}
            onChange={props.onToggle}
            aria-label={t("settings.companySelectChange", {
              field: fieldLabel,
            })}
            label={<strong>{fieldLabel}</strong>}
          />
        ) : (
          <strong>{fieldLabel}</strong>
        )}
        <Badge>
          {t(`settings.companyClass.${item.classification}` as MessageKey)}
        </Badge>
      </div>
      {/* The design system's own old→new diff. A null current value reads as
          the "created" marker rather than as a blank box claiming we held an
          empty string. */}
      <FieldDiff oldValue={item.current_value} newValue={item.proposed_value} />
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
            /* Named, because this field decides what gets written to the company
               record and it had no accessible name at all — no label, no
               aria-label, not even a placeholder. It is revealed by the radio
               above it, so it takes that radio's words plus the field this
               conflict is about: "Keep this value" alone would be one of several
               identical names on a page resolving several conflicts. */
            <TextInput
              aria-label={t("settings.companyResolution.useValueFor", {
                field: fieldLabel,
              })}
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
    </PanelRow>
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
