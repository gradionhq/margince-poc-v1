import { useEffect, useId, useRef, useState } from "react";
import { useT } from "../i18n";
import type { ConfidenceLevel, Provenance } from "./trust";
import { ProvenanceTag } from "./trust";
import "./evidencemark.css";

// The ONE provenance affordance (design-language §4).
//
// A field that came from somewhere other than a person typing it carries a
// dotted underline. Opening the mark says where it came from, how sure the
// system was, the text it was read from, and when — with a way through to
// the full history of that field.
//
// It replaces the stack of chips that used to sit under every value
// (provenance tag + confidence meter + evidence chip, three widgets per
// field). Three chips under a value do not read as "this was derived"; they
// read as clutter, and the value they describe gets lost among them. One
// mark on the value itself keeps the record readable and puts the receipts
// one interaction away — the same information, on demand rather than always.

export type EvidenceMarkSource = {
  provenance: Provenance;
  confidence?: ConfidenceLevel;
  snippet?: string | null;
  sourceUrl?: string | null;
  at?: string | null;
};

export function EvidenceMark({
  value,
  source,
  onOpenHistory,
  historyLabel,
}: Readonly<{
  // The rendered value the mark is about. A mark with nothing to explain
  // renders as plain text: an underline that opens an empty popover teaches
  // the reader to stop opening them.
  value: string;
  source?: EvidenceMarkSource;
  onOpenHistory?: () => void;
  historyLabel?: string;
}>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
        // Focus returns to what opened the panel, so Escape does not drop
        // the reader at the top of the document.
        triggerRef.current?.focus();
      }
    };
    const onPointer = (event: MouseEvent) => {
      const target = event.target;
      // A click outside the DOM tree (or on something that is not a node at
      // all) closes the panel: it is certainly not inside it.
      if (!(target instanceof Node)) {
        setOpen(false);
        return;
      }
      if (
        !panelRef.current?.contains(target) &&
        !triggerRef.current?.contains(target)
      ) {
        setOpen(false);
      }
    };
    globalThis.addEventListener("keydown", onKey);
    globalThis.addEventListener("mousedown", onPointer);
    return () => {
      globalThis.removeEventListener("keydown", onKey);
      globalThis.removeEventListener("mousedown", onPointer);
    };
  }, [open]);

  if (!source) {
    return <span>{value}</span>;
  }

  return (
    <span className="evmark">
      <button
        ref={triggerRef}
        type="button"
        className="evmark-trigger"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        aria-label={t("evidence.explain", { value })}
        onClick={() => setOpen((was) => !was)}
      >
        {value}
      </button>
      {open && (
        // A named section rather than a dialog: this is a disclosure beside
        // the value, not a modal — the page behind it stays usable and
        // nothing here traps focus. The accessible name makes it a landmark
        // a screen reader can jump to, and only one is ever open.
        <section
          ref={panelRef}
          id={panelId}
          className="evmark-panel"
          aria-label={t("evidence.explain", { value })}
        >
          <p className="evmark-row">
            <ProvenanceTag provenance={source.provenance} />
            {source.confidence && (
              <span className="evmark-confidence">
                {t(`confidence.${source.confidence}`)}
              </span>
            )}
          </p>
          {source.snippet && (
            <blockquote className="evmark-snippet">{source.snippet}</blockquote>
          )}
          {source.sourceUrl && (
            <p className="evmark-source t-mono">{source.sourceUrl}</p>
          )}
          {source.at && <p className="evmark-at">{source.at}</p>}
          {onOpenHistory && (
            <button
              type="button"
              className="btn btn-ghost btn-sm"
              onClick={() => {
                setOpen(false);
                onOpenHistory();
              }}
            >
              {historyLabel ?? t("evidence.fullHistory")}
            </button>
          )}
        </section>
      )}
    </span>
  );
}
