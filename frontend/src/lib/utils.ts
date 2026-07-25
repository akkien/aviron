import type { CSSProperties } from "react"

import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

// Sets the --card-float-duration/-delay custom properties the ".card-floating"
// CSS animation (index.css) reads, so each card on a page can bob at its own
// pace instead of moving in lockstep.
export function floatStyle(durationS: number, delaySeconds: number): CSSProperties {
  return {
    "--card-float-duration": `${durationS}s`,
    "--card-float-delay": `${delaySeconds}s`,
  } as CSSProperties
}
