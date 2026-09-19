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
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/bootstrap"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/session"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

const (
	desktopSetupPath         = "/__desktop/first-setup"
	desktopSetupMaximumBytes = 2048
	desktopSetupTimeout      = 30 * time.Second
)

type desktopSetupRequest struct {
	Action      string `json:"action"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Password    string `json:"password,omitempty"`
}

type desktopSetupProfile struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"displayName"`
	Email       string  `json:"email"`
	AvatarRef   *string `json:"avatarRef"`
}

type desktopSetupResponse struct {
	State        string               `json:"state"`
	Profile      *desktopSetupProfile `json:"profile,omitempty"`
	SessionToken string               `json:"sessionToken,omitempty"`
	CSRFToken    string               `json:"csrfToken,omitempty"`
}

type desktopSetup struct {
	database  *database.Database
	bootstrap func(context.Context, bootstrap.Command) error
	login     func(context.Context, string, string) (session.Issued, error)
}

func newDesktopSetup(db *database.Database, sessions *session.Service) *desktopSetup {
	setup := &desktopSetup{database: db}
	if db == nil || sessions == nil {
		return setup
	}
	service, err := bootstrap.NewService(db, operationalAudit{})
	if err == nil {
		setup.bootstrap = func(ctx context.Context, command bootstrap.Command) error {
			_, err := service.Bootstrap(ctx, command)
			return err
		}
		setup.login = sessions.Login
	}
	return setup
}

func newDesktopSetupPrivateRoute(db *database.Database, sessions *session.Service, path string) *desktophost.PrivateRoute {
	setup := newDesktopSetup(db, sessions)
	return &desktophost.PrivateRoute{
		Pattern: "POST " + path,
		Handler: http.HandlerFunc(setup.serveHTTP),
	}
}

func (setup *desktopSetup) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Content-Type") != "application/json" {
		writeDesktopSetup(writer, http.StatusBadRequest, desktopSetupResponse{State: "unavailable"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, desktopSetupMaximumBytes+1))
	if err != nil || len(payload) > desktopSetupMaximumBytes {
		writeDesktopSetup(writer, http.StatusBadRequest, desktopSetupResponse{State: "unavailable"})
		return
	}
	setup.servePayload(writer, request, payload)
}

func (setup *desktopSetup) servePayload(writer http.ResponseWriter, request *http.Request, payload []byte) {
	value, err := decodeDesktopSetupRequest(payload)
	if err != nil {
		writeDesktopSetup(writer, http.StatusBadRequest, desktopSetupResponse{State: "unavailable"})
		return
	}
	defer func() { value.Password = "" }()
	ctx, cancel := context.WithTimeout(request.Context(), desktopSetupTimeout)
	defer cancel()

	state, err := setup.state(ctx)
	if err != nil {
		writeDesktopSetup(writer, http.StatusServiceUnavailable, desktopSetupResponse{State: "unavailable"})
		return
	}
	if value.Action == "first-setup-state" {
		writeDesktopSetup(writer, http.StatusOK, desktopSetupResponse{State: state})
		return
	}
	if state != "required" {
		writeDesktopSetup(writer, http.StatusConflict, desktopSetupResponse{State: "login-required"})
		return
	}

	secret, err := bootstrap.ReadSecret(strings.NewReader(value.Password))
	if err != nil {
		writeDesktopSetup(writer, http.StatusBadRequest, desktopSetupResponse{State: "required"})
		return
	}
	command := bootstrap.Command{
		Username: value.Username, DisplayName: value.DisplayName, Email: value.Email, Secret: secret,
	}
	if setup.bootstrap == nil || setup.login == nil {
		writeDesktopSetup(writer, http.StatusServiceUnavailable, desktopSetupResponse{State: "unavailable"})
		return
	}
	if err := setup.bootstrap(ctx, command); err != nil {
		switch {
		case errors.Is(err, bootstrap.ErrAlreadyInitialized):
			writeDesktopSetup(writer, http.StatusConflict, desktopSetupResponse{State: "login-required"})
		case errors.Is(err, bootstrap.ErrInvalidArgument):
			writeDesktopSetup(writer, http.StatusBadRequest, desktopSetupResponse{State: "required"})
		default:
			writeDesktopSetup(writer, http.StatusInternalServerError, desktopSetupResponse{State: "unavailable"})
		}
		return
	}
	issued, err := setup.login(ctx, value.Username, value.Password)
	if err != nil {
		writeDesktopSetup(writer, http.StatusConflict, desktopSetupResponse{State: "login-required"})
		return
	}
	writeDesktopSetup(writer, http.StatusOK, desktopSetupResponse{
		State: "complete",
		Profile: &desktopSetupProfile{
			ID: issued.Profile.ID, Username: issued.Profile.Username, DisplayName: issued.Profile.DisplayName,
			Email: issued.Profile.Email, AvatarRef: issued.Profile.AvatarRef,
		},
		SessionToken: issued.Token,
		CSRFToken:    issued.CSRF,
	})
}

func decodeDesktopSetupRequest(payload []byte) (desktopSetupRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value desktopSetupRequest
	if err := decoder.Decode(&value); err != nil {
		return desktopSetupRequest{}, errors.New("invalid desktop setup request")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return desktopSetupRequest{}, errors.New("invalid desktop setup request")
	}
	switch value.Action {
	case "first-setup-state":
		if value.Username != "" || value.DisplayName != "" || value.Email != "" || value.Password != "" {
			return desktopSetupRequest{}, errors.New("invalid desktop setup request")
		}
	case "first-setup-submit":
		if value.Username == "" || value.DisplayName == "" || value.Email == "" || value.Password == "" {
			return desktopSetupRequest{}, errors.New("invalid desktop setup request")
		}
	default:
		return desktopSetupRequest{}, errors.New("invalid desktop setup request")
	}
	return value, nil
}

func (setup *desktopSetup) state(ctx context.Context) (string, error) {
	if setup == nil || setup.database == nil {
		return "", errors.New("desktop setup unavailable")
	}
	var accounts, markers int
	err := setup.database.SQL().QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM iam_accounts),
		(SELECT COUNT(*) FROM iam_bootstrap_state)`).Scan(&accounts, &markers)
	if err != nil {
		return "", err
	}
	switch {
	case accounts == 0 && markers == 0:
		return "required", nil
	case accounts > 0 && markers == 1:
		return "login-required", nil
	default:
		return "", errors.New("desktop setup state is inconsistent")
	}
}

func writeDesktopSetup(writer http.ResponseWriter, status int, response desktopSetupResponse) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(response)
}
