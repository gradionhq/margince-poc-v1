// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { FileDropzone } from "./filedropzone";

// The two ways in, and the one way out. A drop zone that only works with a
// mouse silently excludes every keyboard and screen-reader user, so the input
// is the control and the zone is chrome around it — which is also why these
// tests drive both paths.

afterEach(cleanup);

function show(onPick: (file: File) => void, file?: File) {
  return render(
    <LocaleProvider initial="en">
      <FileDropzone
        label="Document"
        hint="Up to 25 MB."
        emptyLabel="Drop the file here, or click to choose one"
        file={file}
        onPick={onPick}
      />
    </LocaleProvider>,
  );
}

const ORDER_FORM = () =>
  new File(["EUR 148,500.00"], "order_form.txt", { type: "text/plain" });

function zone() {
  const found = document.querySelector(".fdz");
  if (!found) {
    throw new Error("the dropzone did not render");
  }
  return found;
}

describe("choosing a file", () => {
  it("takes a file from the picker", async () => {
    const user = userEvent.setup();
    const picked = vi.fn();
    show(picked);

    await user.upload(screen.getByLabelText(/Document/), ORDER_FORM());

    expect(picked).toHaveBeenCalledTimes(1);
    expect(picked.mock.calls[0][0].name).toBe("order_form.txt");
  });

  it("takes a file that was dropped on the zone", () => {
    const picked = vi.fn();
    show(picked);

    fireEvent.drop(zone(), { dataTransfer: { files: [ORDER_FORM()] } });

    expect(picked).toHaveBeenCalledTimes(1);
    expect(picked.mock.calls[0][0].name).toBe("order_form.txt");
  });

  it("admits it will accept the file while one is held over it", () => {
    show(vi.fn());

    expect(zone().className).not.toContain("dragover");
    fireEvent.dragOver(zone(), { dataTransfer: { files: [] } });
    // Without this the page gives no sign it is a target at all, which is the
    // state the old screen-local copy shipped in: the class was toggled and no
    // stylesheet defined it.
    expect(zone().className).toContain("dragover");

    fireEvent.dragLeave(zone());
    expect(zone().className).not.toContain("dragover");
  });

  it("leaves the current choice alone when a drop carries no file", () => {
    const picked = vi.fn();
    show(picked, ORDER_FORM());

    fireEvent.drop(zone(), { dataTransfer: { files: [] } });

    // Cancelling a picker is not the act of clearing a field. Treating it as
    // one would discard a file the reader had already chosen.
    expect(picked).not.toHaveBeenCalled();
    expect(screen.getByText("order_form.txt")).toBeTruthy();
  });

  it("shows the chosen file's name instead of the invitation", () => {
    show(vi.fn(), ORDER_FORM());

    expect(screen.getByText("order_form.txt")).toBeTruthy();
    expect(
      screen.queryByText("Drop the file here, or click to choose one"),
    ).toBeNull();
  });
});
