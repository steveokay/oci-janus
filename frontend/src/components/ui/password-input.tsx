import * as React from "react";
import { Eye, EyeOff } from "lucide-react";
import { cn } from "@/lib/utils";
import { Input, type InputProps } from "@/components/ui/input";

// PasswordInput — a drop-in replacement for the base `Input` that renders a
// masked password field with an inline "show password" reveal toggle
// (FUT-079). It forwards its ref to the underlying <input> so it stays
// compatible with react-hook-form's `register()` and any imperative focus
// callers.
//
// Behaviour + accessibility contract:
//   * The eye / eye-off button is `type="button"` so it never submits the
//     surrounding form.
//   * Its `aria-label` flips between "Show password" and "Hide password" so
//     screen-reader users get the current action, and it is keyboard-focusable
//     like any native button.
//   * Toggling swaps the input's `type` between "password" and "text"; the
//     caller must NOT pass `type` — this component owns it.
//
// Callers pass through every other native input prop (value, onChange,
// placeholder, required, autoComplete, id, name, aria-invalid, ...) exactly as
// they would to `Input`.
export type PasswordInputProps = Omit<InputProps, "type">;

export const PasswordInput = React.forwardRef<
  HTMLInputElement,
  PasswordInputProps
>(function PasswordInput({ className, ...props }, ref) {
  // Local visibility state — starts masked. Each field owns its own toggle so
  // revealing one password never reveals another on the same form.
  const [visible, setVisible] = React.useState(false);

  return (
    <div className="relative">
      <Input
        ref={ref}
        // Reveal simply swaps the native input type; the browser handles the
        // masking so the raw value never leaves the field.
        type={visible ? "text" : "password"}
        // Reserve room on the right so the toggle button doesn't overlap the
        // typed value (or the browser's own reveal/clear affordances).
        className={cn("pr-10", className)}
        {...props}
      />
      <button
        // type="button" is load-bearing: without it, clicking the toggle would
        // submit the form it sits inside.
        type="button"
        // Flip the label with the state so assistive tech announces the action
        // the click will perform, not the current state.
        aria-label={visible ? "Hide password" : "Show password"}
        // `aria-pressed` communicates the toggle's on/off state to screen
        // readers as a bonus over the label alone.
        aria-pressed={visible}
        onClick={() => setVisible((v) => !v)}
        className={cn(
          "absolute inset-y-0 right-0 flex items-center px-3",
          "text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]",
          "focus-visible:outline-none focus-visible:text-[var(--color-accent)]",
          "transition-colors",
        )}
        // Keep the button out of the tab order's way when the field is disabled
        // so a disabled password field can't be revealed.
        tabIndex={props.disabled ? -1 : 0}
        disabled={props.disabled}
      >
        {visible ? (
          <EyeOff className="size-4" aria-hidden />
        ) : (
          <Eye className="size-4" aria-hidden />
        )}
      </button>
    </div>
  );
});
