import { Check } from "lucide-react";
import { useEffect, useState } from "react";
import { Button, SearchField } from "./atoms";

// The shared debounced search→candidate-list→pick pattern: this used to live
// duplicated, near-identically, inline in MergeAction (screens/merge.tsx) and
// AddRelationshipAction (screens/relationships.tsx) — both wire a search
// transport, debounce 250ms, render candidates as pickable buttons, and
// surface a search failure inline rather than throwing it. Neither of those
// two has been migrated onto this extraction yet.
//
// It is now PUBLISHED to the extension tier (surface/design-system.ts), which
// makes this comment something third parties read. Two things it does not do,
// so nobody builds on a promise it does not keep: a stale answer is ignored
// rather than aborted (the request still reaches the server), and the
// candidate list is a list of buttons rather than a combobox — no
// role=combobox, no aria-expanded, no arrow-key navigation.

const SEARCH_DEBOUNCE_MS = 250;

export type RecordPickerCandidate = { id: string; name: string };

export function RecordPicker({
  label,
  searchTargets,
  onPick,
  selected,
  disabled = false,
}: Readonly<{
  // Doubles as the search field's placeholder and aria-label — the caller
  // supplies already-translated copy, so this component never owns i18n.
  label: string;
  searchTargets: (q: string) => Promise<RecordPickerCandidate[]>;
  onPick: (candidate: RecordPickerCandidate) => void;
  selected?: RecordPickerCandidate | null;
  // Inert while the caller's own write for this pick is still in flight, so a
  // second choice cannot race the first. Disabled rather than unmounted: the
  // list and the typed query stay on screen, which is what the user needs to
  // see if the write comes back refused.
  disabled?: boolean;
}>) {
  const [term, setTerm] = useState("");
  const [candidates, setCandidates] = useState<RecordPickerCandidate[]>([]);
  const [searchError, setSearchError] = useState<string | null>(null);

  // A NEW search space empties the list, at once.
  //
  // The effect below cancels the update the previous searchTargets would have
  // made, but cancelling a future answer does nothing about the answers
  // already on screen: they stay rendered, and clickable, until the new search
  // debounces and resolves. A caller that changes searchTargets has changed
  // what it is asking about — notes' filing control does it when the record
  // TYPE changes — so a candidate from the old space is not a wrong-looking
  // answer, it is an answer to a question nobody asked any more, and picking
  // one hands the caller a record of the wrong kind.
  //
  // Done in RENDER rather than in an effect, which is React's own shape for
  // adjusting state when a prop changes: an effect would paint one frame with
  // the stale rows still on screen, and one frame is a click.
  //
  // The TERM survives on purpose: the same words usually mean the same search
  // in the new space, and the effect below re-runs on searchTargets anyway, so
  // the list refills without the person retyping.
  // Both halves take the FUNCTIONAL form because the value is a function:
  // useState(fn) reads fn as a lazy initializer and calls it, and
  // setState(fn) reads it as an updater — so the plain spellings store the
  // RESULT of searching for nothing, which is a promise that never equals the
  // prop, which is an infinite render.
  const [searchSpace, setSearchSpace] = useState(() => searchTargets);
  if (searchSpace !== searchTargets) {
    setSearchSpace(() => searchTargets);
    setCandidates([]);
    setSearchError(null);
  }

  useEffect(() => {
    const query = term.trim();
    if (!query) {
      setCandidates([]);
      setSearchError(null);
      return;
    }
    let cancelled = false;
    const timer = setTimeout(async () => {
      try {
        const results = await searchTargets(query);
        if (!cancelled) {
          setCandidates(results);
          setSearchError(null);
        }
      } catch (error) {
        if (!cancelled) {
          setCandidates([]);
          setSearchError(
            error instanceof Error ? error.message : "request failed",
          );
        }
      }
    }, SEARCH_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [term, searchTargets]);

  return (
    // A named wrapper, because the search field inside it has to FILL it: the
    // `.input-icon` shell is an inline-flex, so it sizes to the input's
    // intrinsic width — about twenty characters — and a placeholder that says
    // what to search truncated mid-word ("Search your Zalo cor…") while every
    // sibling on the card ran full width. Fixed here rather than on `.input-icon`
    // itself, which sits in toolbar rows where shrink-to-fit is what is wanted.
    <div className="recordpicker">
      <SearchField
        placeholder={label}
        aria-label={label}
        value={term}
        disabled={disabled}
        onChange={(event) => setTerm(event.target.value)}
      />
      {searchError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {searchError}
        </p>
      )}
      {/* The current selection stays visible on its own line, independent of
          the search results — a picked record used to vanish the moment its
          candidate list closed, so nothing on screen said what was chosen. */}
      {selected && (
        <p className="recordpicker-selected" aria-live="polite">
          <Check className="recordpicker-selected-check" aria-hidden />
          {selected.name}
        </p>
      )}
      {candidates.length > 0 && (
        <ul className="recordpicker-options">
          {candidates.map((candidate) => (
            <li key={candidate.id}>
              <Button
                className="recordpicker-option"
                aria-pressed={selected?.id === candidate.id}
                disabled={disabled}
                onClick={() => {
                  onPick(candidate);
                  // Collapse the list and empty the field so the resolved
                  // selection reads cleanly instead of sitting under a stale
                  // result set.
                  setTerm("");
                }}
              >
                {candidate.name}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
