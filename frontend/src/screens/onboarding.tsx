import { useMutation } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import { navigate } from "../app/router";
import { Button, SectionHeader, TextInput } from "../design-system/atoms";
import { ConfidenceMeter, EvidenceChip } from "../design-system/trust";
import { useT } from "../i18n";
import { problemMessage } from "./common";
import { confidenceLevel } from "./inbox";

// Onboarding / cold-start wizard (B-EP09.9): rail-less, five steps —
// Read · Confirm · Voice · Results · Connect. Mailbox connect is the LAST
// step, never first (FD-13: value before permission). The read step drives
// the real /coldstart read-back: every returned field carries evidence +
// confidence or it wasn't returned at all (the server's no-guess gate);
// a failed read renders the honest "couldn't read enough" state with
// retry / paste-manually paths, never fabricated firmographics.

const STEPS = ["read", "confirm", "voice", "results", "connect"] as const;
type Step = (typeof STEPS)[number];

export function OnboardingScreen() {
  const t = useT();
  const [step, setStep] = useState<Step>("read");
  const [url, setUrl] = useState("");
  const [voice, setVoice] = useState("");
  const urlId = useId();
  const voiceId = useId();

  const read = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/coldstart", {
        body: { url },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => setStep("confirm"),
  });

  const stepLabel: Record<Step, string> = {
    read: t("ob.read"),
    confirm: t("ob.confirm"),
    voice: t("ob.voice"),
    results: t("ob.results"),
    connect: t("ob.connect"),
  };

  return (
    <div className="wrap narrow ob-top">
      <SectionHeader title={t("ob.title")} sub={t("ob.sub")} />
      <nav className="stepper" aria-label={t("ob.title")}>
        {STEPS.map((candidate) => (
          <span
            key={candidate}
            className={candidate === step ? "step current" : "step"}
            aria-current={candidate === step ? "step" : undefined}
          >
            {stepLabel[candidate]}
          </span>
        ))}
      </nav>

      {step === "read" && (
        <section className="card">
          <SectionHeader title={t("ob.read")} sub={t("ob.readSub")} />
          <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
            <span className="t-label" id={urlId}>
              {t("ob.url")}
            </span>
            <TextInput
              aria-labelledby={urlId}
              value={url}
              onChange={(event) => setUrl(event.target.value)}
              style={{ flex: 1 }}
            />
            <Button
              variant="primary"
              disabled={url.trim() === "" || read.isPending}
              onClick={() => read.mutate()}
            >
              {t("ob.readGo")}
            </Button>
          </div>
          {read.isError && (
            <div className="card card-inset" style={{ marginTop: 10 }}>
              <p className="t-label">{t("ob.readFailed")}</p>
              <p className="t-caption" style={{ marginTop: 4 }}>
                {read.error instanceof Error ? read.error.message : null}
              </p>
              <div style={{ display: "flex", gap: 8, marginTop: 10 }}>
                <Button small onClick={() => read.mutate()}>
                  {t("common.retry")}
                </Button>
                <Button small onClick={() => setStep("confirm")}>
                  {t("ob.manualInstead")}
                </Button>
              </div>
            </div>
          )}
        </section>
      )}

      {step === "confirm" && (
        <section className="card">
          <SectionHeader title={t("ob.confirm")} sub={t("ob.confirmSub")} />
          {read.data ? (
            <ul
              style={{
                listStyle: "none",
                display: "flex",
                flexDirection: "column",
                gap: 10,
              }}
            >
              {read.data.fields.map((field) => {
                const level = confidenceLevel(field.confidence);
                return (
                  <li key={field.field} className="staging-card">
                    <div
                      style={{ display: "flex", gap: 8, alignItems: "center" }}
                    >
                      <span className="t-label">{field.field}</span>
                      {level && <ConfidenceMeter level={level} />}
                    </div>
                    <p style={{ marginTop: 6 }}>{field.value}</p>
                    <EvidenceChip
                      evidence={{
                        snippet: field.evidence_snippet,
                        source: field.source_url,
                      }}
                    />
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="t-caption">{t("ob.nothingRead")}</p>
          )}
          <p className="t-caption" style={{ marginTop: 10 }}>
            {t("ob.acceptInInbox")}
          </p>
          <Button
            variant="primary"
            style={{ marginTop: 10 }}
            onClick={() => setStep("voice")}
          >
            {t("ob.next")}
          </Button>
        </section>
      )}

      {step === "voice" && (
        <section className="card">
          <SectionHeader title={t("ob.voice")} sub={t("ob.voiceSub")} />
          <span className="t-label" id={voiceId}>
            {t("ob.voiceLabel")}
          </span>
          <textarea
            className="textarea"
            aria-labelledby={voiceId}
            rows={4}
            value={voice}
            onChange={(event) => setVoice(event.target.value)}
            style={{ width: "100%", marginTop: 6 }}
          />
          <Button
            variant="primary"
            style={{ marginTop: 10 }}
            onClick={() => setStep("results")}
          >
            {t("ob.next")}
          </Button>
        </section>
      )}

      {step === "results" && (
        <section className="card">
          <SectionHeader title={t("ob.results")} sub={t("ob.resultsSub")} />
          <Button variant="primary" onClick={() => setStep("connect")}>
            {t("ob.next")}
          </Button>
        </section>
      )}

      {step === "connect" && (
        <section className="card">
          <SectionHeader title={t("ob.connect")} sub={t("ob.connectSub")} />
          <p className="t-caption">{t("ob.connectDetail")}</p>
          <Button
            variant="primary"
            style={{ marginTop: 10 }}
            onClick={() => navigate({ screen: "home" })}
          >
            {t("ob.finish")}
          </Button>
        </section>
      )}
    </div>
  );
}
