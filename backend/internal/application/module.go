package application

import (
	"context"
	"net/http"
)

// Migration describes a module-owned migration without coupling the
// application kernel to a database implementation.
type Migration struct {
	ID string
}

// Module is the unit assembled by an Application. Route registration happens
// during Build; lifecycle hooks own only background resources.
type Module interface {
	ID() string
	RegisterRoutes(http.Handler) error
	Migrations() []Migration
	Start(context.Context) error
	Stop(context.Context) error
}

type ModuleSet struct {
	modules []Module
}

func NewModuleSet(modules ...Module) ModuleSet {
	return ModuleSet{modules: append([]Module(nil), modules...)}
}

func (s ModuleSet) Modules() []Module {
	return append([]Module(nil), s.modules...)
}
