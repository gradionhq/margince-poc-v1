import { useMutation } from "@tanstack/react-query";
import { Upload } from "lucide-react";
import { useRef, useState } from "react";
import { SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";

// The LinkedIn connections import (ADR-0078 §2.1b).
//
// Your own export, not an integration: LinkedIn hands every member a
// Connections.csv under Settings → Data privacy, and this reads it. No app
// approval, no OAuth, nothing to configure.
//
// The copy says what happens to the file, because a user uploading their
// personal address book into a company system deserves to know before they
// click rather than after. The imported rows never become contacts.

type ImportSummary = {
  rows: number;
  imported: number;
  skipped: number;
  confirmed: number;
  suggested: number;
};

function useImportConnections() {
  return useMutation({
    mutationFn: async (file: File): Promise<ImportSummary> => {
      // Sent as multipart by hand rather than through the typed client: the
      // generated client serializes JSON bodies, and this endpoint takes a
      // file part.
      const body = new FormData();
      body.append("file", file);
      const response = await fetch("/v1/me/linkedin-connections", {
        method: "POST",
        body,
        credentials: "include",
      });
      const payload = await response.json().catch(() => undefined);
      if (!response.ok) {
        throw new Error(problemMessage(payload));
      }
      return payload as ImportSummary;
    },
  });
}

export function LinkedInImportCard() {
  const t = useT();
  const input = useRef<HTMLInputElement>(null);
  const [fileName, setFileName] = useState<string | null>(null);
  const importer = useImportConnections();

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader
        title={t("linkedinImport.title")}
        sub={t("linkedinImport.sub")}
      />
      <p className="co-muted" style={{ marginTop: 0 }}>
        {t("linkedinImport.explainer")}
      </p>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: "var(--space-3)",
          flexWrap: "wrap",
        }}
      >
        <input
          ref={input}
          type="file"
          accept=".csv,text/csv"
          data-testid="linkedin-import-file"
          onChange={(e) => {
            const file = e.target.files?.[0];
            setFileName(file?.name ?? null);
            if (file) {
              importer.mutate(file);
            }
          }}
        />
        <Upload size={16} aria-hidden />
        {fileName && <span className="co-muted">{fileName}</span>}
      </div>

      {importer.isPending && (
        <p className="co-muted">{t("linkedinImport.working")}</p>
      )}
      {importer.isError && (
        <p
          role="alert"
          className="co-error"
          data-testid="linkedin-import-error"
        >
          {importer.error.message}
        </p>
      )}
      {importer.isSuccess && <ImportResult summary={importer.data} />}
    </section>
  );
}

// ImportResult states what happened in the terms someone checking the import
// would ask. Skipped rows are shown rather than hidden: a file half-ignored
// under a success message is worse than a refusal.
function ImportResult({ summary }: Readonly<{ summary: ImportSummary }>) {
  const t = useT();
  return (
    <dl data-testid="linkedin-import-result" className="co-facts">
      <dt>{t("linkedinImport.imported")}</dt>
      <dd>{summary.imported}</dd>
      <dt>{t("linkedinImport.confirmed")}</dt>
      <dd>{summary.confirmed}</dd>
      <dt>{t("linkedinImport.suggested")}</dt>
      <dd>{summary.suggested}</dd>
      {summary.skipped > 0 && (
        <>
          <dt>{t("linkedinImport.skipped")}</dt>
          <dd>{summary.skipped}</dd>
        </>
      )}
    </dl>
  );
}
