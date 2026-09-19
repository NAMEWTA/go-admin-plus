//! 桌面连接配置只包含模式、服务器地址和公开 CA 路径，凭证由独立保险库管理。
use std::{fs, path::PathBuf, time::Duration};

use serde::{Deserialize, Serialize};
#[cfg(any(test, not(feature = "native-e2e")))]
use sha2::{Digest, Sha256};
use tauri::Manager;
use ureq::tls::{Certificate, RootCerts, TlsConfig};

#[derive(Clone, Default, Deserialize, Serialize, PartialEq)]
#[serde(rename_all = "lowercase")]
pub enum Mode {
    #[default]
    Local,
    Remote,
}

#[derive(Clone, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Settings {
    pub mode: Mode,
    #[serde(default)]
    pub server_url: String,
    #[serde(default)]
    pub ca_certificate: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Status {
    pub configured: bool,
    pub settings: Settings,
}

pub fn path(app: &tauri::AppHandle) -> Result<PathBuf, &'static str> {
    Ok(app
        .path()
        .app_config_dir()
        .map_err(|_| "connection settings directory unavailable")?
        .join("connection.json"))
}
pub fn read(app: &tauri::AppHandle) -> Result<Status, &'static str> {
    let path = path(app)?;
    if !path.exists() {
        return Ok(Status {
            configured: false,
            settings: Settings::default(),
        });
    }
    let bytes = fs::read(path).map_err(|_| "connection settings unreadable")?;
    if bytes.len() > 16384 {
        return Err("connection settings too large");
    }
    let settings: Settings =
        serde_json::from_slice(&bytes).map_err(|_| "connection settings invalid")?;
    Ok(Status {
        configured: true,
        settings: validate(settings)?,
    })
}
pub fn validate(mut settings: Settings) -> Result<Settings, &'static str> {
    if settings.mode == Mode::Local {
        settings.server_url.clear();
        settings.ca_certificate.clear();
        return Ok(settings);
    }
    let url = url::Url::parse(settings.server_url.trim()).map_err(|_| "server URL invalid")?;
    let loopback = matches!(url.host_str(), Some("127.0.0.1" | "localhost" | "[::1]"));
    if (url.scheme() != "https" && !(loopback && url.scheme() == "http"))
        || url.host_str().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.path() != "/"
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err("server must be an HTTPS origin without credentials or path");
    }
    settings.server_url = url.origin().ascii_serialization();
    if settings.server_url.len() > 2048 || settings.ca_certificate.len() > 4096 {
        return Err("connection settings too large");
    }
    Ok(settings)
}
pub fn save(app: &tauri::AppHandle, settings: &Settings) -> Result<(), &'static str> {
    let path = path(app)?;
    let parent = path.parent().ok_or("connection settings path invalid")?;
    fs::create_dir_all(parent).map_err(|_| "connection settings directory unavailable")?;
    let data =
        serde_json::to_vec_pretty(settings).map_err(|_| "connection settings encode failed")?;
    let temporary = parent.join("connection.next.json");
    fs::write(&temporary, data).map_err(|_| "connection settings write failed")?;
    // Windows rename 不覆盖现有目标；原配置只在新内容已成功写入后替换。
    #[cfg(target_os = "windows")]
    if path.exists() {
        fs::remove_file(&path).map_err(|_| "connection settings replace failed")?;
    }
    fs::rename(temporary, path).map_err(|_| "connection settings replace failed")
}
#[cfg(any(test, not(feature = "native-e2e")))]
pub fn vault_directory(settings: &Settings) -> String {
    format!(
        "remote-{:x}",
        Sha256::digest(settings.server_url.as_bytes())
    )
}
pub fn agent(settings: &Settings) -> Result<ureq::Agent, &'static str> {
    let mut tls = TlsConfig::builder();
    if !settings.ca_certificate.is_empty() {
        let pem = fs::read(&settings.ca_certificate).map_err(|_| "CA certificate unreadable")?;
        if pem.len() > 1_048_576 {
            return Err("CA certificate too large");
        }
        let certificate = Certificate::from_pem(&pem).map_err(|_| "CA certificate invalid")?;
        tls = tls.root_certs(RootCerts::from([certificate]));
    }
    Ok(ureq::Agent::config_builder()
        .tls_config(tls.build())
        .timeout_global(Some(Duration::from_secs(15)))
        .max_redirects(0)
        .http_status_as_error(false)
        .build()
        .into())
}
pub fn test(settings: Settings) -> Result<(), &'static str> {
    let settings = validate(settings)?;
    if settings.mode == Mode::Local {
        return Ok(());
    }
    let mut response = agent(&settings)?
        .get(format!("{}/api/runtime/info", settings.server_url))
        .call()
        .map_err(|_| "server connection failed; check address and TLS certificate")?;
    if response.status() != 200 {
        return Err("server runtime information unavailable");
    }
    let body: serde_json::Value = response
        .body_mut()
        .with_config()
        .limit(16384)
        .read_json()
        .map_err(|_| "server runtime information invalid")?;
    if body.get("apiVersion").and_then(serde_json::Value::as_u64) != Some(1) {
        return Err("server API version incompatible");
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn remote_connection_requires_a_compatible_runtime_response() {
        use std::{
            io::{Read, Write},
            net::TcpListener,
            thread,
        };

        // 使用真实 loopback HTTP 交换验证启动前的版本探测，不绕过生产解析逻辑。
        for (status, body, compatible) in [
            (200, r#"{"apiVersion":1}"#, true),
            (200, r#"{"apiVersion":2}"#, false),
            (200, r#"{"apiVersion":"1"}"#, false),
            (200, "not-json", false),
            (503, r#"{"apiVersion":1}"#, false),
        ] {
            let listener = TcpListener::bind("127.0.0.1:0").unwrap();
            let address = listener.local_addr().unwrap();
            let server = thread::spawn(move || {
                let (mut stream, _) = listener.accept().unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(5)))
                    .unwrap();
                let mut request = Vec::new();
                let mut buffer = [0; 512];
                while !request.windows(4).any(|part| part == b"\r\n\r\n") {
                    let count = stream.read(&mut buffer).unwrap();
                    assert!(count > 0 && request.len() < 8192);
                    request.extend_from_slice(&buffer[..count]);
                }
                assert!(request.starts_with(b"GET /api/runtime/info HTTP/1.1\r\n"));
                write!(
                    stream,
                    "HTTP/1.1 {status} Test\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                )
                .unwrap();
            });
            let result = test(Settings {
                mode: Mode::Remote,
                server_url: format!("http://{address}"),
                ..Default::default()
            });
            server.join().unwrap();
            assert_eq!(result.is_ok(), compatible, "response status {status}");
        }
    }

    #[test]
    fn remote_origins_are_explicit_and_credentials_are_isolated() {
        for value in [
            "http://example.com",
            "https://user:secret@example.com",
            "https://example.com/api",
            "file:///etc/passwd",
            "https://example.com?secret=a",
        ] {
            assert!(
                validate(Settings {
                    mode: Mode::Remote,
                    server_url: value.into(),
                    ..Default::default()
                })
                .is_err()
            );
        }
        let a = validate(Settings {
            mode: Mode::Remote,
            server_url: "https://a.example/".into(),
            ..Default::default()
        })
        .unwrap();
        let b = validate(Settings {
            mode: Mode::Remote,
            server_url: "https://b.example".into(),
            ..Default::default()
        })
        .unwrap();
        assert_ne!(vault_directory(&a), vault_directory(&b));
        assert_eq!(a.server_url, "https://a.example");
    }
}
