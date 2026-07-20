/** @vitest-environment jsdom */
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import type { AiCallDetail } from "./aiexport";
import { ExportScenarioDialog, scenarioYaml } from "./aiexport";

const call = {
  task: "capture_classify",
  occurred_at: "2026-07-20T10:00:00Z",
  payload: {
    request: {
      system: "line one\nline two",
      messages: [{ role: "user", content: "hello …[truncated]" }],
    },
    response: "ok",
  },
} satisfies AiCallDetail;

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(URL, "createObjectURL");
  Reflect.deleteProperty(URL, "revokeObjectURL");
});

it("builds an explicitly unreviewed corpus scaffold with safe block scalars", () => {
  const yaml = scenarioYaml(call, "My Run!");
  expect(yaml).toContain("name: my_run");
  expect(yaml).toContain("task: capture_classify");
  expect(yaml).toContain("source: run_export");
  expect(yaml).toContain("sanitized_by: unreviewed");
  expect(yaml).toContain("system: |-\n  line one\n  line two");
  expect(yaml).toContain("input: |-\n  user: hello …[truncated]");
  expect(yaml).toContain("expect:\n  structural: []");
});

it("requires PII acknowledgment before copying or downloading", async () => {
  const writeText = vi.fn(async () => undefined);
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText },
  });
  const createObjectURL = vi.fn(() => "blob:test");
  const revokeObjectURL = vi.fn(() => undefined);
  Object.defineProperties(URL, {
    createObjectURL: { configurable: true, value: createObjectURL },
    revokeObjectURL: { configurable: true, value: revokeObjectURL },
  });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(
    () => undefined,
  );

  render(
    <LocaleProvider initial="en">
      <ExportScenarioDialog call={call} onClose={() => undefined} />
    </LocaleProvider>,
  );
  const copy = screen.getByRole("button", { name: "Copy YAML" });
  const download = screen.getByRole("button", { name: "Download .yaml" });
  expect((copy as HTMLButtonElement).disabled).toBe(true);
  expect((download as HTMLButtonElement).disabled).toBe(true);

  await userEvent.click(screen.getByRole("checkbox"));
  await userEvent.click(copy);
  await waitFor(() => expect(writeText).toHaveBeenCalledOnce());
  await userEvent.click(download);
  expect(createObjectURL).toHaveBeenCalledOnce();
  expect(revokeObjectURL).toHaveBeenCalledWith("blob:test");
});

it("surfaces clipboard rejection", async () => {
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: {
      writeText: vi.fn(async () => Promise.reject(new Error("denied"))),
    },
  });
  render(
    <LocaleProvider initial="en">
      <ExportScenarioDialog call={call} onClose={() => undefined} />
    </LocaleProvider>,
  );
  await userEvent.click(screen.getByRole("checkbox"));
  await userEvent.click(screen.getByRole("button", { name: "Copy YAML" }));
  expect(
    await screen.findByText(
      "Copy failed — use the preview or download instead.",
    ),
  ).toBeTruthy();
});
