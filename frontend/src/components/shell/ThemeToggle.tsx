/**
 * ThemeToggle — segmented control inside the user menu for choosing
 * light / system / dark mode. Visual pattern follows Linear: three small
 * icon buttons in a single rounded container; the active mode lifts
 * with a soft primary tint instead of a hard border, so it reads as a
 * preference rather than a destructive action.
 *
 * Tooltip / aria-label uses the long-form name; the visible affordance
 * is icon-only because the row inside DropdownMenu is already labelled
 * "Theme" by its parent context.
 */
import { Monitor, Moon, Sun } from 'lucide-react'
import { useThemeStore, type ThemeMode } from '@/store/themeStore'
import { cn } from '@/lib/utils/cn'

const OPTIONS: { value: ThemeMode; label: string; Icon: typeof Sun }[] = [
  { value: 'light',  label: 'Light',  Icon: Sun },
  { value: 'system', label: 'System', Icon: Monitor },
  { value: 'dark',   label: 'Dark',   Icon: Moon },
]

export function ThemeToggle() {
  const mode = useThemeStore((s) => s.mode)
  const setMode = useThemeStore((s) => s.setMode)

  return (
    <div
      role="radiogroup"
      aria-label="Theme"
      className="inline-flex items-center gap-0.5 rounded-sm border border-border bg-surface p-0.5"
    >
      {OPTIONS.map(({ value, label, Icon }) => {
        const active = value === mode
        return (
          <button
            key={value}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={label}
            title={label}
            onClick={() => setMode(value)}
            className={cn(
              'inline-flex items-center justify-center w-7 h-7 rounded-xs transition-colors',
              active
                ? 'bg-primary-soft text-primary'
                : 'text-on-surface-muted hover:text-on-surface hover:bg-surface-muted',
            )}
          >
            <Icon className="w-3.5 h-3.5" aria-hidden="true" />
          </button>
        )
      })}
    </div>
  )
}
