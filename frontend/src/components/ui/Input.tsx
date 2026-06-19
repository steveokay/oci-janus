/**
 * Input — single-line text input, foundation for every form field.
 *
 * Variant rules:
 *   * default state uses the neutral border
 *   * focus uses the standard global focus ring (set in globals.css)
 *   * `error` flips border to danger so it stands out without an icon
 *
 * Always paired with a <Label> + (optional) <FieldHint> / <FieldError>
 * sibling. We don't bundle the label in because real forms need to
 * compose: a single label can describe an input + a button (e.g.
 * "Search" with submit button), and react-hook-form wants the
 * registered ref on the input itself.
 */
import { forwardRef, type InputHTMLAttributes } from 'react'
import { cn } from '@/lib/utils/cn'

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  /** Adds danger-coloured border so the error stands out. */
  error?: boolean
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, error, type = 'text', ...props },
  ref,
) {
  return (
    <input
      ref={ref}
      type={type}
      className={cn(
        // base
        'block w-full h-11 px-lg py-sm',
        'rounded-sm bg-surface text-body-md text-on-surface',
        'placeholder:text-on-surface-subtle',
        'border border-border',
        'transition-[border-color,box-shadow] duration-[120ms] ease-out',
        // hover (subtle — only visible when not focused)
        'hover:border-border-strong',
        // disabled
        'disabled:bg-surface-muted disabled:text-on-surface-subtle disabled:cursor-not-allowed',
        // error
        error && 'border-danger-500 hover:border-danger-500',
        className,
      )}
      {...props}
    />
  )
})

/** Form field label — picks up the label-md type token + appropriate spacing. */
export function Label({
  className,
  children,
  required,
  ...props
}: React.LabelHTMLAttributes<HTMLLabelElement> & { required?: boolean }) {
  return (
    <label
      className={cn(
        'block mb-sm text-label-md font-medium text-on-surface',
        className,
      )}
      {...props}
    >
      {children}
      {required && <span className="text-danger-500 ml-1" aria-hidden="true">*</span>}
    </label>
  )
}

/** Inline helper text shown under an input. */
export function FieldHint({ className, children }: { className?: string; children: React.ReactNode }) {
  return (
    <p className={cn('mt-sm text-label-sm text-on-surface-subtle', className)}>{children}</p>
  )
}

/** Inline error text shown under an input. role=alert for screen readers. */
export function FieldError({ className, children }: { className?: string; children: React.ReactNode }) {
  return (
    <p
      role="alert"
      className={cn('mt-sm text-label-sm text-danger-500', className)}
    >
      {children}
    </p>
  )
}
