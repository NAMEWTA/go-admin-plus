export type SchedulerView = 'definitions' | 'executions'

const schedulerViews: Readonly<Record<string, SchedulerView>> = {
  '/scheduler/definitions': 'definitions',
  '/scheduler/executions': 'executions',
}

export const schedulerViewForPath = (path: string): SchedulerView | null => schedulerViews[path] ?? null
