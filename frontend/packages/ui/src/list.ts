export interface SortState {
  readonly key: string
  readonly direction: 'ascending' | 'descending'
}

export interface ListRequest<TFilters> {
  readonly filters: TFilters
  readonly page: number
  readonly pageSize: number
  readonly sort?: SortState
}

export interface ListResult<TRow> {
  readonly rows: ReadonlyArray<TRow>
  readonly total: number
}

export interface ListSnapshot<TFilters, TRow, TKey> extends ListRequest<TFilters> {
  readonly rows: ReadonlyArray<TRow>
  readonly total: number
  readonly selectedKeys: ReadonlyArray<TKey>
  readonly loading: boolean
}

export interface ListController<TFilters, TRow, TKey> {
  snapshot(): ListSnapshot<TFilters, TRow, TKey>
  refresh(): Promise<void>
  search(filters: TFilters): Promise<void>
  reset(): Promise<void>
  setPage(page: number): Promise<void>
  setPageSize(pageSize: number): Promise<void>
  setSort(sort?: SortState): Promise<void>
  select(rows: ReadonlyArray<TRow>): void
  clearSelection(): void
}

export interface ListControllerOptions<TFilters, TRow, TKey> {
  readonly initialFilters: () => TFilters
  readonly load: (request: ListRequest<TFilters>) => Promise<ListResult<TRow>>
  readonly normalizeFilters?: (filters: Readonly<TFilters>) => TFilters
  readonly validate?: (request: Readonly<ListRequest<TFilters>>) => void
  readonly rowKey: (row: TRow) => TKey
  readonly pageSize?: number
}

const requirePositiveInteger = (value: number, name: string) => {
  if (!Number.isSafeInteger(value) || value < 1) throw new RangeError(`${name} must be a positive integer`)
}

/** Owns deterministic filters, paging, sorting and selection for a management list. */
export const createListController = <TFilters extends object, TRow, TKey>(
  options: ListControllerOptions<TFilters, TRow, TKey>
): ListController<TFilters, TRow, TKey> => {
  let filters = options.initialFilters()
  let page = 1
  let pageSize = options.pageSize ?? 20
  let sort: SortState | undefined
  let rows: ReadonlyArray<TRow> = []
  let total = 0
  let selectedKeys: ReadonlyArray<TKey> = []
  let loading = false
  let requestSequence = 0
  requirePositiveInteger(pageSize, 'pageSize')

  const snapshot = (): ListSnapshot<TFilters, TRow, TKey> => ({
    filters: { ...filters },
    page,
    pageSize,
    ...(sort ? { sort: { ...sort } } : {}),
    rows: [...rows],
    total,
    selectedKeys: [...selectedKeys],
    loading
  })

  const execute = async (candidate: ListRequest<TFilters>, clearSelection = false): Promise<void> => {
    const sequence = ++requestSequence
    let request!: ListRequest<TFilters>
    try {
      const normalizedFilters = options.normalizeFilters?.({ ...candidate.filters }) ?? { ...candidate.filters }
      request = {
        filters: { ...normalizedFilters },
        page: candidate.page,
        pageSize: candidate.pageSize,
        ...(candidate.sort ? { sort: { ...candidate.sort } } : {})
      }
      options.validate?.(request)
    } catch (error) {
      if (sequence === requestSequence) loading = false
      throw error
    }
    loading = true
    try {
      const result = await options.load(request)
      if (sequence !== requestSequence) return
      filters = { ...request.filters }
      page = request.page
      pageSize = request.pageSize
      sort = request.sort ? { ...request.sort } : undefined
      rows = [...result.rows]
      total = result.total
      if (clearSelection) selectedKeys = []
    } catch (error) {
      // Replaced requests are no longer observable operations, including their failures.
      if (sequence !== requestSequence) return
      throw error
    } finally {
      if (sequence === requestSequence) loading = false
    }
  }

  return {
    snapshot,
    refresh: () => execute({ filters, page, pageSize, ...(sort ? { sort } : {}) }),
    async search(nextFilters) {
      await execute({ filters: nextFilters, page: 1, pageSize, ...(sort ? { sort } : {}) })
    },
    async reset() {
      await execute({ filters: options.initialFilters(), page: 1, pageSize }, true)
    },
    async setPage(nextPage) {
      requirePositiveInteger(nextPage, 'page')
      await execute({ filters, page: nextPage, pageSize, ...(sort ? { sort } : {}) })
    },
    async setPageSize(nextPageSize) {
      requirePositiveInteger(nextPageSize, 'pageSize')
      await execute({ filters, page: 1, pageSize: nextPageSize, ...(sort ? { sort } : {}) })
    },
    async setSort(nextSort) {
      await execute({ filters, page: 1, pageSize, ...(nextSort ? { sort: nextSort } : {}) })
    },
    select(nextRows) {
      selectedKeys = nextRows.map(options.rowKey)
    },
    clearSelection() {
      selectedKeys = []
    }
  }
}
