import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

// cn — combine class names with twMerge so utilities later in the chain win.
// Used by every UI primitive (Button, Card, …) for variant + override composition.
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
