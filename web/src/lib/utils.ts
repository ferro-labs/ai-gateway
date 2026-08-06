import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/**
 * Merges class names, resolving Tailwind conflicts in favour of the last one.
 *
 * Plain concatenation loses to specificity ties — `px-2` and `px-4` both apply
 * and the winner depends on stylesheet order rather than call order. twMerge
 * makes the caller's override win, which is what every component's `className`
 * prop implies.
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}
