// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import { webUrl } from "./weburl";

// The guard three surfaces share: a company link on a reading chip, a
// provider's source document in the person drawer, and a custom field holding
// whatever an import wrote. Each draws something different for a refused
// value; none of them may decide the schemes for itself, which is why this
// file is where the scheme list is proven.
describe("what may become a link", () => {
  it("admits the two schemes a web address can be, and normalizes it", () => {
    expect(webUrl("https://wiki.example.com/globex")?.href).toBe(
      "https://wiki.example.com/globex",
    );
    expect(webUrl("http://erp.internal/orders/44")?.href).toBe(
      "http://erp.internal/orders/44",
    );
    // A scheme is matched by what it IS, not how it was typed.
    expect(webUrl("HTTPS://example.com/a")?.protocol).toBe("https:");
  });

  it("refuses a clickable payload, whatever it is dressed as", () => {
    for (const value of [
      "javascript:alert(1)",
      "  javascript:alert(1)",
      "JavaScript:alert(1)",
      "data:text/html;base64,PHNjcmlwdD4=",
      "vbscript:msgbox",
      "file:///etc/passwd",
    ]) {
      expect(webUrl(value), value).toBeNull();
    }
  });

  it("refuses anything that is not an absolute destination", () => {
    // These would resolve against our own origin, which is never where a
    // record's link points — the reader would be sent to a page of ours.
    for (const value of [
      "example.com",
      "www.example.com/pricing",
      "/orders/4",
      "",
      "not a url at all",
    ]) {
      expect(webUrl(value), value).toBeNull();
    }
  });

  it("leaves a scheme that is neither web nor executable as text", () => {
    // `mailto:` is a real destination and still not a link this guard makes:
    // a surface that wants one asks for it deliberately.
    expect(webUrl("mailto:sofia@example.com")).toBeNull();
  });
});
