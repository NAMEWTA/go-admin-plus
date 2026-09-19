package audit

import (
	"context"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/contracts/capabilities"
)

var moduleCapabilities = capabilities.ModuleCapabilities{
	Permissions: []capabilities.PermissionDefinition{
		{Code: string(PermissionRead), Name: "Read audit records"},
		{Code: string(PermissionCleanup), Name: "Clean up audit records"},
	},
	Menus: []capabilities.MenuDefinition{{
		ID: "menu-audit-records", Key: "audit-records", Label: "审计日志", Path: "/audit/records",
		PermissionCode: string(PermissionRead), SortOrder: 500,
	}},
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
