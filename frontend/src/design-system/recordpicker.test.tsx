// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

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
    const picked = await screen.findByText("Otto Fischer");
    expect(picked.closest("button")?.getAttribute("aria-pressed")).toBe("true");
    const notPicked = await screen.findByText("Anna Weber");
    expect(notPicked.closest("button")?.getAttribute("aria-pressed")).toBe(
      "false",
    );
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
