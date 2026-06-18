/**
 * cn — tiny utility for merging Tailwind class strings.
 *
 *   clsx       handles conditional classes (e.g. `{ "bg-primary": active }`)
 *   twMerge    deduplicates conflicting Tailwind utilities (the last one wins),
 *              so a Button consumer can pass `className="bg-red-500"` to
 *              override the default `bg-primary` without specificity wars.
 *
 * Used by every component that accepts a className prop.
 */
import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
