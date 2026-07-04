import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, test, expect } from "vitest";
import { BulkDeleteTagsDialog } from "../bulk-delete-tags-dialog";

// Bug 2 — single-tag delete confirms with the TAG NAME, not the count.
// Asking the operator to type "1" to confirm a one-tag delete was a bad
// hand-off: the eye never lands on what's about to disappear. Typing the
// tag name forces a moment of attention. Multi-tag stays as count typing.
//
// The dialog now delegates to the shared ConfirmDestructiveDialog primitive,
// whose confirm label reads "Type <expected> to confirm" (the primitive
// doesn't render the old "the tag name" phrasing). The gate STRENGTH is what
// matters and is unchanged: single ⇒ type the tag name, multi ⇒ type the
// count. We assert on the label→input association (getByLabelText) so the
// exact wording of the primitive isn't load-bearing.

function renderWithClient(children: React.ReactNode): void {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={qc}>{children}</QueryClientProvider>);
}

describe("BulkDeleteTagsDialog confirm gate", () => {
  test("single-tag delete asks for the tag name (not the count)", async () => {
    const user = userEvent.setup();
    renderWithClient(
      <BulkDeleteTagsDialog
        open
        onOpenChange={() => {}}
        org="dev"
        repo="alpine"
        tagNames={["v1.2.3"]}
        onCompleted={() => {}}
      />,
    );

    // The confirm input's accessible name (its label) must include the
    // tag-name verbatim — this is the observable surface the operator's
    // eye lands on before typing.
    const input = screen.getByLabelText(/v1\.2\.3/i);
    expect(input).toBeInTheDocument();

    // Wrong input ("1") must NOT enable the confirm button — that was
    // the old behaviour and is exactly what this fix prevents.
    const confirmBtn = screen.getByRole("button", { name: /^Delete 1/i });
    await user.type(input, "1");
    expect(confirmBtn).toBeDisabled();

    // Typing the actual tag name enables confirm.
    await user.clear(input);
    await user.type(input, "v1.2.3");
    expect(confirmBtn).toBeEnabled();
  });

  test("multi-tag delete still asks for the count", async () => {
    const user = userEvent.setup();
    renderWithClient(
      <BulkDeleteTagsDialog
        open
        onOpenChange={() => {}}
        org="dev"
        repo="alpine"
        tagNames={["v1", "v2", "v3"]}
        onCompleted={() => {}}
      />,
    );

    // Label is "Type 3 to confirm" — the count, not a tag name. Typing
    // any one of the names must NOT satisfy the gate.
    const input = screen.getByLabelText(/type/i);
    const confirmBtn = screen.getByRole("button", { name: /^Delete 3/i });
    await user.type(input, "v1");
    expect(confirmBtn).toBeDisabled();
    await user.clear(input);
    await user.type(input, "3");
    expect(confirmBtn).toBeEnabled();
  });
});
