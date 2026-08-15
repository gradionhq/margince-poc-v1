import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Sparkles, Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Badge, Button, EmptyState, Textarea } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import { problemMessageOf, QueryGate, throwProblem } from "./common";
import { VoiceCorpusIntake } from "./voice-corpus-settings";
import { VOICE_MIN_WORDS } from "./voice-intake-core";
import { useVoiceProfile } from "./voice-profile";
import { ActiveVoiceInsights, VoiceHistory } from "./voice-versions";
import "./voice-dna.css";

type VoiceProfile = components["schemas"]["VoiceProfile"];
type VoiceCorpusSource = components["schemas"]["VoiceCorpusSource"];
type VoiceCorpusSummary = components["schemas"]["VoiceCorpusSummary"];

type CorpusManifest = {
  sources: VoiceCorpusSource[];
  summary: VoiceCorpusSummary;
};

function useVoiceSources(profileId: string) {
  return useQuery({
    queryKey: ["voice-sources", profileId],
    queryFn: async (): Promise<CorpusManifest> => {
      const { data, error } = await api.GET("/voice-profiles/{id}/sources", {
        params: { path: { id: profileId } },
      });
      if (error) {
        throwProblem(error);
      }
      return { sources: data.data, summary: data.summary };
    },
  });
}

// What every mutation on this card does with a failure, spelled once: state it
// in words written for the reader. Keeping the raw failure readable is the
// client's mutation sink's job (app/queryclient.ts), so a save, a removal, an
// add or a build that broke is diagnosable without every mutation here saying
// so for itself.
//
// The parameter is `unknown` rather than react-query's default `Error`
// because what a rejected promise carries is not ours to assume: a thrown
// string reaches here just as a TypeError does, and problemMessageOf takes it
// as it comes.
function reportFailure(
  setError: (message: string) => void,
  t: ReturnType<typeof useT>,
): (error: unknown) => void {
  return (error: unknown) => {
    setError(problemMessageOf(error, t));
  };
}

// The two ADR-0066 maturity thresholds, mirrored so the build control can say
// how far a corpus still has to go. The SERVER decides what state a profile is
// in (VoiceProfile.maturity); these only phrase the distance to the next one.
const VOICE_FIRST_BUILD_WORDS = 800;
const VOICE_FULL_BUILD_WORDS = 4000;

// bandFor mirrors the server's §B1.4 thresholds so the removal warning can
// predict a drop before it happens; the server remains the authority.
function bandFor(totalWords: number): string {
  if (totalWords < 8000) {
    return "thin";
  }
  if (totalWords < 20000) {
    return "good";
  }
  if (totalWords < 30000) {
    return "rich";
  }
  return "sharp";
}

// The "…later in Settings" surface the onboarding Voice step promises: the
// owner's own profile, its corpus, and its builds. A built profile carries five
// subjects, each with its own controls, so each gets a card of its own; a
// profile that does not exist yet is ONE card, because five headings over five
// empty bodies would describe a surface the owner does not have.
export function VoiceDnaCard() {
  const t = useT();
  const canCreate = useCanWrite("voice_profile", "create");
  const qc = useQueryClient();
  const profile = useVoiceProfile();
  return (
    <QueryGate query={profile}>
      {(data) =>
        data ? (
          <VoiceDnaBody profile={data} />
        ) : (
          // The empty state promises samples can be added "below", and a
          // profile is minted by the first add rather than by a step of its
          // own — so the add control has to render here too. Without it an
          // owner who skipped the onboarding voice step could never start a
          // Voice DNA at all, and everything the profile unlocks (corpus,
          // builds, sample drafts) stayed unreachable.
          <Panel title={t("settings.voice.title")}>
            <PanelBody>
              <p className="t-small settings-panel-sub">
                {t("settings.voice.intro")}
              </p>
              <EmptyState>
                <b>{t("settings.voice.emptyTitle")}</b>
                <p className="t-small">{t("settings.voice.emptyBody")}</p>
              </EmptyState>
              {/* The first sample is what MINTS the profile, so the control
                  that adds it asks for the create grant rather than the update
                  one every later sample rides on. Withheld rather than absent:
                  an empty card with no way to start reads as a feature this
                  installation does not have, when the truth is a seat that may
                  not use it. */}
              {canCreate ? (
                <VoiceCorpusIntake
                  first
                  profileId={null}
                  onChanged={() =>
                    qc.invalidateQueries({ queryKey: ["voice-profile"] })
                  }
                />
              ) : (
                <p className="t-small">{t("settings.voice.readOnly")}</p>
              )}
            </PanelBody>
          </Panel>
        )
      }
    </QueryGate>
  );
}

function bandLabel(
  t: ReturnType<typeof useT>,
  band: string | undefined,
): string {
  switch (band) {
    case "thin":
      return t("settings.voice.bandThin");
    case "good":
      return t("settings.voice.bandGood");
    case "rich":
      return t("settings.voice.bandRich");
    case "sharp":
      return t("settings.voice.bandSharp");
    default:
      return band ?? "";
  }
}

function VoiceDnaBody({ profile }: Readonly<{ profile: VoiceProfile }>) {
  const t = useT();
  // useCanWrite, not useCan: every affordance these two children hold issues a
  // mutation, and the seat ceiling has to fold in — a read seat holding the
  // grant would otherwise be offered a write the server clamps.
  const canEdit = useCanWrite("voice_profile", "update");
  const qc = useQueryClient();
  // Intake lives in one card and the build button in another, so the fact that
  // a sample is still arriving has to be held by the parent they share.
  const [intakeBusy, setIntakeBusy] = useState(false);
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["voice-profile"] });
    qc.invalidateQueries({ queryKey: ["voice-sources", profile.id] });
    qc.invalidateQueries({ queryKey: ["voice-versions", profile.id] });
    qc.invalidateQueries({ queryKey: ["voice-deltas", profile.id] });
    qc.invalidateQueries({ queryKey: ["voice-learning", profile.id] });
  };
  return (
    <>
      <Panel title={t("settings.voice.title")}>
        <PanelBody>
          <p className="t-small settings-panel-sub">
            {t("settings.voice.intro")}
          </p>
          {/* Said ONCE, for the whole surface, rather than beside each of the
              five controls a denial disables. The affordances below may then be
              absent without the page making a claim about the data — which is
              the split design-system/README.md draws between a withheld surface
              and a withheld write. */}
          {!canEdit && (
            <p className="t-small vdna-readonly">
              {t("settings.voice.readOnly")}
            </p>
          )}
          <div className="vdna-status">
            <Badge>{t(`settings.voice.status.${profile.status}`)}</Badge>
            {profile.quality_band && (
              <span className="t-small">
                {bandLabel(t, profile.quality_band)}
              </span>
            )}
            <span className="t-small vdna-version">
              {t("settings.voice.version", { n: profile.profile_version ?? 0 })}
            </span>
          </div>

          {/* The insights panel also carries the candidate-review banner, so it
            renders for EVERY profile state: a review-required first build must
            be actionable while the profile is still collecting. It sits with
            the status it can contradict — a card of its own would separate
            "Ready" from the version waiting to replace it. */}
          <ActiveVoiceInsights
            profileId={profile.id}
            canEdit={canEdit}
            onChanged={invalidate}
          />
        </PanelBody>
      </Panel>

      {/* The raw derived text is what a profile can show BEFORE it is ready;
          once it is, the insights panel above quotes the same build back in a
          form a reader can use, and repeating the markdown under it would say
          the same thing twice. */}
      {profile.status !== "ready" && (
        <Panel title={t("settings.voice.derivedLabel")}>
          <PanelBody>
            <DerivedVoice profile={profile} />
          </PanelBody>
        </Panel>
      )}

      <Panel title={t("settings.voice.personalityLabel")}>
        <PanelBody>
          <PersonalityEditor
            profile={profile}
            canEdit={canEdit}
            onSaved={invalidate}
          />
        </PanelBody>
      </Panel>

      <Panel title={t("settings.voice.corpusLabel")}>
        <PanelBody>
          <CorpusManifest
            profileId={profile.id}
            canEdit={canEdit}
            onChanged={invalidate}
          />
          {canEdit && (
            <VoiceCorpusIntake
              profileId={profile.id}
              onChanged={invalidate}
              onBusyChange={setIntakeBusy}
            />
          )}
        </PanelBody>
      </Panel>

      <Panel title={t("settings.voice.buildsTitle")}>
        <PanelBody>
          {/* A build started while a sample is still arriving would describe a
            corpus that no longer exists by the time it finishes. */}
          <BuildControls
            profile={profile}
            canEdit={canEdit}
            onBuilt={invalidate}
            intakeBusy={intakeBusy}
          />
          <VoiceHistory
            profileId={profile.id}
            canEdit={canEdit}
            onChanged={invalidate}
          />
        </PanelBody>
      </Panel>
    </>
  );
}

function DerivedVoice({ profile }: Readonly<{ profile: VoiceProfile }>) {
  const t = useT();
  return profile.voice_profile_md ? (
    <p className="vdna-derived">{profile.voice_profile_md}</p>
  ) : (
    <p className="t-small">{t("settings.voice.derivedEmpty")}</p>
  );
}

// personality_md is the owner-authored preferences the model output never
// overwrites; the PATCH is version-guarded (If-Match on the profile version).
function PersonalityEditor({
  profile,
  canEdit,
  onSaved,
}: Readonly<{
  profile: VoiceProfile;
  canEdit: boolean;
  onSaved: () => void;
}>) {
  const t = useT();
  const [text, setText] = useState(profile.personality_md);
  const [error, setError] = useState<string | null>(null);
  const save = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.PATCH("/voice-profiles/{id}", {
        params: {
          path: { id: profile.id },
          header: { "If-Match": String(profile.version) },
        },
        body: { personality_md: text },
      });
      if (err) {
        throwProblem(err);
      }
    },
    onSuccess: () => {
      setError(null);
      onSaved();
    },
    onError: reportFailure(setError, t),
  });
  const dirty = text !== profile.personality_md;
  return (
    <div className="vdna-composer">
      {/* readOnly rather than disabled: the preferences are a READ this seat
          still holds, and a disabled textarea drops out of the tab order, so a
          keyboard reader could not reach the words to read them. */}
      <Textarea
        rows={4}
        value={text}
        readOnly={!canEdit}
        aria-label={t("settings.voice.personalityLabel")}
        placeholder={t("settings.voice.personalityPlaceholder")}
        onChange={(e) => setText(e.target.value)}
      />
      {canEdit && (
        <div className="vdna-composer-actions">
          <Button
            small
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate()}
          >
            {t("settings.voice.savePreferences")}
          </Button>
          {error && (
            <span className="t-small" role="alert">
              {error}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

// The corpus a profile already holds: the meter, its register mix, and the
// removable rows. Only a caller that HAS a profile renders it — before that
// there is no corpus to read, and asking for one would be a request against an
// id nobody has minted yet.
function CorpusManifest({
  profileId,
  canEdit,
  onChanged,
}: Readonly<{
  profileId: string;
  canEdit: boolean;
  onChanged: () => void;
}>) {
  const t = useT();
  const sources = useVoiceSources(profileId);
  const [error, setError] = useState<string | null>(null);

  const remove = useMutation({
    mutationFn: async (sourceId: string) => {
      const { error: err } = await api.DELETE(
        "/voice-profiles/{id}/sources/{sourceId}",
        { params: { path: { id: profileId, sourceId } } },
      );
      if (err) {
        throwProblem(err);
      }
    },
    onSuccess: () => {
      setError(null);
      onChanged();
    },
    onError: reportFailure(setError, t),
  });

  return (
    <>
      <QueryGate query={sources}>
        {(manifest) => (
          <div>
            <p className="t-small">
              {t("settings.voice.meter", {
                count: manifest.summary.total_words.toLocaleString(),
                target: manifest.summary.target_words.toLocaleString(),
              })}
            </p>
            {/* The meter above tracks the 30,000-word quality target, which
                says nothing about when a first build becomes possible. Below
                the floor, the distance that actually matters is the 800. */}
            {manifest.summary.total_words < VOICE_MIN_WORDS && (
              <FloorMeter words={manifest.summary.total_words} />
            )}
            <RegisterMix summary={manifest.summary} />
            {manifest.sources.length === 0 ? (
              <p className="t-small">{t("settings.voice.corpusEmpty")}</p>
            ) : (
              <ul className="vdna-list">
                {manifest.sources.map((s) => (
                  <SourceRow
                    canEdit={canEdit}
                    key={s.id}
                    source={s}
                    summary={manifest.summary}
                    pending={remove.isPending}
                    onRemove={() => remove.mutate(s.id)}
                  />
                ))}
              </ul>
            )}
          </div>
        )}
      </QueryGate>
      {error && (
        <p className="t-small" style={{ marginTop: "var(--space-2)" }}>
          {error}
        </p>
      )}
    </>
  );
}

// How far the corpus still is from the server's first-build floor. It renders
// only below the floor: once a build is possible, the distance to it is no
// longer the question the reader is asking.
function FloorMeter({ words }: Readonly<{ words: number }>) {
  const t = useT();
  return (
    <p className="t-small vdna-floor">
      <progress
        value={Math.min(words, VOICE_MIN_WORDS)}
        max={VOICE_MIN_WORDS}
        aria-label={t("settings.voice.floorLabel", { min: VOICE_MIN_WORDS })}
      />
      <span>
        {t("settings.voice.floorProgress", {
          words: words.toLocaleString(),
          min: VOICE_MIN_WORDS.toLocaleString(),
        })}
      </span>
    </p>
  );
}

// registerLabel names one closed-vocabulary register; an unknown value (a
// newer server) renders verbatim rather than crashing the card.
function registerLabel(t: ReturnType<typeof useT>, register: string): string {
  switch (register) {
    case "email":
      return t("settings.voice.register.email");
    case "social":
      return t("settings.voice.register.social");
    case "long_form":
      return t("settings.voice.register.long_form");
    case "spoken":
      return t("settings.voice.register.spoken");
    case "general":
      return t("settings.voice.register.general");
    default:
      return register;
  }
}

// RegisterMix shows where the corpus words come from; spoken sources are the
// highest-signal gap to name.
function RegisterMix({ summary }: Readonly<{ summary: VoiceCorpusSummary }>) {
  const t = useT();
  const entries = Object.entries(summary.register_words).filter(
    ([, words]) => words > 0,
  );
  if (entries.length === 0 || summary.total_words === 0) {
    return null;
  }
  return (
    <p className="t-small vdna-regmix">
      {entries
        .map(
          ([register, words]) =>
            `${registerLabel(t, register)} ${Math.round((words / summary.total_words) * 100)}%`,
        )
        .join(" · ")}
    </p>
  );
}

// Removing a source is armed-then-confirmed when it would drop the quality
// band: the warning names the drop before anything is deleted.
function SourceRow({
  source,
  canEdit,
  summary,
  pending,
  onRemove,
}: Readonly<{
  canEdit: boolean;
  source: VoiceCorpusSource;
  summary: VoiceCorpusSummary;
  pending: boolean;
  onRemove: () => void;
}>) {
  const t = useT();
  const [armed, setArmed] = useState(false);
  const bandAfter = bandFor(
    Math.max(0, summary.total_words - source.word_count),
  );
  const drops = source.included && bandAfter !== summary.quality_band;
  const handleRemove = () => {
    if (drops && !armed) {
      setArmed(true);
      return;
    }
    onRemove();
  };
  return (
    <li className="vdna-row">
      <span>
        {source.source_label} · {source.word_count.toLocaleString()}
        <span className="vdna-register">
          {registerLabel(t, source.register)}
        </span>
        {!source.included && ` · ${t("settings.voice.excluded")}`}
      </span>
      {armed && drops && (
        <span className="t-small vdna-banddrop" role="alert">
          {t("settings.voice.bandDrop", {
            from: bandLabel(t, summary.quality_band),
            to: bandLabel(t, bandAfter),
          })}
        </span>
      )}
      {canEdit && (
        <button
          type="button"
          className="iconbtn vdna-row-verb"
          aria-label={t("settings.voice.removeSource")}
          disabled={pending}
          onClick={handleRemove}
        >
          <Trash2 aria-hidden />
        </button>
      )}
    </li>
  );
}

// Build creates a durable background build; poll to a terminal state. A slow or
// budget-deferred build is honestly reported, not spun on forever.
function BuildControls({
  profile,
  canEdit,
  onBuilt,
  intakeBusy = false,
}: Readonly<{
  canEdit: boolean;
  profile: VoiceProfile;
  onBuilt: () => void;
  intakeBusy?: boolean;
}>) {
  const t = useT();
  const [status, setStatus] = useState<
    "succeeded" | "failed" | "deferred" | "pending" | null
  >(null);
  const [error, setError] = useState<string | null>(null);

  const build = useMutation({
    mutationFn: async (): Promise<
      "succeeded" | "failed" | "deferred" | "pending"
    > => {
      const created = await api.POST("/voice-profiles/{id}/builds", {
        params: { path: { id: profile.id } },
        body: { reason: "manual" },
      });
      if (created.error) {
        throwProblem(created.error);
      }
      const buildId = created.data.id;
      for (let attempt = 0; attempt < 40; attempt++) {
        const { data, error: err } = await api.GET(
          "/voice-profiles/{id}/builds/{buildId}",
          { params: { path: { id: profile.id, buildId } } },
        );
        if (err) {
          throwProblem(err);
        }
        if (
          data.status === "succeeded" ||
          data.status === "failed" ||
          data.status === "deferred"
        ) {
          return data.status;
        }
        await new Promise((resolve) => {
          globalThis.setTimeout(resolve, 1500);
        });
      }
      // Still queued/running after the poll budget — honestly "pending", not
      // "deferred" (which specifically means the AI budget snoozed it).
      return "pending";
    },
    onSuccess: (finalStatus) => {
      setStatus(finalStatus);
      setError(null);
      onBuilt();
    },
    onError: reportFailure(setError, t),
  });

  // The corpus summary rides the same query key CorpusManifest already read,
  // so asking for the word total here costs no extra request.
  const corpus = useVoiceSources(profile.id);
  // maturity is the SERVER's verdict on whether a build can say anything, so
  // it — not a locally recomputed threshold — decides whether the button is
  // offered. The word counts below only phrase the distance to the next state.
  const tooThin = profile.maturity === "collecting";
  // The distance is quoted only from a corpus total that actually loaded. A
  // failed fetch would otherwise read as zero words and announce a confident
  // "about 800 more words" whose real cause was the failure — the button's
  // state still follows maturity, which comes from a different request.
  const words = corpus.isSuccess ? corpus.data.summary.total_words : null;
  const blocked =
    tooThin && words !== null
      ? t("settings.voice.buildNeedsWords", {
          n: Math.max(0, VOICE_FIRST_BUILD_WORDS - words),
        })
      : null;
  const reach =
    profile.maturity === "provisional" && words !== null
      ? t("settings.voice.buildProvisional", {
          n: Math.max(0, VOICE_FULL_BUILD_WORDS - words),
        })
      : null;

  return (
    <div className="vdna-composer">
      {/* The title rides the wrapper, not the button: a disabled control fires
          no pointer events, so a tooltip on it would never appear at the exact
          moment someone is asking why they cannot click. */}
      {canEdit && (
        <span className="vdna-build" title={blocked ?? undefined}>
          <Button
            variant="primary"
            small
            disabled={build.isPending || tooThin || intakeBusy}
            onClick={() => build.mutate()}
          >
            <Sparkles aria-hidden />{" "}
            {build.isPending
              ? t("settings.voice.building")
              : t("settings.voice.rebuild")}
          </Button>
        </span>
      )}
      {(blocked ?? reach) && <p className="t-small">{blocked ?? reach}</p>}
      {/* Mounted whether or not there is an outcome yet. A build runs for about
          a minute behind a poll, so the reader who started it has looked away —
          and a live region inserted together with its text is not reliably
          announced, which would leave the one thing they are waiting for
          arriving in silence. */}
      <p className="t-small" role="status">
        {status ? t(`settings.voice.buildStatus.${status}`) : ""}
      </p>
      {error && (
        <p className="t-small" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
