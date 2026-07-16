/** @vitest-environment jsdom */
import {
  act,
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RecordPicker, type RecordPickerCandidate } from "./recordpicker";

// RecordPicker is the extracted debounced search→candidate-list→pick pattern
// that used to live duplicated inline in MergeAction (screens/merge.tsx) and
// AddRelationshipAction (screens/relationships.tsx). These specs pin the
// behaviour those two call sites already relied on: the 250ms debounce, an
// empty term clearing the list instead of searching, an inline (not thrown)
// search error, and a picked candidate reaching onPick.

afterEach(cleanup);

const candidates: RecordPickerCandidate[] = [
  { id: "c-1", name: "Anna Weber" },
  { id: "c-2", name: "Otto Fischer" },
];

describe("RecordPicker", () => {
  it("debounces the typed term and renders the resolved candidates", async () => {
    const searchTargets = vi.fn().mockResolvedValue(candidates);
    rtlRender(
      <RecordPicker
        label="Search…"
        searchTargets={searchTargets}
        onPick={vi.fn()}
      />,
    );

    vi.useFakeTimers();
    try {
      fireEvent.change(screen.getByRole("searchbox"), {
        target: { value: "anna" },
      });
      // Not yet at the debounce boundary — no search fired.
      act(() => {
        vi.advanceTimersByTime(200);
      });
      expect(searchTargets).not.toHaveBeenCalled();
      act(() => {
        vi.advanceTimersByTime(50);
      });
    } finally {
      vi.useRealTimers();
    }

    await waitFor(() => expect(searchTargets).toHaveBeenCalledWith("anna"));
    expect(await screen.findByText("Anna Weber")).toBeTruthy();
    expect(screen.getByText("Otto Fischer")).toBeTruthy();
  });

  it("clears any candidates when the term is emptied instead of searching", async () => {
    const searchTargets = vi.fn().mockResolvedValue(candidates);
    rtlRender(
      <RecordPicker
        label="Search…"
        searchTargets={searchTargets}
        onPick={vi.fn()}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "anna");
    await waitFor(() => expect(screen.getByText("Anna Weber")).toBeTruthy());

    await userEvent.clear(screen.getByRole("searchbox"));
    await waitFor(() => expect(screen.queryByText("Anna Weber")).toBeNull());
    // The clear itself never re-invokes the search transport.
    expect(searchTargets).toHaveBeenCalledTimes(1);
  });

  it("fires onPick with the clicked candidate", async () => {
    const onPick = vi.fn();
    rtlRender(
      <RecordPicker
        label="Search…"
        searchTargets={vi.fn().mockResolvedValue(candidates)}
        onPick={onPick}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "anna");
    await userEvent.click(await screen.findByText("Anna Weber"));

    expect(onPick).toHaveBeenCalledWith({ id: "c-1", name: "Anna Weber" });
  });

  it("marks the selected candidate as pressed", async () => {
    rtlRender(
      <RecordPicker
        label="Search…"
        searchTargets={vi.fn().mockResolvedValue(candidates)}
        onPick={vi.fn()}
        selected={{ id: "c-2", name: "Otto Fischer" }}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "o");
    // Scope to the list option (a button) — the name also appears in the
    // always-visible selection summary below, which is not a button.
    const picked = await screen.findByRole("button", { name: "Otto Fischer" });
    expect(picked.getAttribute("aria-pressed")).toBe("true");
    const notPicked = screen.getByRole("button", { name: "Anna Weber" });
    expect(notPicked.getAttribute("aria-pressed")).toBe("false");
  });

  it("surfaces the current selection even with no active search", async () => {
    // The bug this pins: a picked record left no visible trace once the
    // candidate list was gone (empty term, or a fresh reopen with only a
    // preset `selected`). The selection summary must show regardless.
    rtlRender(
      <RecordPicker
        label="Search…"
        searchTargets={vi.fn().mockResolvedValue(candidates)}
        onPick={vi.fn()}
        selected={{ id: "c-1", name: "Anna Weber" }}
      />,
    );

    // Nothing typed ⇒ no candidate list, but the selection still shows.
    expect(screen.queryByRole("button", { name: "Anna Weber" })).toBeNull();
    expect(screen.getByText("Anna Weber")).toBeTruthy();
  });

  it("clears the search once a candidate is picked", async () => {
    const onPick = vi.fn();
    rtlRender(
      <RecordPicker
        label="Search…"
        searchTargets={vi.fn().mockResolvedValue(candidates)}
        onPick={onPick}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "anna");
    await userEvent.click(
      await screen.findByRole("button", { name: "Anna Weber" }),
    );

    expect(onPick).toHaveBeenCalledWith({ id: "c-1", name: "Anna Weber" });
    // The pick collapses the list and empties the field, so the resolved
    // selection reads cleanly instead of sitting under a stale result set.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Otto Fischer" })).toBeNull(),
    );
    expect((screen.getByRole("searchbox") as HTMLInputElement).value).toBe("");
  });

  it("renders a rejected search inline instead of throwing", async () => {
    const searchTargets = vi.fn().mockRejectedValue(new Error("search down"));
    rtlRender(
      <RecordPicker
        label="Search…"
        searchTargets={searchTargets}
        onPick={vi.fn()}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "anna");

    await waitFor(() => expect(screen.getByText("search down")).toBeTruthy());
    expect(screen.queryByText("Anna Weber")).toBeNull();
  });

  it("discards a stale search when the term changes before it resolves", async () => {
    let resolveFirst: ((value: RecordPickerCandidate[]) => void) | undefined;
    const searchTargets = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<RecordPickerCandidate[]>((resolve) => {
            resolveFirst = resolve;
          }),
      )
      .mockResolvedValueOnce([{ id: "c-2", name: "Otto Fischer" }]);

    rtlRender(
      <RecordPicker
        label="Search…"
        searchTargets={searchTargets}
        onPick={vi.fn()}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "ann");
    await waitFor(() => expect(searchTargets).toHaveBeenCalledTimes(1));

    await userEvent.clear(screen.getByRole("searchbox"));
    await userEvent.type(screen.getByRole("searchbox"), "otto");
    await waitFor(() => expect(searchTargets).toHaveBeenCalledTimes(2));

    // The first (stale) search resolves after the second has already fired —
    // its result must never overwrite the fresher list.
    resolveFirst?.([{ id: "c-1", name: "Anna Weber" }]);
    await waitFor(() => expect(screen.getByText("Otto Fischer")).toBeTruthy());
    expect(screen.queryByText("Anna Weber")).toBeNull();
  });
});
