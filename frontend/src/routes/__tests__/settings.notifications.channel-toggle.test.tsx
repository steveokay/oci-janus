import * as React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, test, expect, vi } from "vitest";
import { ChannelToggleCell } from "../_authenticated.settings.notifications";

// Channel-not-yet-shipped lockout (moved from Settings › Account to Settings ›
// Notifications in the 2026-07-05 UI cleanup).
//
// The notification matrix ships Email/Webhook tooltipped as "Wired in Phase
// 3+" — but a live checkbox would let an operator enable it, see no toast, and
// walk away believing alerts were on. This test pins the behaviour: when
// `hint` is set, the checkbox is visibly disabled and clicks do NOT fire
// `onChange`.

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

describe("ChannelToggleCell — channel lockout", () => {
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

  test("with pending=true (no hint), checkbox is disabled but NOT marked locked", () => {
    // The `pending` and `hint` lockouts overlap on `disabled` but are
    // semantically distinct — `pending` means "wait for the inflight write
    // to settle", `hint` means "this channel doesn't exist yet". Only the
    // latter should set data-locked + the locked visual cue.
    const onChange = vi.fn();
    renderInTable(
      <ChannelToggleCell enabled={true} pending={true} onChange={onChange} />,
    );
    const box = screen.getByRole("checkbox") as HTMLInputElement;
    expect(box.disabled).toBe(true);
    expect(box.getAttribute("data-locked")).toBe("false");
  });

  test("data-locked attribute reflects hint, not pending", () => {
    // Asserting on `data-locked` (a stable component contract) instead of
    // tailwind class names — the latter would break under a design-system
    // swap even though behaviour is unchanged.
    const { rerender } = renderInTable(
      <ChannelToggleCell enabled={false} pending={false} onChange={vi.fn()} />,
    );
    expect(screen.getByRole("checkbox").getAttribute("data-locked")).toBe(
      "false",
    );

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
    expect(screen.getByRole("checkbox").getAttribute("data-locked")).toBe(
      "true",
    );
  });
});
