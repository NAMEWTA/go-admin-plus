package administration

import (
	"context"
	"errors"

	transport "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration/transport"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/authorization"
)

func (s *HTTPServer) GetIamCapabilityManifest(ctx context.Context, _ transport.GetIamCapabilityManifestRequestObject) (transport.GetIamCapabilityManifestResponseObject, error) {
	value, err := s.service.Manifest(ctx, requestHTTP(ctx).actorID)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return transport.GetIamCapabilityManifest403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		return transport.GetIamCapabilityManifest500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.GetIamCapabilityManifest200JSONResponse{Body: transportManifest(value), Headers: transport.GetIamCapabilityManifest200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) ListIamRoles(ctx context.Context, _ transport.ListIamRolesRequestObject) (transport.ListIamRolesResponseObject, error) {
	values, err := s.service.ListRoles(ctx, requestHTTP(ctx).actorID)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return transport.ListIamRoles403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		return transport.ListIamRoles500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	body := make([]transport.Role, 0, len(values))
	for _, value := range values {
		body = append(body, transportRole(value))
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.ListIamRoles200JSONResponse{Body: body, Headers: transport.ListIamRoles200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) CreateIamRole(ctx context.Context, request transport.CreateIamRoleRequestObject) (transport.CreateIamRoleResponseObject, error) {
	if request.Body == nil {
		return transport.CreateIamRole400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	value, err := s.service.CreateRole(ctx, requestHTTP(ctx).actorID, request.Body.Key, request.Body.Name, authorization.Scope(request.Body.DataScope))
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.CreateIamRole400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.CreateIamRole403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.CreateIamRole409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.CreateIamRole500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.CreateIamRole201JSONResponse{Body: transportRole(value), Headers: transport.CreateIamRole201ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) UpdateIamRole(ctx context.Context, request transport.UpdateIamRoleRequestObject) (transport.UpdateIamRoleResponseObject, error) {
	if request.Body == nil {
		return transport.UpdateIamRole400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	err := s.service.UpdateRole(ctx, requestHTTP(ctx).actorID, Role{ID: request.RoleId, Key: request.Body.Key, Name: request.Body.Name, Scope: authorization.Scope(request.Body.DataScope), Enabled: request.Body.Enabled})
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.UpdateIamRole400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.UpdateIamRole403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrNotFound) {
			return transport.UpdateIamRole404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.UpdateIamRole409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.UpdateIamRole500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.UpdateIamRole204Response{Headers: transport.UpdateIamRole204ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) DeleteIamRole(ctx context.Context, request transport.DeleteIamRoleRequestObject) (transport.DeleteIamRoleResponseObject, error) {
	err := s.service.DeleteRole(ctx, requestHTTP(ctx).actorID, request.RoleId)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return transport.DeleteIamRole403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrNotFound) {
			return transport.DeleteIamRole404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.DeleteIamRole409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.DeleteIamRole500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.DeleteIamRole204Response{Headers: transport.DeleteIamRole204ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) SetIamRoleGrants(ctx context.Context, request transport.SetIamRoleGrantsRequestObject) (transport.SetIamRoleGrantsResponseObject, error) {
	if request.Body == nil {
		return transport.SetIamRoleGrants400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	err := s.service.SetRoleGrants(ctx, requestHTTP(ctx).actorID, request.RoleId, request.Body.PermissionCodes, request.Body.MenuIds)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.SetIamRoleGrants400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.SetIamRoleGrants403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrNotFound) {
			return transport.SetIamRoleGrants404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.SetIamRoleGrants409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.SetIamRoleGrants500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.SetIamRoleGrants204Response{Headers: transport.SetIamRoleGrants204ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) ListIamMenus(ctx context.Context, _ transport.ListIamMenusRequestObject) (transport.ListIamMenusResponseObject, error) {
	values, err := s.service.ListMenus(ctx, requestHTTP(ctx).actorID)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return transport.ListIamMenus403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		return transport.ListIamMenus500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	body := make([]transport.Menu, 0, len(values))
	for _, value := range values {
		body = append(body, transportMenu(value))
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.ListIamMenus200JSONResponse{Body: body, Headers: transport.ListIamMenus200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) CreateIamMenu(ctx context.Context, request transport.CreateIamMenuRequestObject) (transport.CreateIamMenuResponseObject, error) {
	if request.Body == nil {
		return transport.CreateIamMenu400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	value, err := s.service.CreateMenu(ctx, requestHTTP(ctx).actorID, menuInput("", *request.Body))
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.CreateIamMenu400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.CreateIamMenu403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.CreateIamMenu409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.CreateIamMenu500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.CreateIamMenu201JSONResponse{Body: transportMenu(value), Headers: transport.CreateIamMenu201ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) UpdateIamMenu(ctx context.Context, request transport.UpdateIamMenuRequestObject) (transport.UpdateIamMenuResponseObject, error) {
	if request.Body == nil {
		return transport.UpdateIamMenu400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	err := s.service.UpdateMenu(ctx, requestHTTP(ctx).actorID, menuInput(request.MenuId, *request.Body))
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.UpdateIamMenu400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.UpdateIamMenu403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrNotFound) {
			return transport.UpdateIamMenu404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.UpdateIamMenu409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.UpdateIamMenu500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.UpdateIamMenu204Response{Headers: transport.UpdateIamMenu204ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) DeleteIamMenu(ctx context.Context, request transport.DeleteIamMenuRequestObject) (transport.DeleteIamMenuResponseObject, error) {
	err := s.service.DeleteMenu(ctx, requestHTTP(ctx).actorID, request.MenuId)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return transport.DeleteIamMenu403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrNotFound) {
			return transport.DeleteIamMenu404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.DeleteIamMenu409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.DeleteIamMenu500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.DeleteIamMenu204Response{Headers: transport.DeleteIamMenu204ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) ListIamPermissions(ctx context.Context, _ transport.ListIamPermissionsRequestObject) (transport.ListIamPermissionsResponseObject, error) {
	values, err := s.service.ListPermissions(ctx, requestHTTP(ctx).actorID)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return transport.ListIamPermissions403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		return transport.ListIamPermissions500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	body := make([]transport.Permission, 0, len(values))
	for _, value := range values {
		body = append(body, transport.Permission{Code: value.Code, Name: value.Name})
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.ListIamPermissions200JSONResponse{Body: body, Headers: transport.ListIamPermissions200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func menuInput(id string, input transport.MenuInput) Menu {
	result := Menu{ID: id, Key: input.Key, Label: input.Label, Path: input.Path, PermissionCode: input.PermissionCode, SortOrder: input.SortOrder, Kind: "page", Enabled: true}
	if input.ParentId != nil {
		result.ParentID = *input.ParentId
	}
	if input.Kind != nil {
		result.Kind = string(*input.Kind)
	}
	if input.Icon != nil {
		result.Icon = *input.Icon
	}
	if input.Enabled != nil {
		result.Enabled = *input.Enabled
	}
	if input.Hidden != nil {
		result.Hidden = *input.Hidden
	}
	if input.Revision != nil {
		result.Revision = int64(*input.Revision)
	}
	return result
}
