import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { createdColumn, ownerColumn, standardViews } from "./recordlist";

const here = dirname(fileURLToPath(import.meta.url));

// The record lists that share the Owner/Created columns and the standard
// views. A fourth owner-scoped list joins this array, not the copy-paste.
const RECORD_LISTS = ["people.tsx", "organizations.tsx", "leads.tsx"];

describe("one record list, three record types", () => {
  it("every record list takes Owner and Created from recordlist.tsx, never its own copy", () => {
    // Three lists carried three copies of the Owner column, and a fix on two
    // of them missed the third. A screen that spells its own
    // `key: "owner"` reintroduces exactly that.
    for (const file of RECORD_LISTS) {
      const source = readFileSync(join(here, file), "utf8");
      expect(source, `${file} imports the shared columns`).toMatch(
        /from "\.\/recordlist"/,
      );
      expect(source, `${file} defines its own owner column`).not.toMatch(
        /key:\s*"owner"/,
      );
      expect(source, `${file} defines its own created column`).not.toMatch(
        /key:\s*"created"/,
      );
      expect(source, `${file} spells its own Mine view`).not.toMatch(
        /label:\s*"list\.viewMine"/,
      );
    }
  });

  it("the standard views are All, and Mine only for a signed-in reader", () => {
    const t = ((key: string) => key) as Parameters<typeof ownerColumn>[0];
    expect(ownerColumn(t).sort).toBe("owner_id");
    expect(createdColumn(t, "en").sort).toBe("created_at");
    expect(standardViews(undefined).map((v) => v.label)).toEqual([
      "list.viewAll",
    ]);
    expect(standardViews("u-1").map((v) => v.label)).toEqual([
      "list.viewAll",
      "list.viewMine",
    ]);
    expect(standardViews("u-1")[1]?.filters).toEqual({ owner_id: "u-1" });
  });
});
