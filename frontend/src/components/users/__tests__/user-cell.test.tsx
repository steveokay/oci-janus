import { render, screen } from "@testing-library/react";
import { describe, test, expect } from "vitest";
import { UserCell } from "../user-cell";

// REM-018 Phase B: UserCell rendering rules are operator-visible — any
// regression here corrupts the members tables, the activity feed, and the
// remove-member confirmation dialog. Tests pin each of the 4 shapes
// (distinct label, username-only, system, inline variant) explicitly so a
// well-meaning refactor can't silently re-introduce "alice (@alice)"
// double-render or a UUID leak.

const UUID = "11111111-2222-3333-4444-555555555555";

describe("UserCell", () => {
  test("renders display_name + @username when distinct", () => {
    render(
      <UserCell
        userId={UUID}
        username="alice"
        displayName="Alice Adams"
      />,
    );
    expect(screen.getByText("Alice Adams")).toBeInTheDocument();
    expect(screen.getByText("@alice")).toBeInTheDocument();
  });

  test("collapses to @username when display_name equals username", () => {
    // Mirrors the COALESCE fallback on the backend: when a user hasn't set
    // a display_name, the auth service emits display_name = username so
    // the FE has a non-empty label. UserCell must NOT render "@alice
    // (@alice)" in that case.
    render(<UserCell userId={UUID} username="alice" displayName="alice" />);
    expect(screen.getByText("@alice")).toBeInTheDocument();
    // No second "@alice" instance — only the single label.
    expect(screen.queryAllByText("@alice")).toHaveLength(1);
  });

  test("renders @username when display_name is empty", () => {
    render(<UserCell userId={UUID} username="bob" displayName="" />);
    expect(screen.getByText("@bob")).toBeInTheDocument();
  });

  test("renders system placeholder when username and display_name both empty", () => {
    // The granted-by enrichment LEFT JOIN returns empty strings when the
    // assignment was created by the system (granted_by zero-UUID). UserCell
    // must surface a "System" label rather than a misleading "@".
    render(<UserCell userId="" username="" displayName="" />);
    expect(screen.getByText("System")).toBeInTheDocument();
    expect(screen.getByText(/system actor/i)).toBeInTheDocument();
  });

  test("inline variant omits the avatar grid", () => {
    const { container } = render(
      <UserCell
        userId={UUID}
        username="alice"
        displayName="Alice Adams"
        variant="inline"
      />,
    );
    // The default variant renders a `<span aria-hidden>` grid as the
    // avatar tile. The inline variant must skip it so dense feeds (audit
    // rows, activity feed) don't burn vertical space.
    expect(container.querySelector(".grid.size-8")).toBeNull();
    expect(screen.getByText("Alice Adams")).toBeInTheDocument();
  });

  test("exposes user_id via the title attribute for power-users", () => {
    // Operators routinely need the UUID for curl + SQL flows. UserCell
    // must keep the UUID one hover away on the primary label even when the
    // visible text is a friendlier display_name.
    render(
      <UserCell
        userId={UUID}
        username="alice"
        displayName="Alice Adams"
      />,
    );
    const label = screen.getByText("Alice Adams");
    expect(label).toHaveAttribute("title", UUID);
  });
});
