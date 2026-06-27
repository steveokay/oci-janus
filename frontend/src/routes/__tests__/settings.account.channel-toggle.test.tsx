import * as React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, test, expect, vi } from "vitest";
import { ChannelToggleCell } from "../_authenticated.settings.account";

// REDESIGN-001 Phase 4.5 — channel-not-yet-shipped lockout.
//
// Phase 4.2.b shipped the notification matrix with Email/Webhook tooltipped
// as "Wired in Phase 3+" — but the checkbox was still live, so an operator
// could enable it, see no toast, and walk away believing alerts were on.
// This test pins the new behaviour: when `hint` is set, the checkbox is
// visibly disabled and clicks do NOT fire `onChange`.

// ChannelToggleCell renders a <td>; rendering it outside a <table> works in
// JSDOM but emits an HTML5-validity warning. Wrap in the minimal table chrome
// to keep the test output clean.
function renderInTable(cell: React.ReactElement) {
  return render(
    <table>
      <tbody>
        <tr>{cell}</tr>
      </tbody>
    </table>,
  );
}

describe("ChannelToggleCell — Phase 4.5 lockout", () => {
  test("with no hint, checkbox is enabled and onChange fires", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    renderInTable(
      <ChannelToggleCell enabled={false} pending={false} onChange={onChange} />,
    );
    const box = screen.getByRole("checkbox") as HTMLInputElement;
    expect(box.disabled).toBe(false);
    await user.click(box);
    expect(onChange).toHaveBeenCalledWith(true);
  });

  test("with hint, checkbox is disabled and clicks do NOT fire onChange", async () => {
    const onChange = vi.fn();
    // userEvent (not fireEvent) respects the `disabled` attribute the way a
    // real browser does. fireEvent.click bypasses the disabled check, which
    // would make this assertion vacuous.
    const user = userEvent.setup();
    renderInTable(
      <ChannelToggleCell
        enabled={false}
        pending={false}
        onChange={onChange}
        hint="Wired in Phase 3+"
      />,
    );
    const box = screen.getByRole("checkbox") as HTMLInputElement;
    expect(box.disabled).toBe(true);
    expect(box.getAttribute("aria-disabled")).toBe("true");
    expect(box.title).toBe("Wired in Phase 3+");
    await user.click(box);
    expect(onChange).not.toHaveBeenCalled();
  });

  test("with pending=true, checkbox is disabled regardless of hint", () => {
    const onChange = vi.fn();
    renderInTable(
      <ChannelToggleCell enabled={true} pending={true} onChange={onChange} />,
    );
    const box = screen.getByRole("checkbox") as HTMLInputElement;
    expect(box.disabled).toBe(true);
  });

  test("locked visual cue: cursor-not-allowed + opacity-50 only when hint set", () => {
    // Without hint
    const { rerender } = renderInTable(
      <ChannelToggleCell enabled={false} pending={false} onChange={vi.fn()} />,
    );
    let box = screen.getByRole("checkbox");
    expect(box.className).toContain("cursor-pointer");
    expect(box.className).not.toContain("opacity-50");

    // With hint
    rerender(
      <table>
        <tbody>
          <tr>
            <ChannelToggleCell
              enabled={false}
              pending={false}
              onChange={vi.fn()}
              hint="Wired in Phase 3+"
            />
          </tr>
        </tbody>
      </table>,
    );
    box = screen.getByRole("checkbox");
    expect(box.className).toContain("cursor-not-allowed");
    expect(box.className).toContain("opacity-50");
  });
});
