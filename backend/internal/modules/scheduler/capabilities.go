package scheduler

import (
	"context"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
)

var moduleCapabilities = capabilities.ModuleCapabilities{
	Permissions: []capabilities.PermissionDefinition{
		{Code: PermissionDefinitionsRead, Name: "Read scheduler definitions"},
		{Code: PermissionDefinitionsWrite, Name: "Manage scheduler definitions"},
		{Code: PermissionDefinitionsDelete, Name: "Delete scheduler definitions"},
		{Code: PermissionExecutionsRead, Name: "Read scheduler execution history"},
	},
	Menus: []capabilities.MenuDefinition{
		{ID: "menu-scheduler-definitions", Key: "scheduler-definitions", Label: "任务调度", Path: "/scheduler/definitions", PermissionCode: PermissionDefinitionsRead, SortOrder: 700},
		{ID: "menu-scheduler-executions", Key: "scheduler-executions", Label: "执行记录", Path: "/scheduler/executions", PermissionCode: PermissionExecutionsRead, SortOrder: 710},
	},
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
