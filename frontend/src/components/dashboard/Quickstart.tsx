/**
 * Quickstart — copy-pasteable CLI snippet for first-time setup.
 *
 * Full-width card with a two-column layout on md+: pitch + CTA on the
 * left, terminal-styled snippet on the right. Stacks vertically below
 * md so phone widths still work.
 *
 * Card background is a plain `bg-surface` — earlier iterations layered
 * a Higgsfield photograph on top of a gradient, but it made the card
 * read as too "designed" relative to its neighbours. Plain surface
 * with the terminal chrome carrying the visual interest reads cleaner.
 *
 * Hostname (`registry.localhost:5000`) is hardcoded for dev. Sprint 3a
 * (runtime site settings) turns this into a per-tenant resolved value.
 */
import { useState } from 'react'
import { Check, Copy, Terminal } from 'lucide-react'
import { toast } from 'sonner'
import { cn } from '@/lib/utils/cn'

const DEV_REGISTRY_HOST = 'registry.localhost:5000'

const SNIPPET = `docker login ${DEV_REGISTRY_HOST}
docker tag my-app:latest ${DEV_REGISTRY_HOST}/my-app:v1
docker push ${DEV_REGISTRY_HOST}/my-app:v1`

export function Quickstart() {
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
      aria-labelledby="quickstart-heading"
      className="rounded-lg border border-border bg-surface"
    >
      <div className="grid grid-cols-1 lg:grid-cols-5 gap-lg p-xl">
        {/* Left column — pitch + heading. */}
        <div className="lg:col-span-2 flex flex-col justify-center">
          <span
            aria-hidden="true"
            className="inline-flex items-center justify-center w-10 h-10 rounded-sm bg-primary-soft text-primary"
          >
            <Terminal className="w-5 h-5" />
          </span>
          <h2
            id="quickstart-heading"
            className="mt-md text-heading-sm font-semibold text-on-surface"
          >
            Push your first image
          </h2>
          <p className="mt-xs text-body-sm text-on-surface-muted">
            Authenticate, tag your local image, push. The repository
            auto-creates on first push if your role allows it — no need
            to pre-provision.
          </p>
        </div>

        {/* Right column — terminal-styled code block. */}
        <div className="lg:col-span-3">
          <div className="rounded-md overflow-hidden shadow-sm border border-neutral-800">
            <header className="flex items-center justify-between px-md py-sm bg-neutral-900 border-b border-neutral-800">
              <div className="flex items-center gap-xs text-label-sm font-mono text-on-surface-inverse/70">
                <span className="w-2.5 h-2.5 rounded-full bg-danger-500/80" aria-hidden="true" />
                <span className="w-2.5 h-2.5 rounded-full bg-warning-500/80" aria-hidden="true" />
                <span className="w-2.5 h-2.5 rounded-full bg-success-500/80" aria-hidden="true" />
                <span className="ml-sm">terminal</span>
              </div>
              <button
                type="button"
                onClick={onCopy}
                aria-label={copied ? 'Copied to clipboard' : 'Copy command to clipboard'}
                className={cn(
                  'inline-flex items-center gap-xs h-7 px-sm rounded-xs',
                  'text-label-sm font-medium transition-colors',
                  copied
                    ? 'text-success-500 bg-success-500/15'
                    : 'text-on-surface-inverse/70 hover:text-on-surface-inverse hover:bg-white/10',
                )}
              >
                {copied ? (
                  <Check className="w-3.5 h-3.5" aria-hidden="true" />
                ) : (
                  <Copy className="w-3.5 h-3.5" aria-hidden="true" />
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
