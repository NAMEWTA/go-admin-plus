use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::proxy::{DesktopRequest, DesktopResponse};

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct BinaryUpload {
    encoding: String,
    name: String,
    media_type: String,
    data: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct UploadBody {
    pub name: String,
    pub media_type: String,
    pub data: String,
}

pub struct ValidatedRequest {
    pub revision: Option<u64>,
    pub path: String,
    pub method: String,
    pub body: Option<Value>,
    pub upload: Option<UploadBody>,
    pub binary_response: bool,
    pub allows_sensitive_input: bool,
}

#[derive(Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct Problem {
    r#type: String,
    title: String,
    status: u16,
    category: String,
    code: String,
    trace_id: String,
}

pub fn validate_request(request: DesktopRequest) -> Result<ValidatedRequest, &'static str> {
    let DesktopRequest {
        path,
        method,
        body,
        revision,
    } = request;
    let path_only = path
        .split_once('?')
        .map_or(path.as_str(), |(value, _)| value);
    if !allowed(path_only, &method) {
        return Err("desktop request is not allowed");
    }
    let upload = if path_only == "/files/objects" && method == "POST" {
        let value: BinaryUpload =
            serde_json::from_value(body.clone().ok_or("desktop upload body invalid")?)
                .map_err(|_| "desktop upload body invalid")?;
        if value.encoding != "base64"
            || value.name.is_empty()
            || value.name.len() > 255
            || value.name.contains(['/', '\\', '\r', '\n', '"'])
            || !matches!(
                value.media_type.as_str(),
                "image/png" | "image/jpeg" | "application/pdf" | "text/plain"
            )
            || value.data.is_empty()
        {
            return Err("desktop upload body invalid");
        }
        Some(UploadBody {
            name: value.name,
            media_type: value.media_type,
            data: value.data,
        })
    } else {
        None
    };
    let allows_sensitive_input = sensitive_input_path(path_only, &method);
    Ok(ValidatedRequest {
        revision,
        binary_response: path_only.starts_with("/files/objects/")
            && path_only.ends_with("/content")
            && method == "GET",
        path,
        method,
        body,
        upload,
        allows_sensitive_input,
    })
}

fn sensitive_input_path(path: &str, method: &str) -> bool {
    if path == "/iam/account/password" && method == "PUT"
        || path == "/iam/administration/users" && method == "POST"
    {
        return true;
    }
    let segments: Vec<_> = path.trim_start_matches('/').split('/').collect();
    matches!(segments.as_slice(), ["iam", "administration", "users", id, "password"] if valid_id(id) && method == "PUT")
}

pub fn validate_response(
    request: &ValidatedRequest,
    response: DesktopResponse,
) -> Result<DesktopResponse, &'static str> {
    if (200..300).contains(&response.status) {
        if request.binary_response {
            let object = response
                .body
                .as_object()
                .ok_or("desktop binary response invalid")?;
            if object.len() != 3
                || object.get("encoding").and_then(Value::as_str) != Some("base64")
                || object.get("mediaType").and_then(Value::as_str).is_none()
                || object.get("data").and_then(Value::as_str).is_none()
            {
                return Err("desktop binary response invalid");
            }
        }
        return Ok(response);
    }
    if !matches!(
        response.status,
        400 | 401 | 403 | 404 | 409 | 413 | 415 | 422 | 500 | 503
    ) {
        return Err("desktop response status invalid");
    }
    let problem: Problem =
        serde_json::from_value(response.body).map_err(|_| "desktop problem response invalid")?;
    if problem.status != response.status
        || !problem.r#type.starts_with("urn:go-admin-plus:problem:")
        || problem.title.is_empty()
        || problem.category.is_empty()
        || problem.code.is_empty()
        || problem.trace_id.len() != 32
    {
        return Err("desktop problem response invalid");
    }
    Ok(DesktopResponse {
        status: response.status,
        body: serde_json::to_value(problem).map_err(|_| "desktop problem response invalid")?,
    })
}

fn allowed(path: &str, method: &str) -> bool {
    let segments: Vec<_> = path.trim_start_matches('/').split('/').collect();
    match segments.as_slice() {
        ["runtime", "info"] => method == "GET",
        ["iam", "account", "profile"] => matches!(method, "GET" | "PATCH"),
        ["iam", "account", "password"] => method == "PUT",
        ["iam", "administration", "manifest" | "permissions"] => method == "GET",
        ["iam", "administration", "users"] => matches!(method, "GET" | "POST"),
        ["iam", "administration", "users", id] if valid_id(id) => {
            matches!(method, "GET" | "PATCH")
        }
        ["iam", "administration", "users", id, "deletion"] if valid_id(id) => {
            matches!(method, "GET" | "POST")
        }
        ["iam", "administration", "users", id, "deletion", "cancel"] if valid_id(id) => {
            method == "POST"
        }
        ["iam", "administration", "users", id, "roles" | "password"] if valid_id(id) => {
            method == "PUT"
        }
        ["iam", "administration", "roles" | "menus"] => matches!(method, "GET" | "POST"),
        ["iam", "administration", "roles", id] | ["iam", "administration", "menus", id]
            if valid_id(id) =>
        {
            matches!(method, "PATCH" | "DELETE")
        }
        ["iam", "administration", "roles", id, "grants"] if valid_id(id) => method == "PUT",
        ["audit", "records"] => method == "GET",
        ["audit", "records", "cleanup"] => method == "POST",
        ["audit", "records", id] if valid_audit_id(id) => method == "GET",
        ["scheduler", "task-types" | "executions"] => method == "GET",
        ["scheduler", "definitions"] => matches!(method, "GET" | "POST"),
        ["scheduler", "definitions", id] if valid_id(id) => matches!(method, "PATCH" | "DELETE"),
        ["scheduler", "definitions", id, "enable" | "stop" | "run"] if valid_id(id) => {
            method == "POST"
        }
        ["files", "objects"] => matches!(method, "GET" | "POST"),
        ["files", "objects", "batch-delete"] => method == "POST",
        ["files", "objects", id] if valid_id(id) => method == "GET",
        ["files", "objects", id, "content"] if valid_id(id) => method == "GET",
        _ => false,
    }
}

fn valid_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 128
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'))
}

// 审计详情使用 a1.<topic>.<base64url> 标识，不能用 UUID/普通资源标识的规则截断。
fn valid_audit_id(value: &str) -> bool {
    let parts: Vec<_> = value.split('.').collect();
    matches!(parts.as_slice(), ["a1", "ls" | "lf" | "oc" | "ou" | "od", key]
        if !key.is_empty() && key.len() <= 384 && key.bytes().all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_')))
}

#[cfg(test)]
mod tests {
    use super::{allowed, sensitive_input_path};

    #[test]
    fn product_allowlist_is_exact() {
        assert!(allowed("/runtime/info", "GET"));
        assert!(!allowed("/runtime/info", "POST"));
        assert!(allowed(
            "/audit/records/a1.ou.cmVzb3VyY2U6ZmlsZXM6ZmFjdA",
            "GET"
        ));
        assert!(!allowed("/audit/records/../files", "GET"));
        assert!(allowed(
            "/iam/administration/users/account-system-admin/deletion",
            "POST"
        ));
        assert!(allowed(
            "/iam/administration/users/account-system-admin/deletion/cancel",
            "POST"
        ));
        assert!(!allowed(
            "/iam/administration/users/account-system-admin",
            "DELETE"
        ));
        assert!(allowed(
            "/iam/administration/roles/role-system-admin",
            "PATCH"
        ));
        assert!(allowed(
            "/scheduler/definitions/00000000-0000-4000-8000-000000000000/stop",
            "POST"
        ));
        assert!(allowed(
            "/files/objects/00000000-0000-4000-8000-000000000000/content",
            "GET"
        ));
        assert!(!allowed("/iam/session/current", "GET"));
        assert!(!allowed("/demo/products", "DELETE"));
        assert!(sensitive_input_path("/iam/account/password", "PUT"));
        assert!(sensitive_input_path(
            "/iam/administration/users/account-system-admin/password",
            "PUT"
        ));
    }
}
