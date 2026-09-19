import type { DefineComponent } from 'vue'

type Component<Props> = DefineComponent<Props>
type TableRow = Record<string, unknown>

export const AppPage: Component<{
  title: string
  description?: string
  busy?: boolean
  error?: string
}>

export const QueryBar: Component<{
  busy?: boolean
  resetDisabled?: boolean
}>

export const TableToolbar: Component<{
  selectedCount?: number
  busy?: boolean
}>

export const EmptyState: Component<{
  title?: string
  actionLabel?: string
}>

export const DataTable: Component<{
  rows: ReadonlyArray<TableRow>
  rowKey?: string | ((row: TableRow) => string)
  loading?: boolean
  error?: string
  emptyTitle?: string
  label?: string
}>

export const FormGrid: Component<{ columns?: 1 | 2 | 3 }>

export const FormDialog: Component<{
  modelValue: boolean
  title: string
  busy?: boolean
  danger?: boolean
  submitLabel?: string
  width?: string | number
}>

export const StatusTag: Component<{
  tone?: 'success' | 'warning' | 'danger' | 'info' | 'neutral'
  label: string
}>

export const Pagination: Component<{
  page: number
  pageSize: number
  total: number
  disabled?: boolean
  pageSizes?: number[]
}>
