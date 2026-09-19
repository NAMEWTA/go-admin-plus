package files

import (
	"context"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
)

var moduleCapabilities = capabilities.ModuleCapabilities{
	Permissions: []capabilities.PermissionDefinition{
		{Code: PermissionFilesRead, Name: "Read files"},
		{Code: PermissionFilesWrite, Name: "Upload files"},
		{Code: PermissionFilesDelete, Name: "Delete files"},
	},
	Menus: []capabilities.MenuDefinition{{ID: "menu-files-objects", Key: "files-objects", Label: "文件管理", Path: "/files", PermissionCode: PermissionFilesRead, SortOrder: 900}},
}

type CapabilityRegistrar interface {
	Register(context.Context, capabilities.ModuleCapabilities) error
}

func RegisterCapabilities(ctx context.Context, registrar CapabilityRegistrar) error {
	if registrar == nil {
		return ErrInternal
	}
	return registrar.Register(ctx, capabilities.ModuleCapabilities{
		Permissions: append([]capabilities.PermissionDefinition(nil), moduleCapabilities.Permissions...),
		Menus:       append([]capabilities.MenuDefinition(nil), moduleCapabilities.Menus...),
	})
}
