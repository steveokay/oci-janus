import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, test, expect, vi } from "vitest";
import { PasswordInput } from "../password-input";

// FUT-079 — the reveal toggle is the whole point of PasswordInput, so we pin
// its type-swapping + accessibility contract here before it gets wired across
// the app's password fields.
describe("PasswordInput", () => {
  test("starts masked (type=password) with a 'Show password' toggle", () => {
    render(<PasswordInput aria-label="Password" defaultValue="hunter2" />);

    // The field itself is masked on first render.
    const field = screen.getByLabelText("Password") as HTMLInputElement;
    expect(field.type).toBe("password");

    // The toggle advertises the action it will perform (reveal), not the state.
    const toggle = screen.getByRole("button", { name: "Show password" });
    expect(toggle).toHaveAttribute("type", "button");
    expect(toggle).toHaveAttribute("aria-pressed", "false");
  });

  test("clicking the toggle reveals the value and flips the aria-label", async () => {
    const user = userEvent.setup();
    render(<PasswordInput aria-label="Password" defaultValue="hunter2" />);

    const field = screen.getByLabelText("Password") as HTMLInputElement;
    await user.click(screen.getByRole("button", { name: "Show password" }));

    // Revealed: type becomes text, label flips to the hide action.
    expect(field.type).toBe("text");
    const toggle = screen.getByRole("button", { name: "Hide password" });
    expect(toggle).toHaveAttribute("aria-pressed", "true");

    // Clicking again re-masks it.
    await user.click(toggle);
    expect(field.type).toBe("password");
    expect(
      screen.getByRole("button", { name: "Show password" }),
    ).toBeInTheDocument();
  });

  test("the toggle does not submit the surrounding form", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
    render(
      <form onSubmit={onSubmit}>
        <PasswordInput aria-label="Password" />
      </form>,
    );

    await user.click(screen.getByRole("button", { name: "Show password" }));
    // A type="button" toggle must never trigger the form's submit handler.
    expect(onSubmit).not.toHaveBeenCalled();
  });

  test("forwards the ref to the underlying input", () => {
    const ref = { current: null as HTMLInputElement | null };
    render(<PasswordInput aria-label="Password" ref={ref} />);
    // react-hook-form registers via the forwarded ref — it must reach the DOM
    // node, not the wrapper div.
    expect(ref.current).toBeInstanceOf(HTMLInputElement);
  });

  test("passes through native input props (placeholder, autoComplete, required)", () => {
    render(
      <PasswordInput
        aria-label="Password"
        placeholder="Your password"
        autoComplete="current-password"
        required
      />,
    );
    const field = screen.getByLabelText("Password") as HTMLInputElement;
    expect(field).toHaveAttribute("placeholder", "Your password");
    expect(field).toHaveAttribute("autocomplete", "current-password");
    expect(field.required).toBe(true);
  });

  test("does not reveal a disabled field", () => {
    render(<PasswordInput aria-label="Password" disabled />);
    const toggle = screen.getByRole("button", { name: "Show password" });
    expect(toggle).toBeDisabled();
  });
});
