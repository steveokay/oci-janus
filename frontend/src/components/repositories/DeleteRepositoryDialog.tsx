/**
 * DeleteRepositoryDialog — GitHub-style "type the name to confirm" delete.
 *
 * Repository deletes are irreversible — they tombstone the metadata row
 * and queue blob GC. So we require the user to type the full `org/repo`
 * name before the Delete button enables. Same friction GitHub uses for
 * the equivalent action.
 *
 * The confirmation text is compared verbatim (case-sensitive) — typing
 * the name back exactly proves the user knows what they're deleting.
 */
import { useEffect, useState } from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { toast } from 'sonner'
import { AxiosError } from 'axios'
import { TriangleAlert, X } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { Input, Label, FieldHint } from '@/components/ui/Input'
import { useDeleteRepository } from '@/lib/api/hooks/useRepositories'

export interface DeleteRepositoryDialogProps {
  /** Full "org/repo" name; null hides the dialog. */
  target: string | null
  onOpenChange: (open: boolean) => void
}

export function DeleteRepositoryDialog({
  target,
  onOpenChange,
}: DeleteRepositoryDialogProps) {
  const deleteMutation = useDeleteRepository()
  const [typed, setTyped] = useState('')
  const matches = !!target && typed === target

  // Reset the input whenever the dialog opens against a new target so
  // the input doesn't keep stale text from a previous attempt.
  useEffect(() => {
    if (target) setTyped('')
  }, [target])

  if (!target) return null

  const onConfirm = () => {
    deleteMutation.mutate(target, {
      onSuccess: () => {
        toast.success(`Repository ${target} deleted`)
        onOpenChange(false)
      },
      onError: (err) => {
        if (err instanceof AxiosError) {
          if (err.response?.status === 403) {
            toast.error("You don't have permission to delete this repository")
            return
          }
          if (err.response?.status === 404) {
            toast.error('Repository not found')
            return
          }
        }
        toast.error("Couldn't delete repository")
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
                  Delete repository
                </Dialog.Title>
                <Dialog.Description className="mt-xs text-body-sm text-on-surface-muted">
                  This will remove all tags, manifests, and blobs. The action
                  cannot be undone.
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

          <div className="p-lg space-y-md">
            <Label htmlFor="delete-confirm">
              Type{' '}
              <code className="font-mono text-code-sm bg-surface-muted px-xs rounded-xs">
                {target}
              </code>{' '}
              to confirm
            </Label>
            <Input
              id="delete-confirm"
              type="text"
              autoComplete="off"
              spellCheck={false}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className="font-mono text-code-sm"
            />
            <FieldHint>This confirmation is case-sensitive.</FieldHint>
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
              disabled={!matches}
              loading={deleteMutation.isPending}
              onClick={onConfirm}
            >
              Delete repository
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
