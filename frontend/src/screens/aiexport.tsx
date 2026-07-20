import { useState } from "react";
import type { components } from "../api/schema";
import { Badge, Button, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";

export type AiCallDetail = Pick<
  components["schemas"]["AiCall"],
  "task" | "occurred_at" | "payload"
>;

type CapturedRequest = {
  system?: string;
  messages?: { role: string; content: string }[];
};

function capturedRequest(value: unknown): CapturedRequest {
  if (typeof value !== "object" || value === null) return {};
  const system =
    "system" in value && typeof value.system === "string"
      ? value.system
      : undefined;
  const messages: CapturedRequest["messages"] = [];
  if ("messages" in value && Array.isArray(value.messages)) {
    for (const message of value.messages) {
      if (
        typeof message === "object" &&
        message !== null &&
        "role" in message &&
        typeof message.role === "string" &&
        "content" in message &&
        typeof message.content === "string"
      ) {
        messages.push({ role: message.role, content: message.content });
      }
    }
  }
  return { system, messages };
}

function blockScalar(text: string): string {
  const body = text
    .split("\n")
    .map((line) => `  ${line}`)
    .join("\n");
  return `|-\n${body}`;
}

function scenarioSlug(name: string): string {
  return name
    .toLowerCase()
    .replaceAll(/[^a-z0-9]+/g, "_")
    .replaceAll(/^_+|_+$/g, "");
}

export function scenarioYaml(call: AiCallDetail, name: string): string {
  const request = capturedRequest(call.payload?.request);
  const input = (request.messages ?? [])
    .map((message) => `${message.role}: ${message.content}`)
    .join("\n");
  return [
    `name: ${scenarioSlug(name)}`,
    `task: ${call.task}`,
    "source: run_export",
    "sanitized_by: unreviewed",
    `system: ${blockScalar(request.system ?? "")}`,
    `input: ${blockScalar(input)}`,
    "expect:",
    "  structural: []",
    "",
  ].join("\n");
}

export function ExportScenarioDialog({
  call,
  onClose,
}: Readonly<{ call: AiCallDetail; onClose: () => void }>) {
  const t = useT();
  const defaultName = `${call.task}_run_${call.occurred_at.slice(0, 10).replaceAll("-", "")}`;
  const [name, setName] = useState(defaultName);
  const [copied, setCopied] = useState(false);
  const yaml = scenarioYaml(call, name);

  async function copyYaml() {
    if (!navigator.clipboard) return;
    await navigator.clipboard.writeText(yaml);
    setCopied(true);
  }

  function downloadYaml() {
    const url = URL.createObjectURL(
      new Blob([yaml], { type: "application/yaml" }),
    );
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `${scenarioSlug(name)}.yaml`;
    anchor.click();
    URL.revokeObjectURL(url);
  }

  return (
    <dialog open aria-label={t("aiexport.title")}>
      <h2>{t("aiexport.title")}</h2>
      <label htmlFor="cert-scenario-name">
        {t("aiexport.nameLabel")}
        <TextInput
          id="cert-scenario-name"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </label>
      <p>
        <Badge tone="warn">{t("aiexport.checklist")}</Badge>
      </p>
      <pre className="t-mono" style={{ maxHeight: 320, overflow: "auto" }}>
        {yaml}
      </pre>
      <pre className="t-mono" style={{ maxHeight: 180, overflow: "auto" }}>
        {JSON.stringify(call.payload?.response, null, 2)}
      </pre>
      <div style={{ display: "flex", gap: "var(--space-2)" }}>
        <Button onClick={() => void copyYaml()}>
          {copied ? t("aiexport.copied") : t("aiexport.copy")}
        </Button>
        <Button onClick={downloadYaml}>{t("aiexport.download")}</Button>
        <Button onClick={onClose}>×</Button>
      </div>
    </dialog>
  );
}
