// Package capabilities defines immutable product capability values shared by business modules.
package capabilities

type PermissionDefinition struct{ Code, Name string }

type MenuDefinition struct {
	ID, Key, Label, Path, PermissionCode string
	SortOrder                            int
}

type ModuleCapabilities struct {
	Permissions []PermissionDefinition
	Menus       []MenuDefinition
}
