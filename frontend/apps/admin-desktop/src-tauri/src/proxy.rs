use std::{collections::BTreeSet, sync::Mutex, time::Duration};

use base64::{Engine as _, engine::general_purpose::STANDARD};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use zeroize::{Zeroize, Zeroizing};

use crate::first_setup::{self, FirstSetupInput, FirstSetupOutcome, FirstSetupState};
use crate::product_contract;
use crate::vault::{SessionSecrets, SessionVault};

const SESSION_COOKIE: &str = "__Host-go-admin-session";
const CONTROL_HEADER: &str = "X-Go-Admin-Desktop-Control";
const MAX_REQUEST_BYTES: usize = 1024 * 1024;
const MAX_RESPONSE_BYTES: u64 = 11 * 1024 * 1024;

#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DesktopRequest {
    #[serde(default)]
    pub revision: Option<u64>,
    pub path: String,
    pub method: String,
    pub body: Option<Value>,
}

#[derive(Serialize)]
pub struct DesktopResponse {
    pub status: u16,
    pub body: Value,
}

#[derive(Default)]
struct Rotation {
    expected_token: Option<Zeroizing<String>>,
    cookie: Option<String>,
    csrf: Option<String>,
}

struct WireResponse {
    response: DesktopResponse,
    rotation: Rotation,
    protected_values: Vec<Zeroizing<String>>,
}

#[derive(Clone, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct PublicProfile {
    pub id: String,
    pub username: String,
    pub display_name: String,
    pub email: String,
    pub avatar_ref: Option<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct SessionWire {
    profile: PublicProfile,
    csrf_token: String,
}

#[derive(Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct PublicMenu {
    pub key: String,
    pub label: String,
    pub path: String,
    pub permission_code: String,
    pub sort_order: i64,
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub parent_id: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub route_key: String,
    #[serde(default)]
    pub icon: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct ManifestWire {
    permission_codes: Vec<String>,
    menus: Vec<PublicMenu>,
    data_scope: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase", tag = "kind")]
pub enum IdentityResult {
    #[serde(rename = "unauthenticated")]
    Unauthenticated,
    #[serde(rename = "authenticated")]
    Authenticated {
        profile: PublicProfile,
        permissions: Vec<String>,
        #[serde(rename = "dataScope")]
        data_scope: String,
    },
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LogoutResult {
    pub local_cleared: bool,
    pub remote_revoked: bool,
}

pub struct TransportProxy {
    origin: String,
    control: Zeroizing<String>,
    vault: Mutex<SessionVault>,
    agent: ureq::Agent,
}

impl TransportProxy {
    #[cfg(not(feature = "native-e2e"))]
    pub fn remote(
        settings: &crate::connection::Settings,
        vault: SessionVault,
    ) -> Result<Self, &'static str> {
        let settings = crate::connection::validate(settings.clone())?;
        Ok(Self {
            origin: settings.server_url.clone(),
            control: Zeroizing::new(String::new()),
            vault: Mutex::new(vault),
            agent: crate::connection::agent(&settings)?,
        })
    }

    pub fn new(
        origin: String,
        control: Zeroizing<String>,
        vault: SessionVault,
    ) -> Result<Self, &'static str> {
        if !valid_origin(&origin) || !valid_secret(&control) {
            return Err("desktop proxy configuration invalid");
        }
        let config = ureq::Agent::config_builder()
            .timeout_global(Some(Duration::from_secs(15)))
            .max_redirects(0)
            .http_status_as_error(false)
            .build();
        Ok(Self {
            origin,
            control,
            vault: Mutex::new(vault),
            agent: config.into(),
        })
    }

    pub fn identity(&self) -> Result<IdentityResult, &'static str> {
        #[cfg(feature = "native-e2e")]
        let had_session = self
            .vault
            .lock()
            .map_err(|_| "desktop vault unavailable")?
            .read()?
            .is_some();
        let current = self.send("GET", "/api/iam/session/current", None, true)?;
        if current.response.status == 401 {
            #[cfg(feature = "native-e2e")]
            eprintln!(
                "desktop native identity state: {}",
                if had_session {
                    "remote unauthenticated"
                } else {
                    "vault empty"
                }
            );
            if contains_secret_material(&current.response.body, &current.protected_values) {
                return Err("desktop identity response invalid");
            }
            self.commit_rotation(current.rotation, None)?;
            return Ok(IdentityResult::Unauthenticated);
        }
        if current.response.status != 200 {
            return Err("desktop identity request failed");
        }
        let session: SessionWire = serde_json::from_value(current.response.body)
            .map_err(|_| "desktop identity response invalid")?;
        validate_profile(&session.profile)?;
        if profile_contains_secret(&session.profile, &current.protected_values) {
            return Err("desktop identity response invalid");
        }
        self.commit_rotation(current.rotation, Some(session.csrf_token))?;
        let manifest = self.manifest()?;
        Ok(IdentityResult::Authenticated {
            profile: session.profile,
            permissions: manifest.permission_codes,
            data_scope: manifest.data_scope,
        })
    }

    pub fn first_setup_state(&self) -> Result<FirstSetupState, &'static str> {
        if self.control.is_empty() {
            return Ok(FirstSetupState::LoginRequired);
        }
        let body = serde_json::json!({"action": first_setup::STATE_ACTION});
        let response = self.send("POST", first_setup::PATH, Some(&body), false)?;
        if response.response.status != 200
            || contains_secret_material(&response.response.body, &response.protected_values)
        {
            return Err("desktop first setup state failed");
        }
        first_setup::decode_state(response.response.body)
    }

    pub fn first_setup_submit(
        &self,
        input: FirstSetupInput,
    ) -> Result<FirstSetupOutcome, &'static str> {
        if self.control.is_empty() || !input.valid() {
            return Err("desktop first setup input invalid");
        }
        let mut body = Some(serde_json::json!({
            "action": first_setup::SUBMIT_ACTION,
            "username": input.username.as_str(),
            "displayName": input.display_name.as_str(),
            "email": input.email.as_str(),
            "password": input.password.as_str(),
        }));
        let response = self.send("POST", first_setup::PATH, body.as_ref(), false);
        if let Some(value) = body.as_mut() {
            scrub_json(value);
        }
        let response = response?;
        if response.response.status == 409 {
            return match first_setup::decode_state(response.response.body)? {
                FirstSetupState::LoginRequired => Ok(FirstSetupOutcome::LoginRequired),
                _ => Err("desktop first setup response invalid"),
            };
        }
        if response.response.status == 400 {
            return Err("desktop first setup input invalid");
        }
        if response.response.status != 200 {
            return Err("desktop first setup failed");
        }
        let mut issued = first_setup::decode_complete(response.response.body)?;
        validate_profile(&issued.profile)?;
        if !valid_secret(&issued.token) || !valid_secret(&issued.csrf) {
            return Err("desktop first setup response invalid");
        }
        self.vault
            .lock()
            .map_err(|_| "desktop vault unavailable")?
            .write(SessionSecrets {
                token: std::mem::take(&mut issued.token),
                csrf: std::mem::take(&mut issued.csrf),
            })?;

        let verified = self.identity();
        match verified {
            Ok(IdentityResult::Authenticated { profile, .. }) if profile == issued.profile => {
                Ok(FirstSetupOutcome::Complete { profile })
            }
            _ => {
                let _ = self.logout();
                self.vault
                    .lock()
                    .map_err(|_| "desktop vault unavailable")?
                    .clear()?;
                Ok(FirstSetupOutcome::LoginRequired)
            }
        }
    }

    pub fn navigation(&self) -> Result<Vec<PublicMenu>, &'static str> {
        let manifest = self.manifest()?;
        Ok(manifest.menus)
    }

    pub fn login(
        &self,
        username: Zeroizing<String>,
        password: Zeroizing<String>,
    ) -> Result<PublicProfile, &'static str> {
        if username.len() < 3 || username.len() > 64 || password.len() < 10 || password.len() > 128
        {
            return Err("desktop login input invalid");
        }
        let mut credentials =
            Some(serde_json::json!({"username": username.as_str(), "password": password.as_str()}));
        let response = self.send(
            "POST",
            "/api/iam/session/login",
            credentials.as_ref(),
            false,
        );
        if let Some(value) = credentials.as_mut() {
            scrub_json(value);
        }
        let response = response?;
        if response.response.status != 200 {
            return Err("desktop login rejected");
        }
        let session: SessionWire = serde_json::from_value(response.response.body)
            .map_err(|_| "desktop login response invalid")?;
        validate_profile(&session.profile)?;
        if profile_contains_secret(&session.profile, &response.protected_values) {
            return Err("desktop login response invalid");
        }
        self.commit_rotation(response.rotation, Some(session.csrf_token))?;
        Ok(session.profile)
    }

    pub fn logout(&self) -> Result<LogoutResult, &'static str> {
        let response = self.send("POST", "/api/iam/session/logout", None, true);
        // 本地退出不依赖远程可用性，断网时也必须移除保存的凭证。
        let status = response
            .ok()
            .filter(|r| !contains_secret_material(&r.response.body, &r.protected_values))
            .map_or(500, |r| r.response.status);
        finish_logout(status, || {
            self.vault
                .lock()
                .map_err(|_| "desktop vault unavailable")?
                .clear()
        })
    }

    pub fn heartbeat(&self) -> Result<PublicProfile, &'static str> {
        self.maintain_session("/api/iam/session/heartbeat")
    }

    pub fn renew(&self) -> Result<PublicProfile, &'static str> {
        self.maintain_session("/api/iam/session/renew")
    }

    fn maintain_session(&self, path: &'static str) -> Result<PublicProfile, &'static str> {
        let response = self.send("POST", path, None, true)?;
        if response.response.status != 200 {
            if contains_secret_material(&response.response.body, &response.protected_values) {
                return Err("desktop session maintenance response invalid");
            }
            let status = response.response.status;
            if matches!(status, 401 | 403) {
                self.vault
                    .lock()
                    .map_err(|_| "desktop vault unavailable")?
                    .clear()?;
            }
            return Err(match status {
                401 => "desktop session authentication failed",
                403 => "desktop session authorization failed",
                _ => "desktop session maintenance failed",
            });
        }
        let session: SessionWire = serde_json::from_value(response.response.body)
            .map_err(|_| "desktop session maintenance response invalid")?;
        validate_profile(&session.profile)?;
        if profile_contains_secret(&session.profile, &response.protected_values) {
            return Err("desktop session maintenance response invalid");
        }
        self.commit_rotation(response.rotation, Some(session.csrf_token))?;
        Ok(session.profile)
    }

    pub fn business(&self, request: DesktopRequest) -> Result<DesktopResponse, &'static str> {
        #[cfg(feature = "native-e2e")]
        if request.path == "/__desktop/test-control" {
            return self.native_e2e_control(request);
        }
        let mut request = product_contract::validate_request(request)?;
        if request.upload.is_none()
            && request.body.as_ref().is_some_and(|body| {
                serde_json::to_vec(body).map_or(true, |encoded| encoded.len() > MAX_REQUEST_BYTES)
                    || !request.allows_sensitive_input && contains_secret_material(body, &[])
            })
        {
            return Err("desktop request body rejected");
        }
        let sidecar_path = format!("/api{}", request.path);
        let response = self.send_with_revision(
            &request.method,
            &sidecar_path,
            request.body.as_ref(),
            true,
            request.revision,
        );
        if let Some(body) = request.body.as_mut() {
            scrub_json(body);
        }
        let response = response?;
        if contains_secret_material(&response.response.body, &response.protected_values) {
            return Err("desktop response body rejected");
        }
        let mut public = product_contract::validate_response(&request, response.response)?;
        self.commit_rotation(response.rotation, None)?;
        if contains_secret_material(&public.body, &[]) {
            scrub_json(&mut public.body);
            return Err("desktop response body rejected");
        }
        Ok(public)
    }

    #[cfg(feature = "native-e2e")]
    fn native_e2e_control(&self, request: DesktopRequest) -> Result<DesktopResponse, &'static str> {
        if request.method != "POST" {
            return Err("desktop test control rejected");
        }
        let body = request.body.ok_or("desktop test control rejected")?;
        let values = body
            .as_object()
            .filter(|values| values.len() == 1)
            .ok_or("desktop test control rejected")?;
        let action = values
            .get("action")
            .and_then(Value::as_str)
            .filter(|value| {
                matches!(
                    *value,
                    "scope-self"
                        | "scope-all"
                        | "permissions-off"
                        | "permissions-on"
                        | "session-revoke"
                )
            })
            .ok_or("desktop test control rejected")?;
        let body = serde_json::json!({"action": action});
        let response = self.send("POST", "/__desktop/test-control", Some(&body), false)?;
        if response.response.status != 204 {
            return Err(match response.response.status {
                403 => "desktop test control authorization failed",
                500 => "desktop test control execution failed",
                501 => "desktop test control scope sentinel failed",
                502 => "desktop test control scope update failed",
                503 => "desktop test control scope rows failed",
                504 => "desktop test control request invalid",
                505 => "desktop test control dependencies unavailable",
                _ => "desktop test control status invalid",
            });
        }
        if response.response.body != Value::Null {
            return Err("desktop test control body invalid");
        }
        if response.rotation.cookie.is_some() || response.rotation.csrf.is_some() {
            return Err("desktop test control rotation invalid");
        }
        if contains_secret_material(&response.response.body, &response.protected_values) {
            return Err("desktop test control material invalid");
        }
        Ok(response.response)
    }

    pub fn shutdown(&self) {
        // 关闭桌面只停止本机 sidecar，远程服务器的生命周期由服务端管理。
        if !self.control.is_empty() {
            let _ = self.send("POST", "/__desktop/shutdown", None, false);
        }
    }

    fn manifest(&self) -> Result<ManifestWire, &'static str> {
        let response = self.send("GET", "/api/iam/administration/manifest", None, true)?;
        if response.response.status != 200 {
            return Err("desktop navigation request failed");
        }
        if contains_secret_material(&response.response.body, &response.protected_values) {
            return Err("desktop navigation response invalid");
        }
        let manifest = decode_manifest(response.response.body)?;
        self.commit_rotation(response.rotation, None)?;
        Ok(manifest)
    }

    fn commit_rotation(
        &self,
        rotation: Rotation,
        response_csrf: Option<String>,
    ) -> Result<(), &'static str> {
        if rotation.cookie.is_none() && rotation.csrf.is_none() && response_csrf.is_none() {
            return Ok(());
        }
        let header_csrf = rotation.csrf;
        if header_csrf
            .as_ref()
            .is_some_and(|value| !valid_secret(value))
            || response_csrf
                .as_ref()
                .is_some_and(|value| !valid_secret(value))
            || header_csrf
                .as_ref()
                .zip(response_csrf.as_ref())
                .is_some_and(|(left, right)| left != right)
        {
            return Err("desktop session response invalid");
        }
        let vault = self.vault.lock().map_err(|_| "desktop vault unavailable")?;
        let existing = vault.read()?;
        if let Some(expected) = rotation.expected_token.as_ref()
            && existing
                .as_ref()
                .is_none_or(|v| v.token != expected.as_str())
        {
            return Ok(());
        }
        let mut current = existing.unwrap_or(SessionSecrets {
            token: String::new(),
            csrf: String::new(),
        });
        if let Some(cookie) = rotation.cookie {
            match parse_session_cookie(&cookie)? {
                Some(token) => current.token = token,
                None => return vault.clear(),
            }
        }
        if let Some(csrf) = response_csrf.or(header_csrf) {
            current.csrf.zeroize();
            current.csrf = csrf;
        }
        if !valid_secret(&current.token) || !valid_secret(&current.csrf) {
            return Err("desktop session rotation invalid");
        };
        vault.write(current)
    }

    // 文件内容通过操作系统句柄流式传输，不穿过 WebView IPC；会话更新使用发送时令牌做 fencing。
    pub fn upload_file(&self, path: std::path::PathBuf) -> Result<(), &'static str> {
        use std::io::{Cursor, Read};
        let mut file = std::fs::File::open(&path).map_err(|_| "selected file unavailable")?;
        let metadata = file.metadata().map_err(|_| "selected file unavailable")?;
        if !metadata.is_file() {
            return Err("selected file invalid");
        }
        let name = path
            .file_name()
            .and_then(|v| v.to_str())
            .filter(|v| !v.chars().any(char::is_control))
            .ok_or("selected file invalid")?;
        let info = self
            .send("GET", "/api/runtime/info", None, false)?
            .response
            .body;
        let max = info
            .get("maxUploadBytes")
            .and_then(Value::as_u64)
            .filter(|value| *value > 0 && *value <= 1 << 30)
            .ok_or("runtime file limits unavailable")?;
        if metadata.len() > max {
            return Err("file exceeds server limit");
        }
        let mut prefix = [0u8; 512];
        let read = file
            .read(&mut prefix)
            .map_err(|_| "selected file unavailable")?;
        use std::io::{Seek, SeekFrom};
        file.seek(SeekFrom::Start(0))
            .map_err(|_| "selected file unavailable")?;
        let mime = if prefix[..read].starts_with(b"%PDF-") {
            "application/pdf"
        } else if prefix[..read].starts_with(b"\x89PNG\r\n\x1a\n") {
            "image/png"
        } else if prefix[..read].starts_with(&[0xff, 0xd8, 0xff]) {
            "image/jpeg"
        } else {
            "text/plain"
        };
        let secrets = self
            .vault
            .lock()
            .map_err(|_| "desktop vault unavailable")?
            .read()?
            .ok_or("session required")?;
        let boundary = "gap-native-stream-6ed86fac5c2441db";
        let escaped = name.replace('\\', "\\\\").replace('"', "\\\"");
        let head = format!(
            "--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{escaped}\"\r\nContent-Type: {mime}\r\n\r\n"
        );
        let tail = format!("\r\n--{boundary}--\r\n");
        let length = head.len() as u64 + metadata.len() + tail.len() as u64;
        let mut stream = Cursor::new(head.into_bytes())
            .chain(file.take(max + 1))
            .chain(Cursor::new(tail.into_bytes()));
        let mut builder = self
            .agent
            .post(format!("{}/api/files/objects", self.origin))
            .config()
            .timeout_global(Some(Duration::from_secs(600)))
            .build()
            .header("X-Client-Platform", "desktop")
            .header("Cookie", &format!("{SESSION_COOKIE}={}", secrets.token))
            .header("X-CSRF-Token", &secrets.csrf)
            .header(
                "Content-Type",
                &format!("multipart/form-data; boundary={boundary}"),
            )
            .header("Content-Length", &length.to_string());
        if !self.control.is_empty() {
            builder = builder.header(CONTROL_HEADER, self.control.as_str());
        }
        let response = builder
            .send(ureq::SendBody::from_reader(&mut stream))
            .map_err(|_| "upload failed")?;
        let status = response.status().as_u16();
        self.commit_rotation(
            Rotation {
                expected_token: Some(Zeroizing::new(secrets.token.clone())),
                cookie: unique_header(response.headers(), "set-cookie")?,
                csrf: unique_header(response.headers(), "x-csrf-token")?,
            },
            None,
        )?;
        if status != 201 {
            return Err("upload rejected by server");
        };
        Ok(())
    }
    pub fn download_file(&self, id: &str, path: std::path::PathBuf) -> Result<(), &'static str> {
        use std::io::Read;
        if id.len() != 36 || !id.chars().all(|c| c.is_ascii_hexdigit() || c == '-') {
            return Err("file id invalid");
        }
        let secrets = self
            .vault
            .lock()
            .map_err(|_| "desktop vault unavailable")?
            .read()?
            .ok_or("session required")?;
        let mut builder = self
            .agent
            .get(format!("{}/api/files/objects/{id}/content", self.origin))
            .config()
            .timeout_global(Some(Duration::from_secs(600)))
            .build()
            .header("X-Client-Platform", "desktop")
            .header("Cookie", &format!("{SESSION_COOKIE}={}", secrets.token));
        if !self.control.is_empty() {
            builder = builder.header(CONTROL_HEADER, self.control.as_str());
        }
        let mut response = builder.call().map_err(|_| "download failed")?;
        self.commit_rotation(
            Rotation {
                expected_token: Some(Zeroizing::new(secrets.token.clone())),
                cookie: unique_header(response.headers(), "set-cookie")?,
                csrf: unique_header(response.headers(), "x-csrf-token")?,
            },
            None,
        )?;
        if response.status() != 200 {
            return Err("download rejected by server");
        }
        let length = response
            .headers()
            .get("content-length")
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.parse::<u64>().ok())
            .filter(|v| *v <= 1 << 30)
            .ok_or("file size invalid")?;
        let mut random = [0u8; 16];
        getrandom::fill(&mut random).map_err(|_| "save file failed")?;
        let suffix: String = random.iter().map(|b| format!("{b:02x}")).collect();
        let temp = path.with_file_name(format!(".gap-download-{suffix}.tmp"));
        let result = (|| {
            let mut output = std::fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&temp)
                .map_err(|_| "save file failed")?;
            let count = std::io::copy(
                &mut response.body_mut().as_reader().take(length + 1),
                &mut output,
            )
            .map_err(|_| "download interrupted")?;
            if count != length {
                return Err("download size mismatch");
            };
            output.sync_all().map_err(|_| "save file failed")?;
            drop(output);
            // 不删除现有目标文件；Windows rename 不覆盖时保留旧文件并报告失败。
            std::fs::rename(&temp, &path).map_err(|_| "save file failed")
        })();
        if result.is_err() {
            let _ = std::fs::remove_file(temp);
        }
        result
    }
    fn send(
        &self,
        method: &str,
        path: &str,
        body: Option<&Value>,
        authenticated: bool,
    ) -> Result<WireResponse, &'static str> {
        self.send_with_revision(method, path, body, authenticated, None)
    }
    fn send_with_revision(
        &self,
        method: &str,
        path: &str,
        body: Option<&Value>,
        authenticated: bool,
        revision: Option<u64>,
    ) -> Result<WireResponse, &'static str> {
        if !valid_path(path) {
            return Err("desktop proxy path invalid");
        }
        let url = format!("{}{}", self.origin, path);
        let secrets = if authenticated {
            self.vault
                .lock()
                .map_err(|_| "desktop vault unavailable")?
                .read()?
        } else {
            None
        };

        let cookie = secrets
            .as_ref()
            .map(|values| Zeroizing::new(format!("{}={}", SESSION_COOKIE, values.token)));
        let mut protected_values = Vec::new();
        if let Some(values) = secrets.as_ref() {
            protected_values.push(Zeroizing::new(values.token.clone()));
            protected_values.push(Zeroizing::new(values.csrf.clone()));
            protected_values.push(Zeroizing::new(format!("Bearer {}", values.token)));
        }
        if let Some(value) = cookie.as_ref() {
            protected_values.push(Zeroizing::new(value.to_string()));
        }
        let mut multipart = if method == "POST" && path == "/api/files/objects" {
            Some(multipart_upload(
                body.ok_or("desktop upload body invalid")?,
            )?)
        } else {
            None
        };
        let result = match (method, body) {
            ("GET", None) => {
                let mut builder = self
                    .agent
                    .get(&url)
                    .header("Accept", "application/json")
                    .header("X-Client-Platform", "desktop")
                    .header("If-Match", &revision.unwrap_or(0).to_string());
                if !self.control.is_empty() {
                    builder = builder.header(CONTROL_HEADER, self.control.as_str());
                }
                if let Some(value) = cookie.as_ref() {
                    builder = builder.header("Cookie", value.as_str());
                }
                builder.call()
            }
            ("POST", value) => {
                let mut builder = self
                    .agent
                    .post(&url)
                    .header("Accept", "application/json")
                    .header("X-Client-Platform", "desktop")
                    .header("If-Match", &revision.unwrap_or(0).to_string());
                if !self.control.is_empty() {
                    builder = builder.header(CONTROL_HEADER, self.control.as_str());
                }
                if let Some(value) = cookie.as_ref() {
                    builder = builder.header("Cookie", value.as_str());
                }
                if let Some(values) = secrets.as_ref() {
                    builder = builder.header("X-CSRF-Token", &values.csrf);
                }
                match multipart.as_ref() {
                    Some((content_type, bytes)) => builder
                        .header("Content-Type", content_type)
                        .send(bytes.as_slice()),
                    None => match value {
                        Some(value) => builder
                            .header("Content-Type", "application/json")
                            .send_json(value),
                        None => builder.send_empty(),
                    },
                }
            }
            ("PUT", Some(value)) => {
                let mut builder = self
                    .agent
                    .put(&url)
                    .header("Accept", "application/json")
                    .header("X-Client-Platform", "desktop")
                    .header("If-Match", &revision.unwrap_or(0).to_string());
                if !self.control.is_empty() {
                    builder = builder.header(CONTROL_HEADER, self.control.as_str());
                }
                if let Some(value) = cookie.as_ref() {
                    builder = builder.header("Cookie", value.as_str());
                }
                if let Some(values) = secrets.as_ref() {
                    builder = builder.header("X-CSRF-Token", &values.csrf);
                }
                builder
                    .header("Content-Type", "application/json")
                    .send_json(value)
            }
            ("PATCH", Some(value)) => {
                let mut builder = self
                    .agent
                    .patch(&url)
                    .header("Accept", "application/json")
                    .header("X-Client-Platform", "desktop")
                    .header("If-Match", &revision.unwrap_or(0).to_string());
                if !self.control.is_empty() {
                    builder = builder.header(CONTROL_HEADER, self.control.as_str());
                }
                if let Some(value) = cookie.as_ref() {
                    builder = builder.header("Cookie", value.as_str());
                }
                if let Some(values) = secrets.as_ref() {
                    builder = builder.header("X-CSRF-Token", &values.csrf);
                }
                builder
                    .header("Content-Type", "application/json")
                    .send_json(value)
            }
            ("DELETE", None) => {
                let mut builder = self
                    .agent
                    .delete(&url)
                    .header("Accept", "application/json")
                    .header("X-Client-Platform", "desktop")
                    .header("If-Match", &revision.unwrap_or(0).to_string());
                if !self.control.is_empty() {
                    builder = builder.header(CONTROL_HEADER, self.control.as_str());
                }
                if let Some(value) = cookie.as_ref() {
                    builder = builder.header("Cookie", value.as_str());
                }
                if let Some(values) = secrets.as_ref() {
                    builder = builder.header("X-CSRF-Token", &values.csrf);
                }
                builder.call()
            }
            _ => return Err("desktop proxy method invalid"),
        };
        if let Some((_, bytes)) = multipart.as_mut() {
            bytes.zeroize();
        }
        let mut response = result.map_err(|_| "desktop sidecar request failed")?;
        let status = response.status().as_u16();
        let replacement = unique_header(response.headers(), "set-cookie")?;
        let rotated_csrf = unique_header(response.headers(), "x-csrf-token")?;
        if let Some(header) = replacement.as_ref() {
            protected_values.push(Zeroizing::new(header.clone()));
            if let Some(token) = parse_session_cookie(header)? {
                protected_values.push(Zeroizing::new(format!("Bearer {token}")));
                protected_values.push(Zeroizing::new(token));
            }
        }
        if let Some(csrf) = rotated_csrf.as_ref() {
            if !valid_secret(csrf) {
                return Err("desktop session response invalid");
            }
            protected_values.push(Zeroizing::new(csrf.clone()));
        }
        let mut bytes = response
            .body_mut()
            .with_config()
            .limit(MAX_RESPONSE_BYTES)
            .read_to_vec()
            .map_err(|_| "desktop sidecar response invalid")?;
        let binary_media_type = if is_files_download(path, status) {
            unique_header(response.headers(), "content-type")?
        } else {
            None
        };
        let parsed = if let Some(body) =
            parse_binary_response_body(path, status, binary_media_type.as_deref(), &bytes)?
        {
            Ok(body)
        } else if bytes.is_empty() {
            Ok(Value::Null)
        } else {
            let decoded = serde_json::from_slice(&bytes);
            #[cfg(feature = "native-e2e")]
            if let Err(error) = &decoded {
                eprintln!(
                    "desktop native JSON response failed: status={status} bytes={} category={:?} line={} column={}",
                    bytes.len(),
                    error.classify(),
                    error.line(),
                    error.column()
                );
            }
            decoded.map_err(|_| "desktop sidecar JSON response invalid")
        };
        bytes.zeroize();
        let body = parsed?;
        Ok(WireResponse {
            response: DesktopResponse { status, body },
            rotation: Rotation {
                expected_token: secrets.as_ref().map(|s| Zeroizing::new(s.token.clone())),
                cookie: replacement,
                csrf: rotated_csrf,
            },
            protected_values,
        })
    }
}

fn is_files_download(path: &str, status: u16) -> bool {
    path.starts_with("/api/files/objects/") && path.ends_with("/content") && status < 300
}

fn parse_binary_response_body(
    path: &str,
    status: u16,
    media_type: Option<&str>,
    bytes: &[u8],
) -> Result<Option<Value>, &'static str> {
    if !is_files_download(path, status) {
        return Ok(None);
    }
    if media_type != Some("application/octet-stream") {
        return Err("desktop binary response invalid");
    }
    Ok(Some(serde_json::json!({
        "encoding": "base64",
        "mediaType": "application/octet-stream",
        "data": STANDARD.encode(bytes),
    })))
}

fn multipart_upload(body: &Value) -> Result<(String, Vec<u8>), &'static str> {
    let upload: crate::product_contract::UploadBody =
        serde_json::from_value(body.clone()).map_err(|_| "desktop upload body invalid")?;
    let data = STANDARD
        .decode(upload.data.as_bytes())
        .map_err(|_| "desktop upload body invalid")?;
    if data.is_empty() || data.len() > 10 * 1024 * 1024 {
        return Err("desktop upload body invalid");
    }
    let boundary = "go-admin-plus-desktop-boundary-7f6d8e3a";
    let mut bytes = Vec::with_capacity(data.len() + 512);
    bytes.extend_from_slice(format!("--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{}\"\r\nContent-Type: {}\r\n\r\n", upload.name, upload.media_type).as_bytes());
    bytes.extend_from_slice(&data);
    bytes.extend_from_slice(format!("\r\n--{boundary}--\r\n").as_bytes());
    Ok((format!("multipart/form-data; boundary={boundary}"), bytes))
}

fn decode_manifest(value: Value) -> Result<ManifestWire, &'static str> {
    let manifest: ManifestWire =
        serde_json::from_value(value).map_err(|_| "desktop navigation response invalid")?;
    let permissions: BTreeSet<_> = manifest.permission_codes.iter().collect();
    let menu_keys: BTreeSet<_> = manifest.menus.iter().map(|menu| &menu.key).collect();
    let menu_paths: BTreeSet<_> = manifest
        .menus
        .iter()
        .filter(|menu| menu.kind != "directory")
        .map(|menu| &menu.path)
        .collect();
    if !matches!(manifest.data_scope.as_str(), "self" | "all")
        || permissions.len() != manifest.permission_codes.len()
        || menu_keys.len() != manifest.menus.len()
        || menu_paths.len()
            != manifest
                .menus
                .iter()
                .filter(|menu| menu.kind != "directory")
                .count()
        || manifest
            .permission_codes
            .iter()
            .any(|value| !valid_permission(value))
        || manifest.menus.iter().any(|menu| {
            menu.key.is_empty()
                || menu.key.len() > 64
                || menu.label.is_empty()
                || menu.label.chars().count() > 64
                || menu.sort_order < 0
                || (menu.kind != "directory" && !valid_path(&menu.path))
                || menu.path.contains('?')
                || (menu.kind != "directory" && !valid_permission(&menu.permission_code))
                || (menu.kind != "directory" && !permissions.contains(&menu.permission_code))
        })
    {
        return Err("desktop navigation response invalid");
    }
    Ok(manifest)
}

fn parse_session_cookie(header: &str) -> Result<Option<String>, &'static str> {
    if header.contains(',') || header.len() > 512 {
        return Err("desktop session cookie invalid");
    }
    let mut parts = header.split(';').map(str::trim);
    let pair = parts.next().ok_or("desktop session cookie invalid")?;
    let prefix = format!("{}=", SESSION_COOKIE);
    let value = pair
        .strip_prefix(&prefix)
        .ok_or("desktop session cookie invalid")?;
    if !value.is_empty() && !valid_secret(value) {
        return Err("desktop session cookie invalid");
    }
    let mut secure = false;
    let mut http_only = false;
    let mut path = false;
    let mut same_site = false;
    let mut deletion = false;
    for part in parts {
        let lower = part.to_ascii_lowercase();
        match lower.as_str() {
            "secure" if !secure => secure = true,
            "httponly" if !http_only => http_only = true,
            "path=/" if !path => path = true,
            "samesite=strict" if !same_site => same_site = true,
            "max-age=0" if !deletion => deletion = true,
            _ => return Err("desktop session cookie invalid"),
        }
    }
    if !secure || !http_only || !path || !same_site || deletion != value.is_empty() {
        return Err("desktop session cookie invalid");
    }
    Ok((!value.is_empty()).then(|| value.to_owned()))
}

fn unique_header(
    headers: &ureq::http::HeaderMap,
    name: &str,
) -> Result<Option<String>, &'static str> {
    let mut values = headers.get_all(name).iter();
    let first = values
        .next()
        .map(|value| value.to_str().map(str::to_owned))
        .transpose()
        .map_err(|_| "desktop sidecar response headers invalid")?;
    if values.next().is_some() {
        return Err("desktop sidecar response headers invalid");
    }
    Ok(first)
}

fn valid_secret(value: &str) -> bool {
    value.len() == 43
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-')
}

fn valid_permission(value: &str) -> bool {
    let segments: Vec<_> = value.split('.').collect();
    (2..=3).contains(&segments.len())
        && segments.iter().all(|segment| {
            !segment.is_empty()
                && segment.len() <= 64
                && segment.as_bytes()[0].is_ascii_lowercase()
                && segment
                    .bytes()
                    .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || byte == b'-')
        })
}

fn validate_profile(profile: &PublicProfile) -> Result<(), &'static str> {
    if profile.id.is_empty()
        || profile.id.len() > 128
        || profile.username.is_empty()
        || profile.username.len() > 64
        || profile.display_name.is_empty()
        || profile.display_name.chars().count() > 120
        || profile.email.is_empty()
        || profile.email.len() > 254
        || profile
            .avatar_ref
            .as_ref()
            .is_some_and(|value| value.len() > 512)
    {
        return Err("desktop profile response invalid");
    }
    Ok(())
}

fn valid_origin(origin: &str) -> bool {
    let Some(port) = origin.strip_prefix("http://127.0.0.1:") else {
        return false;
    };
    !port.is_empty()
        && port.bytes().all(|byte| byte.is_ascii_digit())
        && port.parse::<u16>().is_ok_and(|value| value > 0)
}

fn valid_path(path: &str) -> bool {
    path.starts_with('/')
        && !path.starts_with("//")
        && path.len() <= 2048
        && !path.contains('#')
        && !path.contains('\\')
        && !path.contains("..")
        && !path.bytes().any(|byte| byte < 0x20 || byte == 0x7f)
        && !path.to_ascii_lowercase().contains("%2f")
        && !path.to_ascii_lowercase().contains("%5c")
}

fn sensitive_key(key: &str) -> bool {
    let normalized: String = key
        .chars()
        .filter(|value| value.is_ascii_alphanumeric())
        .map(|value| value.to_ascii_lowercase())
        .collect();
    matches!(
        normalized.as_str(),
        "csrftoken"
            | "csrf"
            | "token"
            | "accesstoken"
            | "refreshtoken"
            | "sessiontoken"
            | "password"
            | "passwordhash"
            | "cookie"
            | "authorization"
            | "session"
            | "credential"
            | "credentials"
            | "secret"
            | "secretkey"
            | "privatekey"
            | "clientsecret"
            | "dsn"
            | "databaseurl"
    )
}

#[cfg(test)]
fn contains_secret_key(value: &Value) -> bool {
    match value {
        Value::Object(values) => values
            .iter()
            .any(|(key, value)| sensitive_key(key) || contains_secret_key(value)),
        Value::Array(values) => values.iter().any(contains_secret_key),
        _ => false,
    }
}

fn secret_like_text(value: &str) -> bool {
    let lower = value.to_ascii_lowercase();
    if lower.contains("bearer ")
        || lower.contains("-----begin private key-----")
        || lower.contains("-----begin ec private key-----")
        || lower.contains("postgres://")
        || lower.contains("postgresql://")
        || lower.contains("mysql://")
        || lower.contains("jdbc:")
        || lower.contains("$argon2")
        || lower.contains("$2a$")
        || lower.contains("$2b$")
        || lower.contains("$2y$")
    {
        return true;
    }
    if let Some(scheme) = lower.find("://") {
        let authority = &value[scheme + 3..];
        let end = authority.find('/').unwrap_or(authority.len());
        let user_info = &authority[..end];
        if user_info.contains('@')
            && user_info
                .split('@')
                .next()
                .is_some_and(|part| part.contains(':'))
        {
            return true;
        }
    }
    if value
        .split(|character: char| {
            !(character.is_ascii_alphanumeric() || character == '_' || character == '-')
        })
        .any(looks_like_opaque_secret)
    {
        return true;
    }
    value.split_whitespace().any(|part| {
        let segments: Vec<_> = part.split('.').collect();
        segments.len() == 3
            && segments.iter().all(|segment| {
                segment.len() >= 8
                    && segment
                        .bytes()
                        .all(|byte| byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-')
            })
    })
}

fn looks_like_opaque_secret(value: &str) -> bool {
    if value.len() != 43 {
        return false;
    }
    let classes = [
        value.bytes().any(|byte| byte.is_ascii_lowercase()),
        value.bytes().any(|byte| byte.is_ascii_uppercase()),
        value.bytes().any(|byte| byte.is_ascii_digit()),
        value.bytes().any(|byte| matches!(byte, b'-' | b'_')),
    ]
    .into_iter()
    .filter(|present| *present)
    .count();
    let distinct: BTreeSet<_> = value.bytes().collect();
    classes >= 2 && distinct.len() >= 12
}

fn contains_secret_material(value: &Value, protected: &[Zeroizing<String>]) -> bool {
    match value {
        Value::String(value) => {
            secret_like_text(value)
                || protected
                    .iter()
                    .any(|secret| !secret.is_empty() && value.contains(secret.as_str()))
        }
        Value::Object(values) => values
            .iter()
            .any(|(key, value)| sensitive_key(key) || contains_secret_material(value, protected)),
        Value::Array(values) => values
            .iter()
            .any(|value| contains_secret_material(value, protected)),
        _ => false,
    }
}

fn profile_contains_secret(profile: &PublicProfile, protected: &[Zeroizing<String>]) -> bool {
    [
        Some(profile.id.as_str()),
        Some(profile.username.as_str()),
        Some(profile.display_name.as_str()),
        Some(profile.email.as_str()),
        profile.avatar_ref.as_deref(),
    ]
    .into_iter()
    .flatten()
    .any(|value| {
        secret_like_text(value)
            || protected
                .iter()
                .any(|secret| !secret.is_empty() && value.contains(secret.as_str()))
    })
}

fn scrub_json(value: &mut Value) {
    match value {
        Value::Object(values) => {
            for value in values.values_mut() {
                scrub_json(value);
            }
            values.clear();
        }
        Value::Array(values) => {
            for value in values.iter_mut() {
                scrub_json(value);
            }
            values.clear();
        }
        Value::String(value) => value.zeroize(),
        other => *other = Value::Null,
    }
}

fn logout_status_allows_clear(status: u16) -> bool {
    matches!(status, 204 | 401)
}

fn finish_logout(
    status: u16,
    clear: impl FnOnce() -> Result<(), &'static str>,
) -> Result<LogoutResult, &'static str> {
    clear()?;
    Ok(LogoutResult {
        local_cleared: true,
        remote_revoked: logout_status_allows_clear(status),
    })
}

#[cfg(test)]
mod tests {
    #[test]
    #[cfg(not(feature = "native-e2e"))]
    fn closing_a_remote_connection_never_sends_a_sidecar_shutdown() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        listener.set_nonblocking(true).unwrap();
        let settings = crate::connection::Settings {
            mode: crate::connection::Mode::Remote,
            server_url: format!("http://{}", listener.local_addr().unwrap()),
            ..Default::default()
        };
        let proxy =
            super::TransportProxy::remote(&settings, crate::vault::SessionVault::memory_for_test())
                .unwrap();
        proxy.shutdown();
        assert_eq!(
            listener.accept().unwrap_err().kind(),
            std::io::ErrorKind::WouldBlock
        );
    }

    use super::*;

    #[test]
    fn native_streams_and_revision_reach_the_server_without_webview_buffers() {
        use std::io::{BufRead, BufReader, Read, Write};
        use std::net::TcpListener;
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let origin = format!("http://{}", listener.local_addr().unwrap());
        let payload = vec![b'x'; 2 * 1024 * 1024 + 123];
        let server_payload = payload.clone();
        let server = std::thread::spawn(move || {
            for step in 0..5 {
                let (stream, _) = listener.accept().unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(10)))
                    .unwrap();
                let mut reader = BufReader::new(stream);
                let mut headers = String::new();
                loop {
                    let mut line = String::new();
                    reader.read_line(&mut line).unwrap();
                    if line == "\r\n" {
                        break;
                    }
                    assert!(!line.is_empty());
                    headers.push_str(&line);
                }
                let lower = headers.to_ascii_lowercase();
                if step > 0 {
                    assert!(lower.contains("x-client-platform: desktop"));
                }
                let length = lower
                    .lines()
                    .find_map(|line| line.strip_prefix("content-length: "))
                    .map(|value| value.parse::<usize>().unwrap())
                    .unwrap_or(0);
                let mut body = vec![0; length];
                reader.read_exact(&mut body).unwrap();
                let mut stream = reader.into_inner();
                match step {
                    0 => {
                        assert!(headers.starts_with("GET /api/runtime/info "));
                        let body = br#"{"maxUploadBytes":4194304}"#;
                        write!(stream, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).unwrap();
                        stream.write_all(body).unwrap();
                    }
                    1 => {
                        assert!(headers.starts_with("POST /api/files/objects "));
                        assert!(lower.contains("content-type: multipart/form-data"));
                        let start = body
                            .windows(4)
                            .position(|value| value == b"\r\n\r\n")
                            .unwrap()
                            + 4;
                        assert_eq!(
                            &body[start..start + server_payload.len()],
                            server_payload.as_slice()
                        );
                        stream.write_all(b"HTTP/1.1 201 Created\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}").unwrap();
                    }
                    2 | 3 => {
                        assert!(headers.starts_with(
                            "GET /api/files/objects/00000000-0000-4000-8000-000000000013/content "
                        ));
                        write!(
                            stream,
                            "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                            server_payload.len()
                        )
                        .unwrap();
                        stream
                            .write_all(if step == 2 {
                                &server_payload
                            } else {
                                &server_payload[..1024]
                            })
                            .unwrap();
                    }
                    _ => {
                        assert!(lower.contains("if-match: 7"));
                        stream
                            .write_all(b"HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
                            .unwrap();
                    }
                }
            }
        });
        let vault = SessionVault::memory_for_test();
        vault
            .write(SessionSecrets {
                token: "a".repeat(43),
                csrf: "b".repeat(43),
            })
            .unwrap();
        let proxy = TransportProxy::new(origin, Zeroizing::new("c".repeat(43)), vault).unwrap();
        let root = std::env::temp_dir().join(format!(
            "gap-stream-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir(&root).unwrap();
        let input = root.join("input.txt");
        let output = root.join("output.txt");
        std::fs::write(&input, &payload).unwrap();
        proxy.upload_file(input).unwrap();
        let id = "00000000-0000-4000-8000-000000000013";
        proxy.download_file(id, output.clone()).unwrap();
        assert_eq!(std::fs::read(&output).unwrap(), payload);
        assert!(proxy.download_file(id, output.clone()).is_err());
        assert_eq!(std::fs::read(&output).unwrap(), payload);
        assert_eq!(std::fs::read_dir(&root).unwrap().count(), 2);
        proxy
            .business(DesktopRequest {
                path: "/iam/administration/roles/role-test".into(),
                method: "PATCH".into(),
                body: Some(serde_json::json!({"name":"角色"})),
                revision: Some(7),
            })
            .unwrap();
        server.join().unwrap();
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn late_stream_rotation_cannot_restore_a_logged_out_session() {
        let vault = SessionVault::memory_for_test();
        let proxy = TransportProxy::new(
            "http://127.0.0.1:1234".into(),
            Zeroizing::new("c".repeat(43)),
            vault,
        )
        .unwrap();
        proxy
            .commit_rotation(
                Rotation {
                    expected_token: Some(Zeroizing::new("a".repeat(43))),
                    cookie: None,
                    csrf: Some("b".repeat(43)),
                },
                None,
            )
            .unwrap();
        assert!(proxy.vault.lock().unwrap().read().unwrap().is_none());
    }

    #[test]
    fn rejects_session_and_traversal_from_generic_bridge() {
        assert!(!"/iam/session/current".starts_with("/demo/"));
        for path in [
            "//demo/products",
            "/demo/../iam/session/current",
            "/demo/%2fsecret",
            "/demo/x#fragment",
        ] {
            assert!(!valid_path(path), "accepted {path}");
        }
    }

    #[test]
    fn detects_nested_credential_keys() {
        for key in [
            "csrfToken",
            "session_token",
            "access-token",
            "password_hash",
        ] {
            let mut nested = serde_json::Map::new();
            nested.insert(key.to_owned(), Value::String("hidden".to_owned()));
            assert!(contains_secret_key(&serde_json::json!({"items":[nested]})));
        }
        assert!(!contains_secret_key(
            &serde_json::json!({"sessionTimeout":60,"tokenCount":2})
        ));
    }

    #[test]
    fn rejects_sensitive_material_in_nested_public_strings() {
        let secret = Zeroizing::new("abcdefghijklmnopqrstuvwxyzABCDEFGH123456789".to_owned());
        assert!(contains_secret_material(
            &serde_json::json!({"detail": ["prefix-abcdefghijklmnopqrstuvwxyzABCDEFGH123456789-suffix"]}),
            &[secret]
        ));
        assert!(!contains_secret_material(
            &serde_json::json!({"sessionTimeout": "tokenized-mode", "tokenCount": 2}),
            &[Zeroizing::new(
                "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789".to_owned()
            )]
        ));
        for value in [
            "aaaaaaaa.bbbbbbbb.cccccccc",
            "-----BEGIN PRIVATE KEY-----",
            "postgresql://user:password@example.test/db",
            "$argon2id$v=19$m=65536,t=3,p=1$hash",
        ] {
            assert!(contains_secret_material(
                &serde_json::json!({"violations":[{"detail":value}]}),
                &[]
            ));
        }
        assert!(!contains_secret_material(
            &serde_json::json!({
                "name": "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopq",
                "description": "a".repeat(120)
            }),
            &[]
        ));
        assert!(contains_secret_material(
            &serde_json::json!({"detail":"abcdefghijklmnopqrstuvwxyzABCDEFGH123456789"}),
            &[]
        ));
    }

    #[test]
    fn public_identity_contains_scope_but_no_session_material() {
        let encoded = serde_json::to_value(IdentityResult::Authenticated {
            profile: PublicProfile {
                id: "account-1".to_owned(),
                username: "admin".to_owned(),
                display_name: "Administrator".to_owned(),
                email: "admin@example.test".to_owned(),
                avatar_ref: None,
            },
            permissions: vec!["demo.products.read".to_owned()],
            data_scope: "all".to_owned(),
        })
        .unwrap();
        assert_eq!(encoded["dataScope"], "all");
        assert!(!contains_secret_key(&encoded));
    }

    #[test]
    fn logout_always_clears_local_credentials() {
        use std::cell::Cell;

        assert!(logout_status_allows_clear(204));
        assert!(logout_status_allows_clear(401));
        assert!(!logout_status_allows_clear(403));
        assert!(!logout_status_allows_clear(500));
        let cleared = Cell::new(false);
        assert!(
            finish_logout(500, || {
                cleared.set(true);
                Ok(())
            })
            .is_ok()
        );
        assert!(cleared.get());
        assert!(finish_logout(204, || Err("injected vault fault")).is_err());
    }

    #[test]
    fn session_cookie_parser_is_exact() {
        let token = "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789";
        assert_eq!(
            parse_session_cookie(&format!(
                "{SESSION_COOKIE}={token}; Path=/; HttpOnly; Secure; SameSite=Strict"
            ))
            .unwrap()
            .as_deref(),
            Some(token)
        );
        assert!(parse_session_cookie(&format!("other={token}")).is_err());
        assert!(
            parse_session_cookie(&format!("{SESSION_COOKIE}={token}; Path=/; HttpOnly")).is_err()
        );
        assert_eq!(
            parse_session_cookie(&format!(
                "{SESSION_COOKIE}=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict"
            ))
            .unwrap(),
            None
        );
        assert!(
            parse_session_cookie(&format!(
                "{SESSION_COOKIE}=; Path=/; HttpOnly; Secure; SameSite=Strict"
            ))
            .is_err()
        );
    }

    #[test]
    fn duplicate_sensitive_headers_and_malformed_csrf_are_rejected() {
        let mut headers = ureq::http::HeaderMap::new();
        headers.append("set-cookie", "first=1".parse().unwrap());
        headers.append("set-cookie", "second=2".parse().unwrap());
        assert!(unique_header(&headers, "set-cookie").is_err());
        assert!(!valid_secret("abcdefghijklmnopqrstuvwxyzABCDEFGH12345678+"));
        assert!(valid_secret("abcdefghijklmnopqrstuvwxyzABCDEFGH123456789"));
    }

    #[test]
    fn files_downloads_use_the_exact_openapi_binary_envelope() {
        let body = parse_binary_response_body(
            "/api/files/objects/00000000-0000-4000-8000-000000000013/content",
            200,
            Some("application/octet-stream"),
            &[0xff, 0x00, 0x61],
        )
        .unwrap()
        .unwrap();
        assert_eq!(
            body,
            serde_json::json!({
                "encoding": "base64",
                "mediaType": "application/octet-stream",
                "data": "/wBh"
            })
        );
        for incompatible in ["image/png", "application/pdf", "text/plain"] {
            assert!(
                parse_binary_response_body(
                    "/api/files/objects/00000000-0000-4000-8000-000000000013/content",
                    200,
                    Some(incompatible),
                    b"content",
                )
                .is_err()
            );
        }
        assert_eq!(
            parse_binary_response_body(
                "/api/files/objects/00000000-0000-4000-8000-000000000013/content",
                404,
                Some("application/problem+json"),
                br#"{"type":"not-found"}"#,
            )
            .unwrap(),
            None
        );
    }
}
