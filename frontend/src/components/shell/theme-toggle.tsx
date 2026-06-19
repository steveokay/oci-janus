import { Moon, Sun, Monitor } from "lucide-react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { Button } from "@/components/ui/button";
import { useTheme, type Theme } from "@/lib/theme";

// Beacon — theme toggle. Three-way switcher in a single dropdown.
// Lives in the Topbar; we hand-roll the menu rather than pull in another
// primitive layer since it's just three items.
export function ThemeToggle(): React.ReactElement {
  const { theme, setTheme } = useTheme();
  const Icon = theme === "dark" ? Moon : theme === "light" ? Sun : Monitor;
  const items: Array<{ value: Theme; label: string; icon: typeof Sun }> = [
    { value: "light", label: "Light", icon: Sun },
    { value: "dark", label: "Dark", icon: Moon },
    { value: "system", label: "System", icon: Monitor },
  ];
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <Button variant="ghost" size="icon" aria-label="Toggle theme">
          <Icon className="size-4" />
        </Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={6}
          className="z-50 min-w-36 overflow-hidden rounded-md border border-[var(--color-border)] bg-[var(--color-surface-2)] p-1 shadow-[var(--shadow-floating)]"
        >
          {items.map((it) => {
            const ItemIcon = it.icon;
            const active = theme === it.value;
            return (
              <DropdownMenu.Item
                key={it.value}
                onSelect={() => setTheme(it.value)}
                className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none data-[highlighted]:bg-[var(--color-surface-sunken)]"
              >
                <ItemIcon className="size-4 text-[var(--color-fg-muted)]" />
                <span className="flex-1">{it.label}</span>
                {active ? (
                  <span className="size-1.5 rounded-full bg-[var(--color-accent)]" />
                ) : null}
              </DropdownMenu.Item>
            );
          })}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
