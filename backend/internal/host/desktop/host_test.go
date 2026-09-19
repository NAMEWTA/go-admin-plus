package desktop

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestDatabaseStartupFailureAlwaysClosesBeforeRestoreAndReportsRecoveryFault(t *testing.T) {
	var order []string
	err := databaseStartupFailure(func() error {
		order = append(order, "close")
		return nil
	}, func() error {
		order = append(order, "restore")
		return nil
	}, "desktop database migration failed")
	if err.Error() != "desktop database migration failed" || fmt.Sprint(order) != "[close restore]" {
		t.Fatalf("successful recovery = %v order=%v", err, order)
	}
	order = nil
	err = databaseStartupFailure(nil, func() error {
		order = append(order, "restore")
		return errors.New("injected restore fault")
	}, "desktop database open failed")
	if err.Error() != "desktop database recovery failed" || fmt.Sprint(order) != "[restore]" {
		t.Fatalf("failed recovery = %v order=%v", err, order)
	}
	order = nil
	err = databaseStartupFailure(func() error {
		order = append(order, "close")
		return errors.New("injected close fault")
	}, func() error {
		order = append(order, "restore")
		return nil
	}, "desktop database migration failed")
	if err.Error() != "desktop database recovery failed" || fmt.Sprint(order) != "[close]" {
		t.Fatalf("close fault recovery = %v order=%v", err, order)
	}
}

func TestPrivateRouteRequiresExactDesktopPostPattern(t *testing.T) {
	for _, route := range []PrivateRoute{
		{},
		{Pattern: "GET /__desktop/private", Handler: handlerStub{}},
		{Pattern: "POST /api/private", Handler: handlerStub{}},
		{Pattern: "POST /__desktop/", Handler: handlerStub{}},
		{Pattern: "POST /__desktop/{name}", Handler: handlerStub{}},
		{Pattern: "POST /__desktop/ready", Handler: handlerStub{}},
		{Pattern: "POST /__desktop/shutdown", Handler: handlerStub{}},
		{Pattern: "POST /__desktop/private ", Handler: handlerStub{}},
	} {
		if err := validatePrivateRoute(route); err == nil {
			t.Fatalf("accepted invalid private route %#v", route)
		}
	}
	if err := validatePrivateRoute(PrivateRoute{Pattern: "POST /__desktop/private", Handler: handlerStub{}}); err != nil {
		t.Fatalf("valid private route: %v", err)
	}
}

type handlerStub struct{}

func (handlerStub) ServeHTTP(http.ResponseWriter, *http.Request) {}
