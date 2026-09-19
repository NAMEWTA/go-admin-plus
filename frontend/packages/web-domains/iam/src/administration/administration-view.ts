export type AdministrationView = 'users' | 'roles' | 'menus'

const administrationViews = new Map<string, AdministrationView>([
  ['/iam/users', 'users'],
  ['/iam/roles', 'roles'],
  ['/iam/menus', 'menus'],
])

export const administrationViewForPath = (path: string): AdministrationView | null =>
  administrationViews.get(path) ?? null
