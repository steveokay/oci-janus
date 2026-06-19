/**
 * EmptyState — friendly "no repositories yet" panel shown when the
 * tenant has zero repos.
 *
 * Two halves:
 *   * Left: warm greeting + primary "New repository" CTA.
 *   * Right: copy-pasteable CLI snippet for the first push, since most
 *     new users already have an image somewhere and just need to know
 *     the docker commands. This way the panel doubles as a quickstart
 *     instead of being a single-CTA dead end.
 *
 * The whole panel uses the same amber-to-rose gradient as the dashboard
 * hero so users feel they're still in the same product, not on a
 * stripped-down "empty page" version of the workspace.
 */
import { useState } from 'react'
import { Check, Copy, Package, Plus, Terminal } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/Button'
import { cn } from '@/lib/utils/cn'

const DEV_REGISTRY_HOST = 'registry.localhost:5000'

const SNIPPET = `docker login ${DEV_REGISTRY_HOST}
docker tag my-app:latest ${DEV_REGISTRY_HOST}/my-app:v1
docker push ${DEV_REGISTRY_HOST}/my-app:v1`

export interface EmptyStateProps {
  onCreate: () => void
}

export function EmptyState({ onCreate }: EmptyStateProps) {
  const [copied, setCopied] = useState(false)

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(SNIPPET)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      toast.error("Couldn't copy to clipboard", {
        description: 'Your browser may have blocked clipboard access.',
      })
    }
  }

  return (
    <section
      className="relative overflow-hidden rounded-lg border border-border bg-surface"
      style={{
        // Warm wash to match the dashboard hero — keeps visual continuity
        // when the user lands on an empty workspace.
        backgroundImage:
          'linear-gradient(120deg, oklch(0.97 0.04 60) 0%, oklch(0.98 0.025 350) 55%, oklch(1 0 0) 100%)',
      }}
    >
      <div className="relative grid grid-cols-1 lg:grid-cols-5 gap-lg p-2xl">
        {/* Left — pitch + CTA */}
        <div className="lg:col-span-2 flex flex-col justify-center">
          <span
            aria-hidden="true"
            className="inline-flex items-center justify-center w-12 h-12 rounded-md bg-primary-soft text-primary"
          >
            <Package className="w-6 h-6" />
          </span>
          <h2 className="mt-lg text-heading-sm font-semibold text-on-surface">
            No repositories yet
          </h2>
          <p className="mt-xs max-w-md text-body-sm text-on-surface-muted">
            Repositories hold the image tags you push from CI or your
            laptop. Create one with the form, or jump straight to the
            command line and push — the repository auto-creates on first
            push if your role allows it.
          </p>
          <Button
            variant="primary"
            size="lg"
            className="mt-lg self-start"
            onClick={onCreate}
          >
            <Plus className="w-4 h-4" aria-hidden="true" />
            New repository
          </Button>
        </div>

        {/* Right — copy-pasteable quickstart. */}
        <div className="lg:col-span-3">
          <div className="rounded-lg border border-border bg-surface overflow-hidden">
            <header className="flex items-center justify-between px-lg py-md border-b border-border">
              <div className="flex items-center gap-sm">
                <span
                  aria-hidden="true"
                  className="inline-flex items-center justify-center w-8 h-8 rounded-sm bg-primary-soft text-primary"
                >
                  <Terminal className="w-4 h-4" />
                </span>
                <span className="text-body-sm font-semibold text-on-surface">
                  Push your first image
                </span>
              </div>
              <button
                type="button"
                onClick={onCopy}
                aria-label={copied ? 'Copied to clipboard' : 'Copy command to clipboard'}
                className={cn(
                  'inline-flex items-center gap-xs h-8 px-md rounded-xs',
                  'text-label-md font-medium transition-colors',
                  copied
                    ? 'text-success-500 bg-success-100'
                    : 'text-on-surface-muted hover:text-on-surface hover:bg-surface-muted',
                )}
              >
                {copied ? (
                  <Check className="w-4 h-4" aria-hidden="true" />
                ) : (
                  <Copy className="w-4 h-4" aria-hidden="true" />
                )}
                {copied ? 'Copied' : 'Copy'}
              </button>
            </header>
            <pre className="overflow-x-auto bg-neutral-950 text-on-surface-inverse px-lg py-md text-code-sm font-mono leading-relaxed">
              <code>{SNIPPET}</code>
            </pre>
          </div>
        </div>
      </div>
    </section>
  )
}
