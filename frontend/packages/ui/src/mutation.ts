export type FormRunResult = 'submitted' | 'invalid' | 'busy' | 'failed' | 'refresh-failed'

export interface FormController<TModel> {
  readonly busy: boolean
  run(model: Readonly<TModel>): Promise<FormRunResult>
}

/** Guards validation and submission as one operation so repeated input cannot duplicate writes. */
export const createFormController = <TModel>(options: {
  readonly validate: (model: Readonly<TModel>) => Promise<boolean>
  readonly submit: (model: Readonly<TModel>) => Promise<void>
  readonly submitted?: () => Promise<void> | void
  readonly failed?: () => Promise<void> | void
}): FormController<TModel> => {
  let inFlight = false
  const notifyFailed = async () => {
    try {
      await options.failed?.()
    } catch {
      // Failure observers cannot change the command result.
    }
  }
  return {
    get busy() { return inFlight },
    async run(model) {
      if (inFlight) return 'busy'
      inFlight = true
      try {
        try {
          if (!await options.validate(model)) return 'invalid'
          await options.submit(model)
        } catch {
          await notifyFailed()
          return 'failed'
        }

        try {
          await options.submitted?.()
          return 'submitted'
        } catch {
          // The write completed; callers must repair UI state instead of retrying the command.
          return 'refresh-failed'
        }
      } finally {
        inFlight = false
      }
    }
  }
}

export type RemovalRunResult =
  | 'completed'
  | 'cancelled'
  | 'empty'
  | 'busy'
  | 'failed'
  | 'refresh-failed'

export interface RemovalController<TKey> {
  readonly busy: boolean
  run(keys: ReadonlyArray<TKey>): Promise<RemovalRunResult>
}

/** Guards confirmation and execution as one destructive operation and refreshes after success. */
export const createRemovalController = <TKey>(options: {
  readonly confirm: (count: number) => Promise<boolean>
  readonly execute: (keys: ReadonlyArray<TKey>) => Promise<void>
  readonly refreshed: () => Promise<void>
  readonly clearSelection: () => void
  readonly failed?: () => Promise<void> | void
}): RemovalController<TKey> => {
  let inFlight = false
  const notifyFailed = async () => {
    try {
      await options.failed?.()
    } catch {
      // Failure observers cannot change the command result.
    }
  }
  return {
    get busy() { return inFlight },
    async run(keys) {
      if (keys.length === 0) return 'empty'
      if (inFlight) return 'busy'
      inFlight = true
      try {
        try {
          if (!await options.confirm(keys.length)) return 'cancelled'
          await options.execute([...keys])
        } catch {
          await notifyFailed()
          return 'failed'
        }

        let refreshFailed = false
        try {
          options.clearSelection()
        } catch {
          refreshFailed = true
        }
        try {
          await options.refreshed()
        } catch {
          refreshFailed = true
        }
        // The write completed; callers must repair UI state instead of retrying the command.
        return refreshFailed ? 'refresh-failed' : 'completed'
      } finally {
        inFlight = false
      }
    }
  }
}
