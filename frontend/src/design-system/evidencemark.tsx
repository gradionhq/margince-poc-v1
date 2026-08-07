import { Sparkles } from "lucide-react";
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

// openMark is the one panel currently showing, across every mark on the
// page. Opening one closes the last, so a keyboard user tabbing along a
// column of marked values leaves a trail of one panel rather than a stack of
// overlapping regions — the same behaviour pointer dismissal gives, made
// true for every input method rather than assumed.
let closeOpenMark: (() => void) | null = null;

export type EvidenceMarkSource = {
  provenance: Provenance;
  confidence?: ConfidenceLevel;
  snippet?: string | null;
  sourceUrl?: string | null;
  at?: string | null;
  // A short phrase naming WHERE the value came from ("Read from the
  // website"), distinct from the ProvenanceTag's WHO-wrote-the-row claim. A
  // person can confirm a site-read value into the row; the tag then reads
  // "human" (true — they wrote it) but the value was still read, not typed,
  // and this line is the only place that survives to say so.
  origin?: string | undefined;
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

  // Registered while this mark is the open one, and cleared on close or
  // unmount so a removed mark never leaves a closer pointing at a component
  // that is gone.
  useEffect(() => {
    if (!open) {
      return;
    }
    const close = () => setOpen(false);
    closeOpenMark?.();
    closeOpenMark = close;
    return () => {
      if (closeOpenMark === close) {
        closeOpenMark = null;
      }
    };
  }, [open]);

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
        {/* The glyph makes a derived value legible at a glance rather than on
            inspection. The underline alone answers "is this derived" only for
            a reader who already suspects it and hovers to check; scanning a
            column of values, nothing stood out to hover. It is the same mark
            everywhere the system speaks, so a reader learns it once. */}
        <Sparkles className="evmark-glyph" aria-hidden size={11} />
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
          {source.origin && <p className="evmark-origin">{source.origin}</p>}
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
