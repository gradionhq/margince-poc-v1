/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { type CreateField, type FormRows, RecordFormBody } from "./create";

// Repeatable-row fields (P-15 foundation): emails/phones/domains as
// {value,type,is_primary} rows, threaded through the shared create/edit form
// via the ripple-free `rows`/`setRows` second channel — scalar `values` stays
// untouched so every existing screen's create callback keeps working.

afterEach(() => {
  cleanup();
});

function render(ui: ReactNode) {
  return rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

const emailsField: CreateField = {
  key: "emails",
  label: "create.email",
  type: "repeatable",
  addLabel: "field.addEmail",
  rowFields: [
    { key: "email", label: "create.email", type: "email", required: true },
    {
      key: "email_type",
      label: "field.emailType",
      type: "select",
      options: [
        { value: "work", label: "Work" },
        { value: "personal", label: "Personal" },
      ],
    },
  ],
  primaryKey: "is_primary",
};

function Harness({
  fields,
  onSubmit,
}: Readonly<{
  fields: CreateField[];
  onSubmit: (values: Record<string, string>, rows?: FormRows) => void;
}>) {
  const [values, setValues] = useState<Record<string, string>>({});
  const [rows, setRows] = useState<FormRows>({});
  return (
    <RecordFormBody
      fields={fields}
      values={values}
      setValues={setValues}
      rows={rows}
      setRows={setRows}
      pending={false}
      error={null}
      onSubmit={onSubmit}
      onClose={vi.fn()}
      submitLabelKey="create.save"
    />
  );
}

describe("repeatable-row fields", () => {
  it("adds a blank row when Add is clicked", async () => {
    render(<Harness fields={[emailsField]} onSubmit={vi.fn()} />);
    expect(screen.queryByLabelText("Email")).toBeNull();
    await userEvent.click(screen.getByText("Add email"));
    expect(screen.getByLabelText("Email *")).toBeTruthy();
    expect(screen.getByLabelText("Type")).toBeTruthy();
  });

  it("updates a row's values as the user types and selects", async () => {
    render(<Harness fields={[emailsField]} onSubmit={vi.fn()} />);
    await userEvent.click(screen.getByText("Add email"));
    await userEvent.type(screen.getByLabelText("Email *"), "a@x.test");
    await userEvent.selectOptions(screen.getByLabelText("Type"), "work");
    expect((screen.getByLabelText("Email *") as HTMLInputElement).value).toBe(
      "a@x.test",
    );
    expect((screen.getByLabelText("Type") as HTMLSelectElement).value).toBe(
      "work",
    );
  });

  it("marks exactly one row primary", async () => {
    render(<Harness fields={[emailsField]} onSubmit={vi.fn()} />);
    await userEvent.click(screen.getByText("Add email"));
    await userEvent.click(screen.getByText("Add email"));
    const radios = screen.getAllByRole("radio", {
      name: "Primary",
    }) as HTMLInputElement[];
    expect(radios).toHaveLength(2);
    await userEvent.click(radios[0]);
    expect(radios[0].checked).toBe(true);
    expect(radios[1].checked).toBe(false);
    await userEvent.click(radios[1]);
    expect(radios[0].checked).toBe(false);
    expect(radios[1].checked).toBe(true);
  });

  it("removes a row", async () => {
    render(<Harness fields={[emailsField]} onSubmit={vi.fn()} />);
    await userEvent.click(screen.getByText("Add email"));
    await userEvent.click(screen.getByText("Add email"));
    expect(screen.getAllByLabelText("Email *")).toHaveLength(2);
    await userEvent.click(screen.getAllByText("Remove")[0]);
    expect(screen.getAllByLabelText("Email *")).toHaveLength(1);
  });

  it("collects the rows for submission", async () => {
    const onSubmit = vi.fn();
    render(<Harness fields={[emailsField]} onSubmit={onSubmit} />);
    await userEvent.click(screen.getByText("Add email"));
    await userEvent.type(screen.getByLabelText("Email *"), "a@x.test");
    await userEvent.selectOptions(screen.getByLabelText("Type"), "work");
    await userEvent.click(screen.getByRole("radio", { name: "Primary" }));
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    expect(onSubmit).toHaveBeenCalledWith(
      {},
      {
        emails: [{ email: "a@x.test", email_type: "work", is_primary: "true" }],
      },
    );
  });
});
