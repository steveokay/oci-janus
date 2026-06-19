import * as React from "react";
import { WEBHOOK_EVENT_CATALOG } from "@/lib/api/webhooks";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

interface WebhookFormFieldsProps {
  url: string;
  onUrlChange: (v: string) => void;
  urlError: string | null;
  selectedEvents: string[];
  onEventsChange: (events: string[]) => void;
  showActive?: boolean;
  active?: boolean;
  onActiveChange?: (active: boolean) => void;
}

// Beacon — shared form fields for both Create + Edit. Lifted out so the
// two dialogs render identically with different submit handlers.
export function WebhookFormFields({
  url,
  onUrlChange,
  urlError,
  selectedEvents,
  onEventsChange,
  showActive,
  active,
  onActiveChange,
}: WebhookFormFieldsProps): React.ReactElement {
  function toggleEvent(key: string): void {
    if (selectedEvents.includes(key)) {
      onEventsChange(selectedEvents.filter((e) => e !== key));
    } else {
      onEventsChange([...selectedEvents, key]);
    }
  }

  return (
    <>
      <div className="space-y-1.5">
        <Label htmlFor="webhook-url">Delivery URL</Label>
        <Input
          id="webhook-url"
          type="url"
          placeholder="https://hooks.example.com/registry"
          value={url}
          onChange={(e) => onUrlChange(e.target.value)}
          aria-invalid={Boolean(urlError) || undefined}
          autoComplete="off"
          spellCheck={false}
          className="font-mono text-xs"
        />
        {urlError ? (
          <p className="text-xs text-[var(--color-danger)]">{urlError}</p>
        ) : (
          <p className="text-xs text-[var(--color-fg-subtle)]">
            HTTPS recommended. The SSRF guard rejects private IPs.
          </p>
        )}
      </div>

      <div className="space-y-2">
        <div className="flex items-baseline justify-between">
          <Label>Events</Label>
          <span className="text-xs text-[var(--color-fg-subtle)]">
            {selectedEvents.length} selected
          </span>
        </div>
        <div className="space-y-1.5">
          {WEBHOOK_EVENT_CATALOG.map(({ key, label, description }) => {
            const checked = selectedEvents.includes(key);
            return (
              <button
                key={key}
                type="button"
                onClick={() => toggleEvent(key)}
                aria-pressed={checked}
                className={cn(
                  "flex w-full items-start gap-3 rounded-md border bg-[var(--color-surface)] px-3 py-2 text-left transition-colors",
                  "focus-visible:outline-none",
                  checked
                    ? "border-[var(--color-accent)] bg-[var(--color-accent-subtle)]/40"
                    : "border-[var(--color-border)] hover:bg-[var(--color-surface-sunken)]",
                )}
              >
                <span
                  aria-hidden
                  className={cn(
                    "mt-0.5 grid size-4 shrink-0 place-items-center rounded-sm border",
                    checked
                      ? "border-[var(--color-accent)] bg-[var(--color-accent)] text-[var(--color-accent-fg)]"
                      : "border-[var(--color-border-strong)] bg-[var(--color-surface)]",
                  )}
                >
                  {checked ? (
                    <svg viewBox="0 0 12 12" className="size-2.5">
                      <path
                        d="M2 6l3 3 5-6"
                        stroke="currentColor"
                        strokeWidth="2"
                        fill="none"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                      />
                    </svg>
                  ) : null}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-2">
                    <span className="text-sm font-medium text-[var(--color-fg)]">
                      {label}
                    </span>
                    <code className="font-mono text-[10px] text-[var(--color-fg-subtle)]">
                      {key}
                    </code>
                  </div>
                  <p className="text-xs text-[var(--color-fg-muted)]">
                    {description}
                  </p>
                </div>
              </button>
            );
          })}
        </div>
      </div>

      {showActive ? (
        <label className="flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-4 py-3 text-sm">
          <div>
            <div className="font-medium">Active</div>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Pause delivery without deleting the endpoint.
            </p>
          </div>
          <input
            type="checkbox"
            checked={active}
            onChange={(e) => onActiveChange?.(e.target.checked)}
            className="size-4 accent-[var(--color-accent)]"
          />
        </label>
      ) : null}
    </>
  );
}
