//go:build desktop_native_e2e

package product

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	desktophost "github.com/NAMEWTA/go-admin-plus/backend/internal/host/desktop"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/files"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const desktopControlTimeout = 15 * time.Second

type desktopNativeAction struct {
	Action string `json:"action"`
}

type desktopNativeControl struct {
	database *database.Database
	sessions *session.Service
	setup    *desktopSetup
}

var (
	errDesktopControlDependencies = errors.New("desktop test control dependencies unavailable")
	errDesktopControlSentinel     = errors.New("desktop test control scope sentinel failed")
	errDesktopControlScopeUpdate  = errors.New("desktop test control scope update failed")
	errDesktopControlScopeRows    = errors.New("desktop test control scope rows failed")
)

func desktopPrivateRoute(db *database.Database, sessions *session.Service) *desktophost.PrivateRoute {
	control := &desktopNativeControl{database: db, sessions: sessions, setup: newDesktopSetup(db, sessions)}
	return &desktophost.PrivateRoute{
		Pattern: "POST /__desktop/test-control",
		Handler: http.HandlerFunc(control.serveHTTP),
	}
}

func decodeDesktopNativeAction(request *http.Request) (string, error) {
	if request.Header.Get("Content-Type") != "application/json" {
		return "", errors.New("invalid desktop test control request")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 129))
	decoder.DisallowUnknownFields()
	var value desktopNativeAction
	if err := decoder.Decode(&value); err != nil {
		return "", errors.New("invalid desktop test control request")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("invalid desktop test control request")
	}
	switch value.Action {
	case "scope-self", "scope-all", "permissions-off", "permissions-on", "session-revoke":
		return value.Action, nil
	default:
		return "", errors.New("invalid desktop test control request")
	}
}

func decodeDesktopNativeActionPayload(payload []byte) (string, error) {
	request, err := http.NewRequest(http.MethodPost, "/__desktop/test-control", bytes.NewReader(payload))
	if err != nil {
		return "", errors.New("invalid desktop test control request")
	}
	request.Header.Set("Content-Type", "application/json")
	return decodeDesktopNativeAction(request)
}

func (control *desktopNativeControl) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Content-Type") != "application/json" {
		writer.WriteHeader(http.StatusGatewayTimeout)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, desktopSetupMaximumBytes+1))
	if err != nil || len(payload) > desktopSetupMaximumBytes {
		writer.WriteHeader(http.StatusGatewayTimeout)
		return
	}
	var envelope desktopNativeAction
	if json.Unmarshal(payload, &envelope) == nil && strings.HasPrefix(envelope.Action, "first-setup-") {
		control.setup.servePayload(writer, request, payload)
		return
	}
	action, err := decodeDesktopNativeActionPayload(payload)
	if err != nil {
		writer.WriteHeader(http.StatusGatewayTimeout)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), desktopControlTimeout)
	defer cancel()
	if err := control.apply(ctx, action); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, errDesktopControlDependencies):
			status = http.StatusHTTPVersionNotSupported
		case errors.Is(err, errDesktopControlSentinel):
			status = http.StatusNotImplemented
		case errors.Is(err, errDesktopControlScopeUpdate):
			status = http.StatusBadGateway
		case errors.Is(err, errDesktopControlScopeRows):
			status = http.StatusServiceUnavailable
		}
		writer.WriteHeader(status)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (control *desktopNativeControl) apply(ctx context.Context, action string) error {
	if control.database == nil || control.sessions == nil {
		return errDesktopControlDependencies
	}
	switch action {
	case "scope-self", "scope-all":
		scope := strings.TrimPrefix(action, "scope-")
		if action == "scope-self" {
			if _, err := control.database.Bun().ExecContext(ctx, `INSERT INTO files_objects
				(id, owner_account_id, original_name, name_key, media_type, size_bytes, sha256, storage_key, state, revision, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
				ON CONFLICT(id) DO NOTHING`, "00000000-0000-4000-8000-000000000001", "account-native-other",
				"foreign.txt", "foreign.txt", "text/plain", 1, strings.Repeat("0", 64), "object-"+strings.Repeat("0", 32), "ready", 1); err != nil {
				return errDesktopControlSentinel
			}
		}
		result, err := control.database.Bun().ExecContext(ctx, `UPDATE iam_roles SET data_scope = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, scope, "role-system-admin")
		if err != nil {
			return errDesktopControlScopeUpdate
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errDesktopControlScopeRows
		}
		return nil
	case "permissions-off":
		result, err := control.database.Bun().ExecContext(ctx, `DELETE FROM iam_role_permissions WHERE role_id = ? AND permission_code IN (?, ?, ?)`, "role-system-admin", files.PermissionFilesRead, files.PermissionFilesWrite, files.PermissionFilesDelete)
		if err != nil {
			return errors.New("desktop test control failed")
		}
		count, err := result.RowsAffected()
		if err != nil || count != 3 {
			return errors.New("desktop test control failed")
		}
		return nil
	case "permissions-on":
		registry, err := authorization.NewCapabilityRegistry(control.database)
		if err != nil {
			return errors.New("desktop test control failed")
		}
		if err := files.RegisterCapabilities(ctx, registry); err != nil {
			return errors.New("desktop test control failed")
		}
		return nil
	case "session-revoke":
		if err := control.sessions.RevokeAccount(ctx, "account-desktop-e2e"); err != nil {
			return errors.New("desktop test control failed")
		}
		return nil
	default:
		return errors.New("desktop test control unavailable")
	}
}
