import { Fragment, type Key, type ReactNode } from 'react'

import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { cn } from '@/lib/utils'
import { EmptyState, Pagination } from './ui'

export interface DataTableColumn<T> {
  id: string
  header: ReactNode
  mobileLabel?: string
  render: (row: T, index: number) => ReactNode
  headerClassName?: string
  cellClassName?: string
  volatile?: boolean
}

export interface DataTablePagination {
  offset: number
  pageSize: number
  returned: number
  total: number
  busy?: boolean
  note?: string
  onOffsetChange: (offset: number) => void
}

interface DataTableProps<T> {
  ariaLabel: string
  columns: readonly DataTableColumn<T>[]
  rows: readonly T[]
  rowKey: (row: T, index: number) => Key
  renderSubRow?: (row: T, index: number) => ReactNode
  title: string
  description?: ReactNode
  emptyTitle: string
  emptyDescription: string
  loading?: boolean
  loadingLabel?: string
  skeletonRows?: number
  pagination?: DataTablePagination
}

const mobileRow =
  'max-sm:mb-2 max-sm:block max-sm:rounded-lg max-sm:border max-sm:border-border max-sm:p-2.5 max-sm:last:mb-0'

const mobileCell =
  'max-sm:flex max-sm:items-baseline max-sm:justify-between max-sm:gap-3 max-sm:border-b max-sm:border-border/60 max-sm:py-1 max-sm:last:border-0 ' +
  'max-sm:before:shrink-0 max-sm:before:text-[11px] max-sm:before:font-semibold max-sm:before:tracking-wide max-sm:before:text-muted-foreground max-sm:before:uppercase max-sm:before:content-[attr(data-label)]'

function DataTableSkeleton<T>({
  columns,
  label,
  rows,
}: {
  columns: readonly DataTableColumn<T>[]
  label: string
  rows: number
}) {
  return (
    <div aria-label={label} role="status">
      <span className="sr-only">{label}</span>
      <Table aria-hidden="true" className="max-sm:block">
        <TableHeader className="max-sm:hidden">
          <TableRow>
            {columns.map((column) => (
              <TableHead className={column.headerClassName} key={column.id}>
                {column.header}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody className="max-sm:block">
          {Array.from({ length: rows }, (_, rowIndex) => (
            <TableRow className={mobileRow} key={rowIndex}>
              {columns.map((column, columnIndex) => (
                <TableCell
                  className={cn(column.mobileLabel !== undefined && mobileCell, column.cellClassName)}
                  data-label={column.mobileLabel}
                  key={column.id}
                >
                  <Skeleton
                    className={cn(
                      'h-4',
                      columnIndex === columns.length - 1 ? 'w-14' : 'w-full max-w-24',
                    )}
                  />
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

/**
 * A reusable, server-paginated table surface.
 *
 * Pages own query state and server sorting/filtering. This component owns the
 * consistent table anatomy, responsive row treatment, loading geometry, empty
 * state, and offset controls. Keeping those concerns separate makes it possible
 * to put TanStack Table behind the column contract later without coupling API
 * queries to a rendering library now.
 */
export function DataTable<T>({
  ariaLabel,
  columns,
  rows,
  rowKey,
  renderSubRow,
  title,
  description,
  emptyTitle,
  emptyDescription,
  loading = false,
  loadingLabel = 'Loading table',
  skeletonRows = 6,
  pagination,
}: DataTableProps<T>) {
  return (
    <section aria-busy={loading} aria-label={title} className="overflow-hidden rounded-lg border border-border bg-card">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 border-b border-border px-4 py-2.5">
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        {description ? <p className="text-xs text-muted-foreground">{description}</p> : null}
      </div>

      {loading && rows.length === 0 ? (
        <DataTableSkeleton columns={columns} label={loadingLabel} rows={skeletonRows} />
      ) : rows.length === 0 ? (
        <div className="p-3">
          <EmptyState description={emptyDescription} title={emptyTitle} />
        </div>
      ) : (
        <Table aria-label={ariaLabel} className="max-sm:block">
          <TableHeader className="max-sm:hidden">
            <TableRow>
              {columns.map((column) => (
                <TableHead className={column.headerClassName} key={column.id}>
                  {column.header}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody className="max-sm:block">
            {rows.map((row, index) => {
              const subRow = renderSubRow?.(row, index)
              return (
                <Fragment key={rowKey(row, index)}>
                  <TableRow className={mobileRow}>
                    {columns.map((column) => (
                      <TableCell
                        className={cn(column.mobileLabel !== undefined && mobileCell, column.cellClassName)}
                        data-label={column.mobileLabel}
                        data-volatile={column.volatile || undefined}
                        key={column.id}
                      >
                        {column.render(row, index)}
                      </TableCell>
                    ))}
                  </TableRow>
                  {subRow ? (
                    <TableRow className="max-sm:mb-2 max-sm:block max-sm:rounded-lg max-sm:border max-sm:border-border max-sm:p-2.5">
                      <TableCell className="bg-muted/40 max-sm:block max-sm:bg-transparent" colSpan={columns.length}>
                        {subRow}
                      </TableCell>
                    </TableRow>
                  ) : null}
                </Fragment>
              )
            })}
          </TableBody>
        </Table>
      )}

      {pagination ? <Pagination {...pagination} /> : null}
    </section>
  )
}
