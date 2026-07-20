import { expect, it } from "vitest";
import type { AiCallDetail } from "./aiexport";
import { scenarioYaml } from "./aiexport";

it("builds an explicitly unreviewed corpus scaffold with safe block scalars", () => {
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
  } as AiCallDetail;
  const yaml = scenarioYaml(call, "My Run!");
  expect(yaml).toContain("name: my_run");
  expect(yaml).toContain("task: capture_classify");
  expect(yaml).toContain("source: run_export");
  expect(yaml).toContain("sanitized_by: unreviewed");
  expect(yaml).toContain("system: |-\n  line one\n  line two");
  expect(yaml).toContain("input: |-\n  user: hello …[truncated]");
  expect(yaml).toContain("expect:\n  structural: []");
});
