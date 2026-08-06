import type { ComponentProps } from 'react'

import { cn } from '@/lib/utils'

function Skeleton({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      className={cn('animate-pulse rounded-md bg-muted', className)}
      data-slot="skeleton"
      {...props}
    />
  )
}

export { Skeleton }
