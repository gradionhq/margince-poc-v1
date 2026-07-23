import { Check, CheckCircle2, GitBranch, Lock, Sparkles } from "lucide-react";
import type { components } from "../api/schema";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";

// The results recap panel: what the funnel actually did, with honest cards
// for anything skipped. The conversational results act renders it as the
// artifact next to the recap turn.

type CompanyProfile = components["schemas"]["CompanyProfile"];

export function ResultsStep({
  voiceBuilt,
  profileSaved,
  profile,
}: Readonly<{
  voiceBuilt: boolean;
  profileSaved: boolean;
  profile?: CompanyProfile;
}>) {
  const t = useT();
  // The cards tell the truth about what the funnel actually did: a skipped
  // voice step gets the honest "starter voice" card, not a claim that drafts
  // already sound like the user — and a profile that was never confirmed is
  // named unsaved, not claimed as captured.
  const cards: { title: MessageKey; body: MessageKey }[] = [
    {
      title: "ob.s3.cardProfile",
      body: profileSaved
        ? "ob.s3.cardProfileBody"
        : "ob.s3.cardProfileSkippedBody",
    },
    {
      title: "ob.s3.cardVoice",
      body: voiceBuilt ? "ob.s3.cardVoiceBody" : "ob.s3.cardVoiceSkippedBody",
    },
    { title: "ob.s3.cardPipeline", body: "ob.s3.cardPipelineBody" },
    {
      title: voiceBuilt ? "ob.s3.cardDraft" : "ob.s3.cardDraftExample",
      body: "ob.s3.cardDraftBody",
    },
  ];
  const understood = [
    { label: t("ob.field.offer_summary"), value: profile?.offer_summary },
    { label: t("ob.field.icp"), value: profile?.icp },
    {
      label: t("ob.field.value_proposition"),
      value: profile?.value_proposition,
    },
    { label: t("ob.field.buying_center"), value: profile?.buying_center },
  ].filter((item): item is { label: string; value: string } =>
    Boolean(item.value),
  );
  return (
    <section className="ob-panel">
      <div className="kick">{t("ob.s3.kick")}</div>
      <h1 className="ttl">
        {t("ob.s3.title")} <span className="em">{t("ob.s3.titleEm")}</span>
      </h1>
      {/* The subtitle claims only what the funnel actually did: "knows your
          voice" is earned by building it, not by reaching this step. */}
      <p className="ob-sub">
        {t(voiceBuilt ? "ob.s3.sub" : "ob.s3.subNoVoice")}
      </p>
      {profile && understood.length > 0 && (
        <div className="understanding-reveal">
          <div className="understanding-brand">
            <span>
              <CheckCircle2 aria-hidden />
            </span>
            <div>
              <small>{t("ob.nowUnderstands")}</small>
              <h2>{profile.display_name}</h2>
            </div>
          </div>
          <div className="understanding-grid">
            {understood.map((item) => (
              <div key={item.label}>
                <small>{item.label}</small>
                <p>{item.value}</p>
              </div>
            ))}
          </div>
          <p className="understanding-note">
            <Sparkles aria-hidden /> {t("ob.contextReady")}
          </p>
        </div>
      )}
      <div className="rcards">
        {cards.map((c) => (
          <div key={c.title} className="rcard">
            <div className="rh">
              <span className="ck">
                <Check aria-hidden />
              </span>
              {t(c.title)}
            </div>
            <p>{t(c.body)}</p>
          </div>
        ))}
      </div>
      <div
        className="omit"
        style={{ marginTop: "var(--space-4)", borderStyle: "solid" }}
      >
        <GitBranch aria-hidden />
        <div>
          <div className="l">{t("ob.s3.originLabel")}</div>
          <p>{t("ob.s3.originBody")}</p>
        </div>
      </div>
      <span className="trustpill" style={{ marginTop: "var(--space-4)" }}>
        <Lock aria-hidden /> {t("ob.s3.stillNothing")}
      </span>
    </section>
  );
}
