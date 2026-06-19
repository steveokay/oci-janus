/**
 * Quickstart — copy-pasteable CLI snippet for first-time setup.
 *
 * Three lines: `docker login`, `docker tag`, `docker push` against the
 * current workspace's registry hostname. The hostname is a placeholder
 * for now (we don't have a per-tenant host wired up in dev); Sprint 3a
 * (runtime site settings) makes this a real configured value.
 *
 * The copy button writes to the clipboard via the standard API. On
 * insecure contexts (some corporate dev machines) clipboard writes can
 * fail — we surface that as a toast rather than silently no-oping.
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
      // Reset the icon after a beat. setTimeout is fine for this — no
      // state coordination needed and the user already has their copy.
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
      className="flex flex-col rounded-lg border border-border bg-surface"
    >
      <header className="flex items-center justify-between p-lg pb-md border-b border-border">
        <div className="flex items-center gap-sm">
          <span
            aria-hidden="true"
            className="inline-flex items-center justify-center w-8 h-8 rounded-sm bg-primary-soft text-primary"
          >
            <Terminal className="w-4 h-4" />
          </span>
          <h2
            id="quickstart-heading"
            className="text-heading-sm font-semibold text-on-surface"
          >
            Quickstart
          </h2>
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
      <pre className="m-lg overflow-x-auto rounded-sm bg-neutral-950 text-on-surface-inverse p-lg text-code-sm font-mono leading-relaxed">
        <code>{SNIPPET}</code>
      </pre>
    </section>
  )
}
