import * as React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, test, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  ChannelToggleCell,
  NotificationsSection,
} from "../_authenticated.settings.notifications";
import type { NotificationPreferencesPage } from "@/lib/api/notification-preferences";
import type { NotificationWebhookConfig } from "@/lib/api/notification-webhook";

// ── ChannelToggleCell — generic lockout contract ─────────────────────
//
// ChannelToggleCell is the shared, reusable checkbox cell. When `hint` is set
// it renders visibly disabled (the operator cannot flip a channel that's
// managed elsewhere / not yet available). These tests pin that generic
// contract independent of which column uses it.

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

describe("ChannelToggleCell — generic lockout", () => {
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
        hint="Admin-managed"
      />,
    );
    const box = screen.getByRole("checkbox") as HTMLInputElement;
    expect(box.disabled).toBe(true);
    expect(box.getAttribute("aria-disabled")).toBe("true");
    expect(box.title).toBe("Admin-managed");
    await user.click(box);
    expect(onChange).not.toHaveBeenCalled();
  });

  test("with pending=true (no hint), checkbox is disabled but NOT marked locked", () => {
    // The `pending` and `hint` lockouts overlap on `disabled` but are
    // semantically distinct — `pending` means "wait for the inflight write
    // to settle", `hint` means "this channel is managed elsewhere". Only the
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
              hint="Admin-managed"
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

// ── Webhook column — admin-editable / read-only for non-admins ───────
//
// FUT-019 webhook channel: the Webhook column is no longer unconditionally
// locked. For admins it's live + reflects the org webhook config's
// enabled_categories set; for non-admins it's read-only ("Admin-managed").

// Mutable holders reset per test.
let mockIsAdmin = true;
let mockPrefs: NotificationPreferencesPage | undefined;
let mockWebhookCfg: NotificationWebhookConfig | undefined;

vi.mock("@/lib/api/abilities", () => ({
  useIsGlobalAdmin: () => mockIsAdmin,
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/api/notification-preferences", () => ({
  useNotificationPreferences: () => ({
    data: mockPrefs,
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  }),
  useUpdateNotificationPreferences: () => ({
    mutateAsync: vi.fn().mockResolvedValue(mockPrefs),
    isPending: false,
  }),
}));

vi.mock("@/lib/api/email-transport", () => ({
  useEmailTransport: () => ({ data: { enabled: true } }),
}));

vi.mock("@/lib/api/notification-webhook", () => ({
  useNotificationWebhook: () => ({ data: mockWebhookCfg }),
  useUpdateNotificationWebhook: () => ({
    mutateAsync: vi.fn().mockResolvedValue(mockWebhookCfg),
    isPending: false,
  }),
}));

// One-row preference matrix. bell/email are per-user; the webhook column is
// driven off the webhook config's enabled_categories, not this row's
// webhook_enabled.
function prefsFixture(): NotificationPreferencesPage {
  return {
    preferences: [
      {
        key: "scan.completed",
        label: "Scan completed",
        description: "A vulnerability scan finished.",
        shipped_in: "v1.0",
        bell_enabled: true,
        email_enabled: false,
        webhook_enabled: false,
      },
    ],
  };
}

function webhookFixture(
  overrides: Partial<NotificationWebhookConfig> = {},
): NotificationWebhookConfig {
  return {
    url: "https://hooks.example.com/abc",
    enabled: true,
    has_secret: true,
    enabled_categories: ["scan.completed"],
    ...overrides,
  };
}

function renderSection() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <NotificationsSection />
    </QueryClientProvider>,
  );
}

describe("NotificationsSection — webhook column admin gating", () => {
  beforeEach(() => {
    mockIsAdmin = true;
    mockPrefs = prefsFixture();
    mockWebhookCfg = webhookFixture();
  });

  // The three per-row checkboxes are Bell, Email, Webhook in DOM order.
  function checkboxes(): HTMLInputElement[] {
    return screen.getAllByRole("checkbox") as HTMLInputElement[];
  }

  test("admin: webhook checkbox is enabled and reflects enabled_categories", () => {
    mockIsAdmin = true;
    mockWebhookCfg = webhookFixture({ enabled_categories: ["scan.completed"] });
    renderSection();
    const [bell, email, webhook] = checkboxes();
    // Bell/email remain the per-user preference cells (still live).
    expect(bell.checked).toBe(true);
    expect(email.checked).toBe(false);
    // Webhook is editable for admins + checked because the category is in the
    // org config's enabled_categories set.
    expect(webhook.disabled).toBe(false);
    expect(webhook.checked).toBe(true);
    expect(webhook.getAttribute("data-locked")).toBe("false");
  });

  test("admin: webhook checkbox is unchecked when category not in the set", () => {
    mockIsAdmin = true;
    mockWebhookCfg = webhookFixture({ enabled_categories: [] });
    renderSection();
    const webhook = checkboxes()[2];
    expect(webhook.disabled).toBe(false);
    expect(webhook.checked).toBe(false);
  });

  test("non-admin: webhook checkbox is disabled (read-only)", () => {
    mockIsAdmin = false;
    renderSection();
    const [bell, , webhook] = checkboxes();
    // Bell (per-user) stays editable for everyone.
    expect(bell.disabled).toBe(false);
    // Webhook is locked with the Admin-managed hint.
    expect(webhook.disabled).toBe(true);
    expect(webhook.getAttribute("data-locked")).toBe("true");
    expect(webhook.title).toBe("Admin-managed");
  });
});
