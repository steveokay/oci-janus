/**
 * Button — the only button primitive in the app.
 *
 * Variants follow the visual hierarchy we use across screens:
 *   primary   : the one action you most want the user to take on a screen
 *   secondary : an alternative action of equal weight
 *   ghost     : a quiet tertiary action (cancel, "show more", etc.)
 *   destructive: deletes / removes things — uses danger palette
 *   link      : looks like a text link, behaves like a button
 *
 * Sizes line up with the type scale so a Button next to body text always
 * looks proportional.
 */
import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils/cn'

const buttonStyles = cva(
  // base: layout, font, focus ring, disabled, transition
  [
    'inline-flex items-center justify-center gap-sm',
    'font-medium',
    'rounded-sm',
    'whitespace-nowrap',
    'transition-[background-color,color,box-shadow,transform]',
    'duration-[120ms] ease-out',
    'select-none',
    'disabled:opacity-50 disabled:cursor-not-allowed disabled:pointer-events-none',
    'active:scale-[0.98]',
  ],
  {
    variants: {
      variant: {
        primary: [
          'bg-primary text-on-primary',
          'hover:bg-primary-600',
          'active:bg-primary-700',
          'shadow-xs hover:shadow-sm',
        ],
        secondary: [
          'bg-surface text-on-surface',
          'border border-border-strong',
          'hover:bg-surface-muted',
          'shadow-xs',
        ],
        ghost: [
          'bg-transparent text-on-surface-muted',
          'hover:bg-surface-muted hover:text-on-surface',
        ],
        destructive: [
          'bg-danger-500 text-white',
          'hover:brightness-110',
          'shadow-xs hover:shadow-sm',
        ],
        link: [
          'bg-transparent text-primary underline-offset-4',
          'hover:underline',
          'p-0 h-auto',
        ],
      },
      size: {
        sm: ['h-8 px-md text-label-md'],
        md: ['h-10 px-lg text-body-sm'],
        lg: ['h-11 px-xl text-body-md'],
        icon: ['h-10 w-10'],
      },
      fullWidth: {
        true: 'w-full',
      },
    },
    defaultVariants: {
      variant: 'primary',
      size: 'md',
    },
  },
)

export interface ButtonProps
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonStyles> {
  /** Show an inline spinner and disable the button — used during async submits. */
  loading?: boolean
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { className, variant, size, fullWidth, loading, disabled, children, ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      className={cn(buttonStyles({ variant, size, fullWidth }), className)}
      disabled={disabled || loading}
      {...props}
    >
      {loading && <Spinner />}
      {children}
    </button>
  )
})

/** Inline spinner — currentColor so it inherits the button's text color. */
function Spinner() {
  return (
    <svg
      className="h-4 w-4 animate-spin"
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="10" stroke="currentColor" strokeOpacity="0.25" strokeWidth="3" />
      <path
        d="M22 12a10 10 0 0 0-10-10"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
      />
    </svg>
  )
}
