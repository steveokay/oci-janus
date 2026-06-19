/**
 * EmptyState — friendly "no repositories yet" panel shown when the
 * tenant has zero repos.
 *
 * Pattern follows Notion/Linear empty states: a hint of what should
 * live here, a primary CTA (create), and a copy-able quickstart so a
 * first-time user has a clear next step beyond clicking the button.
 */
import { Package, Plus } from 'lucide-react'
import { Button } from '@/components/ui/Button'

export interface EmptyStateProps {
  onCreate: () => void
}

export function EmptyState({ onCreate }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center rounded-lg border border-border bg-surface p-2xl text-center">
      <span
        aria-hidden="true"
        className="inline-flex items-center justify-center w-14 h-14 rounded-md bg-primary-soft text-primary"
      >
        <Package className="w-7 h-7" />
      </span>
      <h2 className="mt-lg text-heading-sm font-semibold text-on-surface">
        No repositories yet
      </h2>
      <p className="mt-xs max-w-md text-body-sm text-on-surface-muted">
        Repositories hold the image tags you push from CI or your laptop.
        Create one to get started — you can change visibility and quota
        any time.
      </p>
      <Button
        variant="primary"
        size="lg"
        className="mt-lg"
        onClick={onCreate}
      >
        <Plus className="w-4 h-4" aria-hidden="true" />
        New repository
      </Button>
    </div>
  )
}
