package contracts

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestProductContractPublishesCurrentIdentityAndCapacitySurface(t *testing.T) {
	root := filepath.Join("..", "..", "..", "contracts", "openapi")
	product := loadContractDocument(t, filepath.Join(root, "product.yaml"))
	sessions := loadContractDocument(t, filepath.Join(root, "modules", "iam-session.yaml"))
	administration := loadContractDocument(t, filepath.Join(root, "modules", "iam-administration.yaml"))
	files := loadContractDocument(t, filepath.Join(root, "modules", "files.yaml"))

	for _, path := range []string{
		"/iam/session/heartbeat",
		"/iam/session/renew",
		"/iam/administration/users/{userId}/deletion",
		"/iam/administration/users/{userId}/deletion/cancel",
	} {
		if product.Paths.Find(path) == nil {
			t.Errorf("product contract does not publish %s", path)
		}
	}

	if product.Paths.Find("/iam/administration/users/batch-delete") != nil {
		t.Fatal("removed user batch-delete path remains public")
	}
	for _, path := range []string{
		"/iam/administration/users/{userId}/organization",
		"/iam/administration/roles/{roleId}/data-scope",
		"/organization/departments",
		"/organization/positions",
		"/generator/tables",
	} {
		if product.Paths.Find(path) != nil {
			t.Fatalf("removed product path remains public: %s", path)
		}
	}
	userPath := administration.Paths.Find("/iam/administration/users/{userId}")
	if userPath == nil || userPath.Delete != nil {
		t.Fatal("removed single-user DELETE operation remains public")
	}

	current := sessions.Paths.Find("/iam/session/current")
	if current == nil || current.Get == nil {
		t.Fatal("current Session operation is missing")
	}
	currentResponse := current.Get.Responses.Value("200")
	if currentResponse == nil || currentResponse.Value == nil {
		t.Fatal("current Session success response is missing")
	}
	if _, rotates := currentResponse.Value.Headers["Set-Cookie"]; rotates {
		t.Fatal("read-only current Session still declares cookie rotation")
	}
	if strings.Contains(strings.ToLower(current.Get.Summary+" "+current.Get.Description), "refresh") {
		t.Fatal("read-only current Session still describes refresh-on-read")
	}

	login := sessions.Paths.Find("/iam/session/login")
	if login == nil || login.Post == nil || login.Post.Responses.Value("429") == nil {
		t.Fatal("login does not declare the stable rate-limit Problem")
	}
	for _, path := range []string{"/iam/session/heartbeat", "/iam/session/renew"} {
		operation := sessions.Paths.Find(path)
		if operation == nil || operation.Post == nil {
			t.Errorf("session module does not publish POST %s", path)
		}
	}

	scope := administration.Components.Schemas["DataScope"]
	if scope == nil || scope.Value == nil || !reflect.DeepEqual(scope.Value.Enum, []any{"all", "self"}) {
		t.Fatalf("data scope enum = %#v, want all/self scopes", scope)
	}
	deletion := administration.Paths.Find("/iam/administration/users/{userId}/deletion")
	if deletion == nil || deletion.Post == nil || deletion.Get == nil {
		t.Fatal("account deletion command and status operations must be published together")
	}

	upload := files.Paths.Find("/files/objects")
	if upload == nil || upload.Post == nil || upload.Post.Responses.Value("409") == nil || upload.Post.Responses.Value("507") == nil {
		t.Fatal("file upload does not declare stable quota and disk-capacity Problems")
	}
}

func loadContractDocument(t *testing.T, path string) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	document, err := loader.LoadFromFile(path)
	if err != nil {
		t.Fatalf("load OpenAPI contract %s: %v", path, err)
	}
	if err := document.Validate(loader.Context); err != nil {
		t.Fatalf("validate OpenAPI contract %s: %v", path, err)
	}
	return document
}
