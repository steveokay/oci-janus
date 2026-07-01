import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { PromoteTagDialog } from "../PromoteTagDialog";

// Tests for the FUT-020 PromoteTagDialog. Mocks the promotions API so
// nothing hits the network — the dialog only consumes usePromoteTag for
// the submit mutation.

const mockMutate = vi.fn();
let mockPending = false;

vi.mock("@/lib/api/promotions", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api/promotions")>(
      "@/lib/api/promotions",
    );
  return {
    ...actual,
    usePromoteTag: () => ({
      mutateAsync: mockMutate,
      mutate: mockMutate,
      isPending: mockPending,
      error: null,
      reset: vi.fn(),
    }),
  };
});

// Sonner toasts are noisy in test output and don't matter for behaviour;
// stub the toast API so an assertion-driven test can focus on the mutation
// call instead of side-effect UI.
vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

function renderDialog(open = true) {
  const onOpenChange = vi.fn();
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const utils = render(
    <QueryClientProvider client={qc}>
      <PromoteTagDialog
        open={open}
        onOpenChange={onOpenChange}
        srcOrg="acme"
        srcRepo="api"
        srcTag="v1.2.3"
      />
    </QueryClientProvider>,
  );
  return { ...utils, onOpenChange };
}

describe("PromoteTagDialog", () => {
  beforeEach(() => {
    mockMutate.mockReset();
    mockMutate.mockResolvedValue({
      id: "prom-1",
      dst_org: "acme",
      dst_repo: "api",
      dst_tag: "prod",
      src_org: "acme",
      src_repo: "api",
      src_tag: "v1.2.3",
      src_digest: "sha256:abc",
      dst_digest: "sha256:abc",
      promoted_at: "2026-07-01T00:00:00Z",
    });
    mockPending = false;
  });

  it("renders the dialog with the source pinned in the description", () => {
    renderDialog();
    expect(
      screen.getByRole("heading", { name: /promote tag/i }),
    ).toBeInTheDocument();
    // Description mentions the source composite so the operator confirms
    // they're promoting the right thing.
    expect(screen.getByText(/acme\/api:v1\.2\.3/i)).toBeInTheDocument();
  });

  it("defaults every destination field to the source values", () => {
    renderDialog();
    expect(
      (screen.getByLabelText(/destination org/i) as HTMLInputElement).value,
    ).toBe("acme");
    expect(
      (screen.getByLabelText(/destination repository/i) as HTMLInputElement)
        .value,
    ).toBe("api");
    expect(
      (screen.getByLabelText(/destination tag/i) as HTMLInputElement).value,
    ).toBe("v1.2.3");
  });

  it("shows an inline error when the destination org fails the shape regex", async () => {
    const user = userEvent.setup();
    renderDialog();

    // Uppercase orgs violate the CLAUDE.md §7 regex.
    await user.clear(screen.getByLabelText(/destination org/i));
    await user.type(screen.getByLabelText(/destination org/i), "ACME");
    await user.click(screen.getByRole("button", { name: /^promote$/i }));

    // Error message from the zod schema surfaces inline.
    expect(
      await screen.findByText(/lowercase alphanumeric/i),
    ).toBeInTheDocument();
    expect(mockMutate).not.toHaveBeenCalled();
  });

  it("shows an inline error when the note exceeds 256 chars", async () => {
    const user = userEvent.setup();
    renderDialog();
    const long = "x".repeat(257);
    await user.type(screen.getByLabelText(/^note/i), long);
    await user.click(screen.getByRole("button", { name: /^promote$/i }));
    expect(
      await screen.findByText(/keep the note under 256/i),
    ).toBeInTheDocument();
    expect(mockMutate).not.toHaveBeenCalled();
  });

  it("fires the mutation with the trimmed values on submit", async () => {
    const user = userEvent.setup();
    const { onOpenChange } = renderDialog();

    await user.clear(screen.getByLabelText(/destination tag/i));
    await user.type(screen.getByLabelText(/destination tag/i), "prod");
    await user.type(screen.getByLabelText(/^note/i), "green-lit for prod");
    await user.click(screen.getByRole("button", { name: /^promote$/i }));

    expect(mockMutate).toHaveBeenCalledTimes(1);
    expect(mockMutate).toHaveBeenCalledWith({
      dst_org: "acme",
      dst_repo: "api",
      dst_tag: "prod",
      note: "green-lit for prod",
    });
    // Successful mutation closes the dialog.
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("keeps the dialog open when the mutation rejects", async () => {
    const user = userEvent.setup();
    // Simulate a 403 from the BFF (missing writer role on dst).
    mockMutate.mockRejectedValueOnce({ response: { status: 403 } });
    const { onOpenChange } = renderDialog();

    await user.click(screen.getByRole("button", { name: /^promote$/i }));

    // Rejection should NOT close the dialog — operator sees the toast +
    // a chance to correct their input.
    expect(onOpenChange).not.toHaveBeenCalled();
  });
});
