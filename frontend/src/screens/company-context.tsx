import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  CircleAlert,
  RefreshCw,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { usePublishSelection } from "../app/attention";
import { useCanUpsert } from "../app/capability";
import { navigate } from "../app/router";
import { useUnsavedGuard } from "../app/unsaved";
import {
  Badge,
  Button,
  Checkbox,
  Disclosure,
  Field,
  Modal,
  Radio,
  SectionHeader,
  Textarea,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import {
  EvidenceMark,
  type EvidenceMarkSource,
} from "../design-system/evidencemark";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { FieldDiff } from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  coldFieldLabel,
  problemMessageOf,
  provenanceOf,
  QueryGate,
  type QueryLike,
  throwProblem,
  useMe,
} from "./common";
import { confidenceLevel } from "./inbox";
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

// The three the save DEMANDS, and the two groups that say more.
//
// The distinction is the server's, not a presentation choice: `requiredComplete`
// below is exactly this first group, so the essentials are the rows the card
// shows standing open and the rest are what a reader unfolds when they have
// something to add. All three groups are still one form — see the dialog, where
// they are the sections of it — because the profile is written by ONE PUT.
const [ESSENTIALS, ...ELABORATIONS] = PROFILE_GROUPS;

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
            small
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
  // Which row's Edit was pressed, and so where the dialog puts focus. One
  // dialog holds every field because ONE PUT writes them: a per-group form
  // would promise three independent writes the server does not offer, and the
  // reader would be committing a draft of the other twelve fields unseen.
  const [editing, setEditing] = useState<keyof CompanyInput | null>(null);
  const [readID, setReadID] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  // The agent surface reports what the reader is doing, and a selection is the
  // clearest statement of it a screen can make (app/attention.tsx).
  usePublishSelection(selected.size);
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
      // Committing is what the dialog was opened for, so a landed save closes
      // it and leaves the confirmation on the card behind — where the rows the
      // save changed are. A refused save keeps the dialog open with what was
      // typed still in it.
      setEditing(null);
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

  // An unsaved edit, and the window's own question about leaving with one.
  //
  // The draft outlives the dialog on purpose — dismissing the dialog does not
  // destroy what was typed, and reopening Edit shows it again — which means a
  // card whose dialog is closed can still be holding a change nobody has
  // committed. Compared against what the server SAID rather than against the
  // object it said it in, for the same reason the seed effect above does: every
  // refetch mints a new object and none of them is an edit.
  const stored = company.data ? profileInput(company.data) : null;
  const dirty =
    form !== null &&
    stored !== null &&
    JSON.stringify(form) !== JSON.stringify(stored);
  useUnsavedGuard(dirty);

  if (capabilities.data && !capabilities.data.read_enabled) {
    return null;
  }

  return (
    <div className="company-context-shell">
      <CompanyFactsCard
        company={company}
        rollout={capabilities.data?.rollout}
        form={form}
        canEdit={canEdit}
        // The posture is a fact about the READER, so it waits for the probe
        // that answers it: a reader who may edit never sees this flash while
        // /me is in flight.
        readOnly={me.isSuccess && !canEdit}
        saved={save.isSuccess}
        onEdit={setEditing}
      />
      {form && (
        <CompanySourceCard
          form={form}
          canEdit={canEdit}
          refreshing={startRefresh.isPending}
          failure={refreshFailure}
          onEdit={() => setEditing("website")}
          onRefresh={() => startRefresh.mutate(form.website ?? "")}
        />
      )}
      {editing !== null && form !== null && (
        <CompanyProfileDialog
          form={form}
          focus={editing}
          pending={save.isPending}
          error={save.isError ? problemMessageOf(save.error, t) : null}
          onChange={setForm}
          onClose={() => setEditing(null)}
          onSubmit={() => save.mutate(trimCompanyInput(form))}
        />
      )}
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

/**
 * WHAT we hold, as one row per fact.
 *
 * What stood here was a form dump: an eyebrow repeating the title, a recessed
 * plate carrying two unrelated facts, a website field with a button beside it,
 * then seventeen inputs under three heading levels with a provenance chip
 * floating under each. Now every fact a reader can change is a ROW — named on
 * the left, what it currently says on the right, and the editing behind one
 * verb, because ONE PUT writes all of them. Where the site read a value rather
 * than a person typing it, the value carries the design system's own provenance
 * mark instead of a chip of its own.
 */
function CompanyFactsCard({
  company,
  rollout,
  form,
  canEdit,
  readOnly,
  saved,
  onEdit,
}: Readonly<{
  company: QueryLike<CompanyProfile>;
  /** Which rollout stage this installation is on, once the probe has answered. */
  rollout?: Capabilities["rollout"];
  form: CompanyInput | null;
  canEdit: boolean;
  readOnly: boolean;
  saved: boolean;
  onEdit: (field: keyof CompanyInput) => void;
}>) {
  const t = useT();
  return (
    <Panel
      tone="accent"
      title={
        <>
          <Sparkles aria-hidden size={16} />
          {t("settings.companyTitle")}
        </>
      }
      // The two facts that belong to the WHOLE card, in the band that reports
      // rather than acts: how much of this profile somebody has confirmed, and
      // which rollout stage this installation is on. Neither is a decision, so
      // neither is a row. The plate that used to carry the count paired it with
      // the sentence about website text never becoming instructions — a promise
      // about the READ, which is the card below.
      footer={
        company.data ? (
          <>
            <span className="company-context-count">
              <strong>{company.data.fields?.length ?? 0}</strong>{" "}
              {t("settings.companyConfirmed")}
            </span>
            {rollout && <Badge>{rollout}</Badge>}
          </>
        ) : undefined
      }
    >
      <PanelBody>
        <p className="settings-panel-sub">{t("settings.companySub")}</p>
        {/* The surface keeps its place and states its posture ONCE. This is a
            PERMISSION, which is why it speaks at all — the rollout flag returns
            null instead, because a capability this installation does not have
            is not a fact about the reader. */}
        {readOnly && (
          <p className="t-caption">{t("settings.companyReadOnly")}</p>
        )}
        <QueryGate query={company}>
          {(profile) =>
            form && (
              <>
                <SettingList>
                  {ESSENTIALS.fields.map((field) => (
                    <CompanyFactRow
                      key={field}
                      field={field}
                      value={String(form[field] ?? "")}
                      profile={profile}
                      canEdit={canEdit}
                      onEdit={() => onEdit(field)}
                    />
                  ))}
                  {/* The elaborations, closed. Thirteen optional statements
                      against the three the save DEMANDS, and open by default
                      they buried the three that decide whether this profile is
                      usable at all. A Disclosure inside the list is the
                      settings page's own answer for a card's secondary half:
                      its summary sits on the same beat as the labels above. */}
                  {ELABORATIONS.map((group) => (
                    <Disclosure key={group.title} summary={t(group.title)}>
                      <SettingList>
                        {group.fields.map((field) => (
                          <CompanyFactRow
                            key={field}
                            field={field}
                            value={String(form[field] ?? "")}
                            profile={profile}
                            canEdit={canEdit}
                            onEdit={() => onEdit(field)}
                          />
                        ))}
                      </SettingList>
                    </Disclosure>
                  ))}
                </SettingList>
                {/* The save landed and the dialog it landed in is gone, so the
                    confirmation is left on the card that now shows the new
                    values. A refusal stays in the dialog, beside the fields it
                    refused. */}
                {saved && (
                  <div className="settings-panel-commit">
                    <Callout tone="success" live="status">
                      {t("settings.companySaved")}
                    </Callout>
                  </div>
                )}
              </>
            )
          }
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}

/**
 * WHERE we read it from, and the verb that reads it again.
 *
 * Its own card because it answers a different question from the one above: that
 * card says what we hold, this one says which site we hold it from and offers
 * the reading. The trust rule rides here for the same reason — that website
 * text never becomes instructions is a promise about the READ, and on the plate
 * above it read as a caption on the profile.
 */
function CompanySourceCard({
  form,
  canEdit,
  refreshing,
  failure,
  onEdit,
  onRefresh,
}: Readonly<{
  form: CompanyInput;
  canEdit: boolean;
  refreshing: boolean;
  /** What went wrong with the last read, in words a reader can act on. */
  failure: string | null;
  onEdit: () => void;
  onRefresh: () => void;
}>) {
  const t = useT();
  const website = (form.website ?? "").trim();
  return (
    <Panel title={t("settings.companySourceTitle")}>
      <PanelBody>
        <p className="settings-panel-sub">{t("settings.companyTrust")}</p>
        <SettingList>
          <SettingRow
            label={t("settings.companyWebsite")}
            description={t("settings.companyWebsiteHint")}
            value={website === "" ? t("field.unset") : website}
            control={
              canEdit ? (
                <Button
                  small
                  variant="ghost"
                  aria-label={t("settings.companyEditField", {
                    field: t("settings.companyWebsite"),
                  })}
                  onClick={onEdit}
                >
                  {t("settings.companyEdit")}
                </Button>
              ) : null
            }
          />
          {/* Reading the website is a WRITE of this profile: the server admits
              the read on the same create-or-update the save needs, because a
              read exists to change what the record says. Absent without that
              grant, like every other verb on these two cards — the posture is
              stated once, on the card above.

              This is the one card on the page that exists to make a MOVE, which
              is what earns the primary. The refusal it can state is stated:
              with no website there is nothing to read, in the same sentence the
              start itself would answer with. */}
          {canEdit && (
            <SettingRow
              label={t("settings.companyRefreshRow")}
              description={t("settings.companyRefreshHint")}
              control={
                <Button
                  small
                  variant="primary"
                  reason={
                    website === ""
                      ? t("settings.companyWebsiteRequired")
                      : undefined
                  }
                  pending={refreshing}
                  onClick={onRefresh}
                >
                  <RefreshCw aria-hidden size={16} />{" "}
                  {t("settings.companyRefresh")}
                </Button>
              }
            />
          )}
        </SettingList>
        {failure !== null && (
          <div className="settings-panel-commit">
            <Callout tone="danger" live="alert">
              {failure}
            </Callout>
          </div>
        )}
      </PanelBody>
    </Panel>
  );
}

/**
 * What we hold for one field, as a row: the fact named, what it says, and the
 * verb that changes it.
 *
 * The value is the row's ANSWER rather than an input, which is what the whole
 * card gained: a reader auditing this profile travels one column instead of
 * reading seventeen boxes to find out which of them are empty.
 */
function CompanyFactRow({
  field,
  value,
  profile,
  canEdit,
  onEdit,
}: Readonly<{
  field: keyof CompanyInput;
  value: string;
  profile: CompanyProfile;
  canEdit: boolean;
  onEdit: () => void;
}>) {
  const t = useT();
  const label = coldFieldLabel(field, t);
  const stored = profile.fields?.find((item) => item.field === field);
  return (
    <SettingRow
      label={label}
      // An empty field says so, in the words every other surface uses for it.
      // A blank right column is indistinguishable from a row that failed to
      // render its answer.
      value={
        <EvidenceMark
          value={value.trim() === "" ? t("field.unset") : value}
          source={stored ? derivedSource(stored) : undefined}
        />
      }
      control={
        canEdit ? (
          <Button
            small
            variant="ghost"
            // Named by the fact it changes, not "Edit": seventeen rows offering
            // seventeen identically-named buttons make a screen reader's user
            // count them to find out which one they are on.
            aria-label={t("settings.companyEditField", { field: label })}
            onClick={onEdit}
          >
            {t("settings.companyEdit")}
          </Button>
        ) : null
      }
    />
  );
}

/**
 * Where a value came from, when a person did not type it.
 *
 * A human-entered value gets no mark, which is the record page's rule and the
 * reason the mark means anything: this profile is mostly typed by people, and
 * underlining all of it would say only "this is a value". What the mark carries
 * is deliberately not a date — the surrounding surface is a settings page with
 * no record zone of its own, and a timestamp rendered in some other zone is
 * worse than none.
 */
function derivedSource(
  stored: components["schemas"]["CompanyProfileField"],
): EvidenceMarkSource | undefined {
  const provenance = provenanceOf(stored.captured_by);
  if (provenance.kind === "human") {
    return undefined;
  }
  return {
    provenance,
    confidence: confidenceLevel(stored.confidence) ?? undefined,
    snippet: stored.evidence_snippet,
    sourceUrl: stored.source_url,
  };
}

/**
 * The one form every row's Edit verb opens.
 *
 * One dialog rather than one per group, because ONE PUT writes this profile:
 * three dialogs would each be committing the other two groups' unsaved draft
 * without showing it. The groups survive as the dialog's own sections, which is
 * where a form's headings belong — on the page they were three heading levels
 * deep on top of the card's own title.
 */
function CompanyProfileDialog({
  form,
  focus,
  pending,
  error,
  onChange,
  onClose,
  onSubmit,
}: Readonly<{
  form: CompanyInput;
  /** Which row's Edit was pressed, and so where focus lands. */
  focus: keyof CompanyInput;
  pending: boolean;
  error: string | null;
  onChange: (next: CompanyInput) => void;
  onClose: () => void;
  onSubmit: () => void;
}>) {
  const t = useT();
  const titleId = useId();
  // Focus lands on the field whose Edit was pressed — programmatic rather than
  // the `autoFocus` attribute, so the a11y lint's blanket rule against
  // autofocus stays intact. A callback rather than a ref handed down: the field
  // it lands on is an input for some rows and a textarea for others, and one
  // callback taking the element they have in common beats two refs the caller
  // would have to pick between.
  const asked = useRef<HTMLElement | null>(null);
  const capture = (node: HTMLElement | null) => {
    asked.current = node;
  };
  useEffect(() => {
    asked.current?.focus();
  }, []);
  return (
    <Modal open onClose={onClose} labelledBy={titleId} size="wide">
      <h2 id={titleId} className="t-h2 modal-title">
        {t("settings.companyTitle")}
      </h2>
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          onSubmit();
        }}
      >
        {/* The site the read starts from, above the statements a read would
            propose changes to: it is the precondition for everything below. */}
        <CompanyFieldInput
          field="website"
          form={form}
          asked={focus === "website" ? capture : undefined}
          onChange={onChange}
        />
        {PROFILE_GROUPS.map((group) => (
          <div className="form-stack" key={group.title}>
            <SectionHeader title={t(group.title)} level={3} />
            {group.fields.map((field) => (
              <CompanyFieldInput
                key={field}
                field={field}
                form={form}
                asked={focus === field ? capture : undefined}
                onChange={onChange}
              />
            ))}
          </div>
        ))}
        {error !== null && (
          <Callout tone="danger" live="alert">
            {error}
          </Callout>
        )}
        <div className="form-actions">
          <Button small variant="ghost" type="button" onClick={onClose}>
            {t("create.cancel")}
          </Button>
          {/* The three the server demands are the three the button waits for —
              the same condition the page's Save carried, now beside the fields
              that satisfy it. */}
          <Button
            small
            type="submit"
            variant="primary"
            disabled={!pending && !requiredComplete(form)}
            pending={pending}
            busyLabel={t("common.saving")}
          >
            {t("settings.companySave")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/**
 * One field of that form. `Field` owns the id and draws a real `<label for>`,
 * so the words above the box are the box's own click target and its accessible
 * name.
 */
function CompanyFieldInput({
  field,
  form,
  asked,
  onChange,
}: Readonly<{
  field: keyof CompanyInput;
  form: CompanyInput;
  /** Set on the ONE field whose Edit opened this dialog, so focus lands there. */
  asked?: (node: HTMLElement | null) => void;
  onChange: (next: CompanyInput) => void;
}>) {
  const t = useT();
  const value = String(form[field] ?? "");
  const label =
    field === "website"
      ? t("settings.companyWebsite")
      : coldFieldLabel(field, t);
  return (
    <Field label={label}>
      {(control) =>
        MULTILINE_FIELDS.has(field) ? (
          <Textarea
            {...control}
            ref={asked}
            rows={3}
            value={value}
            onChange={(event) =>
              onChange({ ...form, [field]: event.target.value })
            }
          />
        ) : (
          <TextInput
            {...control}
            ref={asked}
            value={value}
            onChange={(event) =>
              onChange({ ...form, [field]: event.target.value })
            }
          />
        )
      }
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
              small
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
