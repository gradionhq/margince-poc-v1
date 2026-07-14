import { useEffect, useState } from "react";
import { SearchField } from "./atoms";

// The shared debounced search→candidate-list→pick pattern: this used to
// live duplicated, near-identically, inline in MergeAction
// (screens/merge.tsx) and AddRelationshipAction (screens/relationships.tsx)
// — both wire a search transport, debounce 250ms, render candidates as
// pickable buttons, and surface a search failure inline rather than
// throwing it. Neither call site has been migrated onto this extraction
// yet; offers.tsx (buyer-org and product pickers) is its only consumer
// today.

const SEARCH_DEBOUNCE_MS = 250;

export type RecordPickerCandidate = { id: string; name: string };

export function RecordPicker({
  label,
  searchTargets,
  onPick,
  selected,
}: Readonly<{
  // Doubles as the search field's placeholder and aria-label — the caller
  // supplies already-translated copy, so this component never owns i18n.
  label: string;
  searchTargets: (q: string) => Promise<RecordPickerCandidate[]>;
  onPick: (candidate: RecordPickerCandidate) => void;
  selected?: RecordPickerCandidate | null;
}>) {
  const [term, setTerm] = useState("");
  const [candidates, setCandidates] = useState<RecordPickerCandidate[]>([]);
  const [searchError, setSearchError] = useState<string | null>(null);

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
    <div>
      <SearchField
        placeholder={label}
        aria-label={label}
        value={term}
        onChange={(event) => setTerm(event.target.value)}
      />
      {searchError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {searchError}
        </p>
      )}
      <ul style={{ listStyle: "none", margin: "8px 0", padding: 0 }}>
        {candidates.map((candidate) => (
          <li key={candidate.id}>
            <button
              type="button"
              className="btn btn-ghost"
              aria-pressed={selected?.id === candidate.id}
              onClick={() => onPick(candidate)}
              style={{ width: "100%", textAlign: "left" }}
            >
              {candidate.name}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
