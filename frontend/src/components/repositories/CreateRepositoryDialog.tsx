/**
 * CreateRepositoryDialog — Radix Dialog wrapping a create-repo form.
 *
 * Form layout mirrors the API's `createRepositoryBody`:
 *   * Org (validated against ORG_NAME_PATTERN)
 *   * Repository name (validated against REPO_NAME_PATTERN, max length)
 *   * Visibility radio — Public vs Private (defaults Private)
 *
 * We surface validation errors via zod + react-hook-form so the user gets
 * inline feedback before round-tripping the API. The server re-validates;
 * any 400/403 from the API surfaces as a toast.
 *
 * Storage quota is intentionally hidden — most users want the default
 * (10 GB on dev). When tenant-level quota dials show up in Sprint 3 we'll
 * surface a quota override here too.
 */
import { useEffect } from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { zodResolver } from '@hookform/resolvers/zod'
import { toast } from 'sonner'
import { AxiosError } from 'axios'
import { X } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { Input, Label, FieldError, FieldHint } from '@/components/ui/Input'
import {
  ORG_NAME_PATTERN,
  REPO_NAME_PATTERN,
  REPO_NAME_MAX,
  useCreateRepository,
} from '@/lib/api/hooks/useRepositories'
import { cn } from '@/lib/utils/cn'

const createSchema = z.object({
  org: z
    .string()
    .min(2, 'Org must be 2–64 characters')
    .max(64, 'Org must be 2–64 characters')
    .regex(ORG_NAME_PATTERN, 'Lowercase letters, digits, and dashes only'),
  name: z
    .string()
    .min(1, 'Required')
    .max(REPO_NAME_MAX, `Max ${REPO_NAME_MAX} characters`)
    .regex(REPO_NAME_PATTERN, 'Lowercase letters, digits, with . _ - separators'),
  visibility: z.enum(['public', 'private']),
})

type FormInput = z.infer<typeof createSchema>

export interface CreateRepositoryDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Optional callback after a successful create — e.g. to navigate. */
  onCreated?: (fullName: string) => void
}

export function CreateRepositoryDialog({
  open,
  onOpenChange,
  onCreated,
}: CreateRepositoryDialogProps) {
  const createMutation = useCreateRepository()

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<FormInput>({
    resolver: zodResolver(createSchema),
    defaultValues: { org: '', name: '', visibility: 'private' },
  })

  // Reset the form whenever the dialog closes so the next open starts
  // fresh (no stale typing from a cancelled attempt).
  useEffect(() => {
    if (!open) reset()
  }, [open, reset])

  const onSubmit = (input: FormInput) => {
    createMutation.mutate(
      {
        org: input.org,
        name: input.name,
        is_public: input.visibility === 'public',
      },
      {
        onSuccess: (repo) => {
          toast.success(`Repository ${repo.name} created`)
          onOpenChange(false)
          onCreated?.(repo.name)
        },
        onError: (err) => {
          if (err instanceof AxiosError) {
            const msg = (err.response?.data as { error?: string } | undefined)?.error
            if (err.response?.status === 403) {
              toast.error("You don't have permission to create in that org")
              return
            }
            toast.error(msg ?? "Couldn't create repository")
            return
          }
          toast.error("Couldn't create repository")
        },
      },
    )
  }

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-surface-overlay backdrop-blur-sm data-[state=open]:animate-in data-[state=open]:fade-in-0" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-full max-w-[480px] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-surface shadow-xl focus:outline-none">
          <div className="flex items-start justify-between p-lg border-b border-border">
            <div>
              <Dialog.Title className="text-heading-sm font-semibold text-on-surface">
                New repository
              </Dialog.Title>
              <Dialog.Description className="mt-xs text-body-sm text-on-surface-muted">
                Repositories live under an organisation. Names are lowercase
                and can include dashes, dots, and underscores.
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
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-md">
              <div>
                <Label htmlFor="org">Organisation</Label>
                <Input
                  id="org"
                  type="text"
                  placeholder="acme"
                  autoComplete="off"
                  spellCheck={false}
                  error={!!errors.org}
                  {...register('org')}
                />
                {errors.org ? (
                  <FieldError>{errors.org.message}</FieldError>
                ) : (
                  <FieldHint>2–64 chars · letters, digits, dashes</FieldHint>
                )}
              </div>

              <div>
                <Label htmlFor="name">Repository name</Label>
                <Input
                  id="name"
                  type="text"
                  placeholder="webapp"
                  autoComplete="off"
                  spellCheck={false}
                  error={!!errors.name}
                  {...register('name')}
                />
                {errors.name ? (
                  <FieldError>{errors.name.message}</FieldError>
                ) : (
                  <FieldHint>Lowercase · separators: . _ -</FieldHint>
                )}
              </div>
            </div>

            <fieldset>
              <legend className="block mb-sm text-label-md font-medium text-on-surface">
                Visibility
              </legend>
              <div className="grid grid-cols-2 gap-sm">
                <VisibilityRadio
                  value="private"
                  label="Private"
                  description="Only members can pull"
                  {...register('visibility')}
                />
                <VisibilityRadio
                  value="public"
                  label="Public"
                  description="Anyone can pull"
                  {...register('visibility')}
                />
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
              <Button
                type="submit"
                variant="primary"
                loading={createMutation.isPending}
              >
                Create repository
              </Button>
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

/**
 * Custom radio rendered as a card with a label + description. Uses the
 * native input under the hood so react-hook-form's `register` wiring
 * Just Works — no need for a Controller wrapper.
 */
const VisibilityRadio = ({
  value,
  label,
  description,
  ...registerProps
}: {
  value: 'public' | 'private'
  label: string
  description: string
} & React.InputHTMLAttributes<HTMLInputElement>) => {
  return (
    <label
      className={cn(
        'group flex flex-col gap-xs px-md py-sm rounded-sm border border-border',
        'cursor-pointer hover:border-border-strong transition-colors',
        'has-[input:checked]:border-primary has-[input:checked]:bg-primary-soft',
      )}
    >
      <input
        type="radio"
        value={value}
        className="sr-only"
        {...registerProps}
      />
      <span className="text-body-sm font-medium text-on-surface">{label}</span>
      <span className="text-label-sm text-on-surface-muted">{description}</span>
    </label>
  )
}
