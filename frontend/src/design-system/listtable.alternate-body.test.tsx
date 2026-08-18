/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { ListTable } from "./listtable";

afterEach(cleanup);

describe("ListTable alternate body paging ownership", () => {
  it("keeps shared controls but suppresses the table count and pager", () => {
    render(
      <LocaleProvider initial="en">
        <ListTable
          rows={[{ id: "lead-1" }]}
          columns={[
            {
              key: "id",
              header: "Name",
              cell: (row) => row.id,
              fixed: true,
            },
          ]}
          rowKey={(row) => row.id}
          unit="rows"
          search={{ value: "", onChange: () => undefined }}
          body={<div>Complete board</div>}
          bodyOwnsPaging
          hasMore
        />
      </LocaleProvider>,
    );

    expect(screen.getByRole("searchbox")).toBeTruthy();
    expect(screen.getByText("Complete board")).toBeTruthy();
    expect(screen.queryByText(/of 1 rows/)).toBeNull();
    expect(screen.queryByRole("navigation", { name: "Pages" })).toBeNull();
  });
});
