use std::{
    fs,
    path::{Path, PathBuf},
    sync::mpsc,
    time::Duration,
};

use serde::{Deserialize, Serialize};
use tauri_plugin_stronghold::stronghold::Stronghold;
use zeroize::{Zeroize, Zeroizing};

const KEY_BYTES: usize = 32;
const CLIENT_ID: &[u8] = b"go-admin-plus-desktop";
const SESSION_KEY: &[u8] = b"opaque-session";
#[cfg(not(feature = "native-e2e"))]
const KEYRING_SERVICE: &str = "com.goadmin.plus.stronghold";
#[cfg(not(feature = "native-e2e"))]
const KEYRING_ACCOUNT: &str = "desktop-session-vault";
#[cfg(feature = "native-e2e")]
const E2E_KEYRING_SERVICE: &str = "com.goadmin.plus.stronghold.native-e2e";
#[cfg(feature = "native-e2e")]
const E2E_KEYRING_ENVIRONMENT: &str = "GO_ADMIN_DESKTOP_E2E_KEYRING_ACCOUNT";
#[cfg(feature = "native-e2e")]
const E2E_KEYRING_PREFIX: &str = "go-admin-plus-native-e2e-";

#[derive(Clone, Deserialize, Serialize, Zeroize)]
#[zeroize(drop)]
pub struct SessionSecrets {
    pub token: String,
    pub csrf: String,
}

pub struct SessionVault {
    stronghold: Option<Stronghold>,
    memory: std::sync::Mutex<Option<SessionSecrets>>,
    snapshot: PathBuf,
}

impl SessionVault {
    #[cfg(test)]
    pub(crate) fn memory_for_test() -> Self {
        Self {
            stronghold: None,
            memory: std::sync::Mutex::new(None),
            snapshot: PathBuf::new(),
        }
    }

    pub fn open(data_root: &Path) -> Result<Self, &'static str> {
        match Self::open_persistent(data_root) {
            Ok(vault) => Ok(vault),
            Err(_) => {
                eprintln!("系统凭据存储不可用，当前会话仅保存在内存中");
                Ok(Self {
                    stronghold: None,
                    memory: std::sync::Mutex::new(None),
                    snapshot: data_root.join("session.stronghold"),
                })
            }
        }
    }
    fn open_persistent(data_root: &Path) -> Result<Self, &'static str> {
        let key = load_key_with_timeout(Duration::from_secs(3), read_or_create_os_key)?;
        let snapshot = data_root.join("session.stronghold");
        validate_snapshot(&snapshot)?;
        let stronghold =
            Stronghold::new(&snapshot, key.to_vec()).map_err(|_| "stronghold open failed")?;
        if stronghold.load_client(CLIENT_ID).is_err() {
            stronghold
                .create_client(CLIENT_ID)
                .map_err(|_| "stronghold client creation failed")?;
            save_private(&stronghold, &snapshot)?;
        }
        Ok(Self {
            stronghold: Some(stronghold),
            memory: std::sync::Mutex::new(None),
            snapshot,
        })
    }

    pub fn read(&self) -> Result<Option<SessionSecrets>, &'static str> {
        if self.stronghold.is_none() {
            return Ok(self
                .memory
                .lock()
                .map_err(|_| "session memory unavailable")?
                .clone());
        }
        let client = self
            .stronghold
            .as_ref()
            .ok_or("session vault unavailable")?
            .get_client(CLIENT_ID)
            .map_err(|_| "stronghold client unavailable")?;
        let Some(mut encoded) = client
            .store()
            .get(SESSION_KEY)
            .map_err(|_| "stronghold read failed")?
        else {
            return Ok(None);
        };
        let result = serde_json::from_slice(&encoded).map_err(|_| "stronghold record invalid");
        encoded.zeroize();
        result.map(Some)
    }

    pub fn write(&self, secrets: SessionSecrets) -> Result<(), &'static str> {
        if self.stronghold.is_none() {
            *self
                .memory
                .lock()
                .map_err(|_| "session memory unavailable")? = Some(secrets);
            return Ok(());
        }
        let client = self
            .stronghold
            .as_ref()
            .ok_or("session vault unavailable")?
            .get_client(CLIENT_ID)
            .map_err(|_| "stronghold client unavailable")?;
        let mut encoded =
            serde_json::to_vec(&secrets).map_err(|_| "stronghold record encode failed")?;
        client
            .store()
            .insert(SESSION_KEY.to_vec(), std::mem::take(&mut encoded), None)
            .map_err(|_| "stronghold write failed")?;
        encoded.zeroize();
        save_private(
            self.stronghold
                .as_ref()
                .ok_or("session vault unavailable")?,
            &self.snapshot,
        )
    }

    pub fn clear(&self) -> Result<(), &'static str> {
        if self.stronghold.is_none() {
            *self
                .memory
                .lock()
                .map_err(|_| "session memory unavailable")? = None;
            return Ok(());
        }
        let client = self
            .stronghold
            .as_ref()
            .ok_or("session vault unavailable")?
            .get_client(CLIENT_ID)
            .map_err(|_| "stronghold client unavailable")?;
        let _ = client
            .store()
            .delete(SESSION_KEY)
            .map_err(|_| "stronghold clear failed")?;
        save_private(
            self.stronghold
                .as_ref()
                .ok_or("session vault unavailable")?,
            &self.snapshot,
        )
    }

    #[allow(dead_code)]
    pub fn snapshot_path_for_test(&self) -> &Path {
        &self.snapshot
    }
}

// 系统凭据服务可能等待解锁或停在失效的 D-Bus 连接上，不能阻塞应用启动。
// 后台线程只接触系统密钥；超时后丢弃结果，不再打开或改写 Stronghold 文件。
fn load_key_with_timeout(
    timeout: Duration,
    read: impl FnOnce() -> Result<Zeroizing<[u8; KEY_BYTES]>, &'static str> + Send + 'static,
) -> Result<Zeroizing<[u8; KEY_BYTES]>, &'static str> {
    let (sender, receiver) = mpsc::sync_channel(1);
    std::thread::Builder::new()
        .name("desktop-credential-key".into())
        .spawn(move || {
            let _ = sender.send(read());
        })
        .map_err(|_| "credential worker unavailable")?;
    receiver
        .recv_timeout(timeout)
        .map_err(|_| "operating system credential store timed out")?
}

fn read_or_create_os_key() -> Result<Zeroizing<[u8; KEY_BYTES]>, &'static str> {
    let (service, account) = keyring_identity()?;
    let entry = keyring::Entry::new(service, &account)
        .map_err(|_| "operating system credential store unavailable")?;
    match entry.get_secret() {
        Ok(mut stored) => {
            if stored.len() != KEY_BYTES {
                stored.zeroize();
                return Err("stronghold key invalid");
            }
            let mut key = Zeroizing::new([0_u8; KEY_BYTES]);
            key.copy_from_slice(&stored);
            stored.zeroize();
            Ok(key)
        }
        Err(keyring::Error::NoEntry) => {
            let mut key = Zeroizing::new([0_u8; KEY_BYTES]);
            getrandom::fill(&mut *key).map_err(|_| "stronghold key generation failed")?;
            entry
                .set_secret(&*key)
                .map_err(|_| "stronghold key storage failed")?;
            Ok(key)
        }
        Err(_) => Err("stronghold key unavailable"),
    }
}

#[cfg(feature = "native-e2e")]
fn keyring_identity() -> Result<(&'static str, String), &'static str> {
    match std::env::var(E2E_KEYRING_ENVIRONMENT) {
        Ok(account) if valid_e2e_account(&account) => Ok((E2E_KEYRING_SERVICE, account)),
        Ok(_) => Err("native E2E credential identity invalid"),
        Err(std::env::VarError::NotPresent) => Err("native E2E credential identity missing"),
        Err(_) => Err("native E2E credential identity invalid"),
    }
}

#[cfg(not(feature = "native-e2e"))]
fn keyring_identity() -> Result<(&'static str, String), &'static str> {
    Ok((KEYRING_SERVICE, KEYRING_ACCOUNT.to_owned()))
}

#[cfg(feature = "native-e2e")]
fn valid_e2e_account(account: &str) -> bool {
    account
        .strip_prefix(E2E_KEYRING_PREFIX)
        .is_some_and(|suffix| {
            suffix.len() == 32
                && suffix
                    .bytes()
                    .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase())
        })
}

fn validate_snapshot(path: &Path) -> Result<(), &'static str> {
    let parent = path.parent().ok_or("stronghold parent unavailable")?;
    let parent_info = fs::symlink_metadata(parent).map_err(|_| "stronghold parent unavailable")?;
    if !parent_info.is_dir()
        || parent_info.file_type().is_symlink()
        || fs::canonicalize(parent).map_err(|_| "stronghold parent unavailable")? != parent
    {
        return Err("stronghold parent is unsafe");
    }
    match fs::symlink_metadata(path) {
        Ok(info) if info.is_file() && !info.file_type().is_symlink() => {
            validate_snapshot_permissions(&info)
        }
        Ok(_) => Err("stronghold snapshot is unsafe"),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(_) => Err("stronghold snapshot unavailable"),
    }
}

fn save_private(stronghold: &Stronghold, snapshot: &Path) -> Result<(), &'static str> {
    stronghold.save().map_err(|_| "stronghold save failed")?;
    set_snapshot_private(snapshot)?;
    validate_snapshot(snapshot)
}

#[cfg(unix)]
fn validate_snapshot_permissions(info: &fs::Metadata) -> Result<(), &'static str> {
    use std::os::unix::fs::PermissionsExt;
    if info.permissions().mode() & 0o077 != 0 {
        return Err("stronghold snapshot permissions are unsafe");
    }
    Ok(())
}

#[cfg(not(unix))]
fn validate_snapshot_permissions(_info: &fs::Metadata) -> Result<(), &'static str> {
    Ok(())
}

#[cfg(unix)]
fn set_snapshot_private(path: &Path) -> Result<(), &'static str> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o600))
        .map_err(|_| "stronghold snapshot cannot be secured")
}

#[cfg(not(unix))]
fn set_snapshot_private(_path: &Path) -> Result<(), &'static str> {
    // Windows stores the encryption key in Credential Manager. The snapshot is
    // additionally contained by the application data directory ACL.
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_stalled_credential_service_cannot_hold_application_startup() {
        let (release, blocked) = mpsc::channel();
        let result = load_key_with_timeout(Duration::from_millis(20), move || {
            blocked.recv().unwrap();
            Ok(Zeroizing::new([7; KEY_BYTES]))
        });
        assert_eq!(
            result.err(),
            Some("operating system credential store timed out")
        );
        release.send(()).unwrap();
    }

    #[test]
    fn available_credentials_are_returned_without_changing_key_material() {
        let key = load_key_with_timeout(Duration::from_secs(5), || {
            Ok(Zeroizing::new([7; KEY_BYTES]))
        })
        .unwrap();
        assert_eq!(*key, [7; KEY_BYTES]);
    }

    #[cfg(feature = "native-e2e")]
    #[test]
    fn native_e2e_account_is_strictly_isolated() {
        use super::valid_e2e_account;

        assert!(valid_e2e_account(
            "go-admin-plus-native-e2e-0123456789abcdef0123456789abcdef"
        ));
        for value in [
            "desktop-session-vault",
            "go-admin-plus-native-e2e-0123456789ABCDEF0123456789ABCDEF",
            "go-admin-plus-native-e2e-../production",
        ] {
            assert!(!valid_e2e_account(value));
        }
    }
}
