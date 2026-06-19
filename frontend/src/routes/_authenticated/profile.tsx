/**
 * /profile — Sprint 1e: identity + API keys.
 *
 * Page anatomy:
 *   1. Hero banner — same layered photographic header as the other top-
 *      level pages, anchored on the repositories.png asset.
 *   2. Identity card — read-only display of what's in the JWT today:
 *      username, primary role, sub UUID, tenant. Editable name / email
 *      / password fields are stubbed out with friendly "soon" notes so
 *      the section structure is visible — backend endpoints don't exist
 *      yet (tracked as FE-API-011 through FE-API-013 in status.md).
 *   3. API keys card — full create / list / revoke against
 *      services/auth (`/api/v1/apikeys`). Create flow surfaces the raw
 *      secret exactly once via a dedicated dialog; subsequent reads
 *      can't recover it.
 */
import { useEffect, useMemo, useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import * as Dialog from '@radix-ui/react-dialog'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { zodResolver } from '@hookform/resolvers/zod'
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Check,
  Copy,
  Eye,
  EyeOff,
  Info,
  Lock,
  Plus,
  Trash2,
  TriangleAlert,
  User,
  X,
} from 'lucide-react'
import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
} from '@tanstack/react-table'
import { toast } from 'sonner'
import { AxiosError } from 'axios'
import { Button } from '@/components/ui/Button'
import { Input, Label, FieldError, FieldHint } from '@/components/ui/Input'
import {
  useApiKeys,
  useCreateApiKey,
  useDeleteApiKey,
  type ApiKey,
  type CreatedApiKey,
} from '@/lib/api/hooks/useApiKeys'
import { useAuthStore } from '@/store/authStore'
import { relativeTime } from '@/lib/format/time'
import { EmptyPanel } from '@/components/ui/states/EmptyPanel'
import { ErrorPanel } from '@/components/ui/states/ErrorPanel'
import { TableSkeleton } from '@/components/ui/states/TableSkeleton'
import { ApiKeyIllustration } from '@/components/ui/illustrations'
import { cn } from '@/lib/utils/cn'

export const Route = createFileRoute('/_authenticated/profile')({
  staticData: { crumb: 'Profile' },
  component: ProfilePage,
})

function ProfilePage() {
  return (
    <div className="p-xl space-y-lg">
      <PageHero />
      <IdentityCard />
      <ApiKeysCard />
    </div>
  )
}

/* -------------------------------------------------------------------- */
/* Hero                                                                 */
/* -------------------------------------------------------------------- */

function PageHero() {
  const username = useAuthStore((s) => s.user?.username ?? '')
  return (
    <section
      aria-labelledby="profile-heading"
      className="relative overflow-hidden rounded-lg border border-border bg-surface"
    >
      <div
        aria-hidden="true"
        className="absolute inset-0 dark:hidden"
        style={{
          backgroundImage:
            'linear-gradient(110deg, oklch(0.95 0.06 50) 0%, oklch(0.96 0.04 30) 45%, oklch(0.99 0.02 60) 90%)',
        }}
      />
      <img
        src="/hero/repositories.png"
        alt=""
        aria-hidden="true"
        onError={(e) => {
          ;(e.currentTarget as HTMLImageElement).style.display = 'none'
        }}
        className="absolute inset-0 w-full h-full object-cover opacity-60 mix-blend-overlay pointer-events-none dark:hidden"
      />
      <div
        aria-hidden="true"
        className="absolute inset-0 dark:hidden"
        style={{
          background:
            'linear-gradient(105deg, oklch(1 0 0 / 0.65), oklch(1 0 0 / 0.30) 60%, transparent 90%)',
        }}
      />
      <div
        aria-hidden="true"
        className="hidden dark:block absolute inset-0"
        style={{
          backgroundImage:
            'linear-gradient(105deg, oklch(0.22 0.06 280) 0%, oklch(0.19 0.04 260) 60%, oklch(0.16 0.03 250) 100%)',
        }}
      />
      <div className="relative px-xl py-xl flex items-center gap-md">
        <span
          aria-hidden="true"
          className="inline-flex items-center justify-center w-12 h-12 rounded-md bg-primary-soft text-primary shadow-xs shrink-0"
        >
          <User className="w-6 h-6" />
        </span>
        <div>
          <h1
            id="profile-heading"
            className="text-display-lg font-semibold text-on-surface tracking-tight"
          >
            Profile
          </h1>
          <p className="mt-xs text-body-md text-on-surface-muted">
            {username
              ? `Signed in as ${username}. Manage your account and API keys.`
              : 'Manage your account and API keys.'}
          </p>
        </div>
      </div>
    </section>
  )
}

/* -------------------------------------------------------------------- */
/* Identity                                                             */
/* -------------------------------------------------------------------- */

/**
 * Read-only identity panel.
 *
 * Editable name / email / password fields are stubbed below — the
 * relevant backend endpoints are tracked in status.md as FE-API-011
 * (GET /users/me), FE-API-012 (PATCH /users/me), and FE-API-013
 * (POST /users/me/password). When they land, the rows below become
 * inline-editable.
 */
function IdentityCard() {
  const user = useAuthStore((s) => s.user)
  if (!user) return null

  const initial = user.username.charAt(0).toUpperCase() || '?'
  const primaryRole = user.roles[0] ?? 'member'

  return (
    <section
      aria-labelledby="identity-heading"
      className="rounded-lg border border-border bg-surface"
    >
      <header className="p-lg border-b border-border">
        <h2
          id="identity-heading"
          className="text-heading-sm font-semibold text-on-surface"
        >
          Account
        </h2>
        <p className="mt-xs text-body-sm text-on-surface-muted">
          Read-only for now — inline editing wires up once the backend
          surfaces a user-profile endpoint.
        </p>
      </header>

      <div className="p-lg flex items-start gap-lg">
        <span
          aria-hidden="true"
          className="inline-flex items-center justify-center w-16 h-16 rounded-full bg-primary text-on-primary text-heading-md font-semibold shadow-xs shrink-0"
        >
          {initial}
        </span>
        <dl className="flex-1 grid grid-cols-1 sm:grid-cols-2 gap-lg">
          <Field label="Username" value={user.username || '—'} />
          <Field
            label="Primary role"
            value={<span className="capitalize">{primaryRole}</span>}
          />
          <Field
            label="User ID"
            value={
              <code className="font-mono text-code-sm break-all">
                {user.sub}
              </code>
            }
          />
          <Field
            label="Workspace ID"
            value={
              <code className="font-mono text-code-sm break-all">
                {user.tenantId}
              </code>
            }
          />
        </dl>
      </div>

      <footer className="p-lg border-t border-border bg-surface-muted/30">
        <div className="flex items-start gap-sm text-body-sm text-on-surface-muted">
          <Info
            className="w-4 h-4 text-on-surface-subtle shrink-0 mt-0.5"
            aria-hidden="true"
          />
          <p>
            Editing display name, email, and password lands once the auth
            service exposes the matching endpoints (FE-API-011 through
            FE-API-013).
          </p>
        </div>
      </footer>
    </section>
  )
}

function Field({
  label,
  value,
}: {
  label: string
  value: React.ReactNode
}) {
  return (
    <div>
      <dt className="text-label-sm uppercase tracking-wider text-on-surface-subtle font-semibold">
        {label}
      </dt>
      <dd className="mt-xs text-body-md text-on-surface">{value}</dd>
    </div>
  )
}

/* -------------------------------------------------------------------- */
/* API Keys                                                             */
/* -------------------------------------------------------------------- */

function ApiKeysCard() {
  const { data: keys = [], isLoading, isError } = useApiKeys()
  const [createOpen, setCreateOpen] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<ApiKey | null>(null)
  const [secret, setSecret] = useState<CreatedApiKey | null>(null)

  return (
    <section
      aria-labelledby="api-keys-heading"
      className="rounded-lg border border-border bg-surface"
    >
      <header className="p-lg border-b border-border flex flex-col gap-md sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2
            id="api-keys-heading"
            className="text-heading-sm font-semibold text-on-surface"
          >
            API keys
          </h2>
          <p className="mt-xs text-body-sm text-on-surface-muted">
            Long-lived credentials for CI, scripts, and the CLI. Treat
            the raw key as a password — anyone with it can act as you.
          </p>
        </div>
        <Button variant="primary" onClick={() => setCreateOpen(true)}>
          <Plus className="w-4 h-4" aria-hidden="true" />
          New API key
        </Button>
      </header>

      {isError ? (
        <div className="p-lg">
          <ErrorPanel
            title="Couldn't load API keys"
            description={
              <>
                Retrying automatically. If this persists, check{' '}
                <code className="font-mono text-code-sm">registry-auth</code>{' '}
                is reachable.
              </>
            }
          />
        </div>
      ) : isLoading ? (
        <div className="p-sm">
          <TableSkeleton rows={3} widths={[160, 120, 100, 100, 32]} className="border-0 shadow-none" />
        </div>
      ) : keys.length === 0 ? (
        <ApiKeysEmpty onCreate={() => setCreateOpen(true)} />
      ) : (
        <ApiKeysTable keys={keys} onRevoke={setRevokeTarget} />
      )}

      <CreateApiKeyDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(k) => setSecret(k)}
      />
      <RevokeApiKeyDialog
        target={revokeTarget}
        onOpenChange={(open) => !open && setRevokeTarget(null)}
      />
      <ShowSecretDialog
        secret={secret}
        onOpenChange={(open) => !open && setSecret(null)}
      />
    </section>
  )
}

/* ----- Empty / loading ----------------------------------------------- */

function ApiKeysEmpty({ onCreate }: { onCreate: () => void }) {
  return (
    <div className="p-lg">
      <EmptyPanel
        illustration={<ApiKeyIllustration className="w-28 h-28 text-primary" />}
        title="No API keys yet"
        description="Create one to authenticate from CI, scripts, or the registry CLI. You'll see the raw secret exactly once when it's created — copy it somewhere safe."
        action={
          <Button variant="primary" onClick={onCreate}>
            <Plus className="w-4 h-4" aria-hidden="true" />
            Create your first key
          </Button>
        }
      />
    </div>
  )
}

/* ----- Table --------------------------------------------------------- */

function ApiKeysTable({
  keys,
  onRevoke,
}: {
  keys: ApiKey[]
  onRevoke: (key: ApiKey) => void
}) {
  const [sorting, setSorting] = useState<SortingState>([
    { id: 'created_at', desc: true },
  ])

  const columns = useMemo<ColumnDef<ApiKey>[]>(
    () => [
      {
        accessorKey: 'name',
        header: 'Name',
        cell: (info) => (
          <span className="text-body-sm font-medium text-on-surface">
            {info.getValue<string>()}
          </span>
        ),
      },
      {
        accessorKey: 'prefix',
        header: 'Prefix',
        cell: (info) => (
          <code className="font-mono text-code-sm text-on-surface-muted">
            {info.getValue<string>()}…
          </code>
        ),
        enableSorting: false,
      },
      {
        accessorKey: 'scopes',
        header: 'Scopes',
        cell: (info) => {
          const scopes = info.getValue<string[]>() ?? []
          if (scopes.length === 0) {
            return (
              <span className="text-label-sm text-on-surface-subtle italic">
                none
              </span>
            )
          }
          return (
            <div className="flex flex-wrap gap-xs">
              {scopes.map((s) => (
                <span
                  key={s}
                  className="inline-flex items-center px-sm py-0.5 rounded-full border border-border bg-neutral-100 text-on-surface-muted text-label-sm font-mono"
                >
                  {s}
                </span>
              ))}
            </div>
          )
        },
        enableSorting: false,
      },
      {
        accessorKey: 'expires_at',
        header: 'Expires',
        cell: (info) => {
          const value = info.getValue<string | null>()
          if (!value) {
            return (
              <span className="text-label-sm text-on-surface-subtle">
                Never
              </span>
            )
          }
          const expired = new Date(value).getTime() < Date.now()
          return (
            <span
              className={cn(
                'text-body-sm',
                expired ? 'text-danger-500 font-medium' : 'text-on-surface-muted',
              )}
            >
              {expired ? `Expired ${relativeTime(value)}` : relativeTime(value)}
            </span>
          )
        },
      },
      {
        accessorKey: 'created_at',
        header: 'Created',
        cell: (info) => (
          <span className="text-body-sm text-on-surface-muted">
            {relativeTime(info.getValue<string>())}
          </span>
        ),
        sortingFn: (a, b) =>
          new Date(a.original.created_at).getTime() -
          new Date(b.original.created_at).getTime(),
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">Actions</span>,
        cell: ({ row }) => (
          <div className="flex items-center justify-end opacity-0 group-hover/row:opacity-100 focus-within:opacity-100 transition-opacity">
            <button
              type="button"
              aria-label={`Revoke API key ${row.original.name}`}
              onClick={() => onRevoke(row.original)}
              className="inline-flex items-center justify-center w-8 h-8 rounded-xs text-on-surface-subtle hover:text-danger-500 hover:bg-danger-100 transition-colors"
            >
              <Trash2 className="w-4 h-4" aria-hidden="true" />
            </button>
          </div>
        ),
        enableSorting: false,
      },
    ],
    [onRevoke],
  )

  const table = useReactTable({
    data: keys,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  })

  return (
    <table className="w-full text-left">
      <thead className="border-b border-border bg-surface-muted/40">
        {table.getHeaderGroups().map((headerGroup) => (
          <tr key={headerGroup.id}>
            {headerGroup.headers.map((header) => {
              const canSort = header.column.getCanSort()
              const sortDir = header.column.getIsSorted()
              return (
                <th
                  key={header.id}
                  scope="col"
                  className="px-lg py-sm text-label-sm uppercase tracking-wider font-semibold text-on-surface-subtle"
                >
                  {canSort ? (
                    <button
                      type="button"
                      onClick={header.column.getToggleSortingHandler()}
                      className={cn(
                        'inline-flex items-center gap-xs',
                        'hover:text-on-surface transition-colors',
                        sortDir && 'text-on-surface',
                      )}
                    >
                      {flexRender(
                        header.column.columnDef.header,
                        header.getContext(),
                      )}
                      <SortIcon dir={sortDir || false} />
                    </button>
                  ) : (
                    flexRender(
                      header.column.columnDef.header,
                      header.getContext(),
                    )
                  )}
                </th>
              )
            })}
          </tr>
        ))}
      </thead>
      <tbody className="divide-y divide-border">
        {table.getRowModel().rows.map((row) => (
          <tr
            key={row.id}
            className="group/row hover:bg-surface-muted/40 transition-colors"
          >
            {row.getVisibleCells().map((cell) => (
              <td key={cell.id} className="px-lg py-md align-middle">
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function SortIcon({ dir }: { dir: 'asc' | 'desc' | false }) {
  if (dir === 'asc') return <ArrowUp className="w-3 h-3" aria-hidden="true" />
  if (dir === 'desc') return <ArrowDown className="w-3 h-3" aria-hidden="true" />
  return <ArrowUpDown className="w-3 h-3 opacity-50" aria-hidden="true" />
}

/* ----- Create dialog ------------------------------------------------- */

const SCOPE_OPTIONS: { value: string; label: string; description: string }[] = [
  {
    value: 'repo:read',
    label: 'Read',
    description: 'Pull images. Read-only access to repos and tags.',
  },
  {
    value: 'repo:write',
    label: 'Write',
    description: 'Push and pull. Cannot delete or change visibility.',
  },
  {
    value: 'repo:admin',
    label: 'Admin',
    description: 'Push, pull, delete, manage visibility and quotas.',
  },
]

const createSchema = z.object({
  name: z
    .string()
    .min(1, 'Required')
    .max(64, 'Max 64 characters'),
  scope: z.enum(['repo:read', 'repo:write', 'repo:admin']),
  expiry: z.enum(['never', '30d', '90d', '365d']),
})

type CreateFormInput = z.infer<typeof createSchema>

function CreateApiKeyDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (key: CreatedApiKey) => void
}) {
  const create = useCreateApiKey()
  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<CreateFormInput>({
    resolver: zodResolver(createSchema),
    defaultValues: { name: '', scope: 'repo:write', expiry: '365d' },
  })

  useEffect(() => {
    if (!open) reset()
  }, [open, reset])

  const onSubmit = (input: CreateFormInput) => {
    create.mutate(
      {
        name: input.name,
        scopes: [input.scope],
        expires_at: expiryToIso(input.expiry),
      },
      {
        onSuccess: (key) => {
          onOpenChange(false)
          onCreated(key)
        },
        onError: (err) => {
          if (err instanceof AxiosError) {
            if (err.response?.status === 409) {
              toast.error('An API key with that name already exists')
              return
            }
          }
          toast.error("Couldn't create API key")
        },
      },
    )
  }

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-surface-overlay backdrop-blur-sm" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-[520px] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-surface shadow-xl focus:outline-none">
          <div className="flex items-start justify-between p-lg border-b border-border">
            <div>
              <Dialog.Title className="text-heading-sm font-semibold text-on-surface">
                New API key
              </Dialog.Title>
              <Dialog.Description className="mt-xs text-body-sm text-on-surface-muted">
                Pick a memorable name and the smallest scope you need.
                You'll see the raw secret exactly once.
              </Dialog.Description>
            </div>
            <Dialog.Close
              aria-label="Close"
              className="inline-flex items-center justify-center w-8 h-8 rounded-xs text-on-surface-muted hover:text-on-surface hover:bg-surface-muted transition-colors"
            >
              <X className="w-4 h-4" aria-hidden="true" />
            </Dialog.Close>
          </div>

          <form onSubmit={handleSubmit(onSubmit)} className="p-lg space-y-lg">
            <div>
              <Label htmlFor="apikey-name">Name</Label>
              <Input
                id="apikey-name"
                type="text"
                placeholder="CI deploy bot"
                autoComplete="off"
                error={!!errors.name}
                {...register('name')}
              />
              {errors.name ? (
                <FieldError>{errors.name.message}</FieldError>
              ) : (
                <FieldHint>Where will this key live? Be specific.</FieldHint>
              )}
            </div>

            <fieldset>
              <legend className="block mb-sm text-label-md font-medium text-on-surface">
                Scope
              </legend>
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-sm">
                {SCOPE_OPTIONS.map((opt) => (
                  <ScopeRadio
                    key={opt.value}
                    value={opt.value}
                    label={opt.label}
                    description={opt.description}
                    {...register('scope')}
                  />
                ))}
              </div>
            </fieldset>

            <fieldset>
              <legend className="block mb-sm text-label-md font-medium text-on-surface">
                Expires
              </legend>
              <div className="grid grid-cols-2 sm:grid-cols-4 gap-sm">
                {(['30d', '90d', '365d', 'never'] as const).map((v) => (
                  <ExpiryRadio key={v} value={v} {...register('expiry')} />
                ))}
              </div>
            </fieldset>

            <div className="flex items-center justify-end gap-sm pt-md border-t border-border">
              <Button
                type="button"
                variant="ghost"
                onClick={() => onOpenChange(false)}
              >
                Cancel
              </Button>
              <Button type="submit" loading={create.isPending}>
                Create key
              </Button>
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

const ScopeRadio = ({
  value,
  label,
  description,
  ...rest
}: {
  value: string
  label: string
  description: string
} & React.InputHTMLAttributes<HTMLInputElement>) => (
  <label
    className={cn(
      'group flex flex-col gap-xs px-md py-sm rounded-sm border border-border',
      'cursor-pointer hover:border-border-strong transition-colors',
      'has-[input:checked]:border-primary has-[input:checked]:bg-primary-soft',
    )}
  >
    <input type="radio" value={value} className="sr-only" {...rest} />
    <span className="text-body-sm font-medium text-on-surface">{label}</span>
    <span className="text-label-sm text-on-surface-muted">{description}</span>
  </label>
)

const EXPIRY_LABELS: Record<string, string> = {
  '30d': '30 days',
  '90d': '90 days',
  '365d': '1 year',
  never: 'Never',
}

const ExpiryRadio = ({
  value,
  ...rest
}: { value: string } & React.InputHTMLAttributes<HTMLInputElement>) => (
  <label
    className={cn(
      'flex items-center justify-center px-md h-10 rounded-sm border border-border',
      'cursor-pointer hover:border-border-strong transition-colors',
      'has-[input:checked]:border-primary has-[input:checked]:bg-primary-soft has-[input:checked]:text-primary',
      'text-body-sm font-medium text-on-surface',
    )}
  >
    <input type="radio" value={value} className="sr-only" {...rest} />
    {EXPIRY_LABELS[value]}
  </label>
)

/** Map UI expiry choice to an absolute ISO timestamp the backend expects. */
function expiryToIso(choice: string): string | undefined {
  const now = Date.now()
  switch (choice) {
    case '30d':
      return new Date(now + 30 * 86400_000).toISOString()
    case '90d':
      return new Date(now + 90 * 86400_000).toISOString()
    case '365d':
      return new Date(now + 365 * 86400_000).toISOString()
    case 'never':
    default:
      return undefined
  }
}

/* ----- Show-secret dialog ------------------------------------------- */

/**
 * Shown immediately after create. The raw secret is in the response
 * body once and never again; the dialog forces the user to acknowledge
 * they've copied it before closing.
 */
function ShowSecretDialog({
  secret,
  onOpenChange,
}: {
  secret: CreatedApiKey | null
  onOpenChange: (open: boolean) => void
}) {
  const [revealed, setRevealed] = useState(false)
  const [copied, setCopied] = useState(false)
  const [confirmed, setConfirmed] = useState(false)

  useEffect(() => {
    if (secret) {
      setRevealed(false)
      setCopied(false)
      setConfirmed(false)
    }
  }, [secret])

  if (!secret) return null

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(secret.key)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      toast.error("Couldn't copy to clipboard")
    }
  }

  return (
    <Dialog.Root open={!!secret} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-surface-overlay backdrop-blur-sm" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-[560px] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-surface shadow-xl focus:outline-none">
          <div className="p-lg border-b border-border">
            <div className="flex items-start gap-md">
              <span
                aria-hidden="true"
                className="inline-flex items-center justify-center w-10 h-10 rounded-sm bg-warning-100 text-warning-500 shrink-0"
              >
                <TriangleAlert className="w-5 h-5" />
              </span>
              <div>
                <Dialog.Title className="text-heading-sm font-semibold text-on-surface">
                  Copy your API key
                </Dialog.Title>
                <Dialog.Description className="mt-xs text-body-sm text-on-surface-muted">
                  This is the only time we'll show this secret. Store it
                  in your secrets manager now — we can't recover it later.
                </Dialog.Description>
              </div>
            </div>
          </div>

          <div className="p-lg space-y-md">
            <div>
              <Label htmlFor="apikey-secret">{secret.name}</Label>
              <div className="relative">
                <Input
                  id="apikey-secret"
                  type={revealed ? 'text' : 'password'}
                  value={secret.key}
                  readOnly
                  className="font-mono text-code-sm pr-[5rem]"
                  onFocus={(e) => e.currentTarget.select()}
                />
                <div className="absolute right-sm top-1/2 -translate-y-1/2 flex items-center gap-xs">
                  <button
                    type="button"
                    onClick={() => setRevealed((s) => !s)}
                    aria-label={revealed ? 'Hide secret' : 'Reveal secret'}
                    className="inline-flex items-center justify-center w-8 h-8 rounded-xs text-on-surface-muted hover:text-on-surface hover:bg-surface-muted transition-colors"
                  >
                    {revealed ? (
                      <EyeOff className="w-4 h-4" aria-hidden="true" />
                    ) : (
                      <Eye className="w-4 h-4" aria-hidden="true" />
                    )}
                  </button>
                  <button
                    type="button"
                    onClick={onCopy}
                    aria-label={copied ? 'Copied' : 'Copy secret'}
                    className={cn(
                      'inline-flex items-center justify-center w-8 h-8 rounded-xs transition-colors',
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
                  </button>
                </div>
              </div>
            </div>

            <label className="flex items-start gap-sm cursor-pointer">
              <input
                type="checkbox"
                checked={confirmed}
                onChange={(e) => setConfirmed(e.target.checked)}
                className="mt-0.5"
              />
              <span className="text-body-sm text-on-surface">
                I've stored this key somewhere safe.
              </span>
            </label>
          </div>

          <div className="flex items-center justify-end gap-sm p-lg pt-0">
            <Button
              type="button"
              disabled={!confirmed}
              onClick={() => onOpenChange(false)}
            >
              <Lock className="w-4 h-4" aria-hidden="true" />
              Done
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

/* ----- Revoke confirmation ------------------------------------------ */

function RevokeApiKeyDialog({
  target,
  onOpenChange,
}: {
  target: ApiKey | null
  onOpenChange: (open: boolean) => void
}) {
  const remove = useDeleteApiKey()
  if (!target) return null

  const onConfirm = () => {
    remove.mutate(target.id, {
      onSuccess: () => {
        toast.success(`Revoked "${target.name}"`)
        onOpenChange(false)
      },
      onError: () => {
        toast.error("Couldn't revoke key")
      },
    })
  }

  return (
    <Dialog.Root open={!!target} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-surface-overlay backdrop-blur-sm" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-[440px] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-surface shadow-xl focus:outline-none">
          <div className="flex items-start justify-between p-lg border-b border-border">
            <div className="flex items-start gap-md">
              <span
                aria-hidden="true"
                className="inline-flex items-center justify-center w-9 h-9 rounded-sm bg-danger-100 text-danger-500"
              >
                <TriangleAlert className="w-[18px] h-[18px]" />
              </span>
              <div>
                <Dialog.Title className="text-heading-sm font-semibold text-on-surface">
                  Revoke API key
                </Dialog.Title>
                <Dialog.Description className="mt-xs text-body-sm text-on-surface-muted">
                  Any service still using this key will stop working
                  immediately. The action cannot be undone.
                </Dialog.Description>
              </div>
            </div>
            <Dialog.Close
              aria-label="Close"
              className="inline-flex items-center justify-center w-8 h-8 rounded-xs text-on-surface-muted hover:text-on-surface hover:bg-surface-muted transition-colors"
            >
              <X className="w-4 h-4" aria-hidden="true" />
            </Dialog.Close>
          </div>

          <div className="p-lg">
            <p className="text-body-sm text-on-surface">
              Revoking{' '}
              <strong className="font-semibold">{target.name}</strong>{' '}
              <span className="text-on-surface-muted">
                (prefix <code className="font-mono text-code-sm">{target.prefix}…</code>).
              </span>
            </p>
          </div>

          <div className="flex items-center justify-end gap-sm p-lg pt-0">
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="destructive"
              loading={remove.isPending}
              onClick={onConfirm}
            >
              Revoke key
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
