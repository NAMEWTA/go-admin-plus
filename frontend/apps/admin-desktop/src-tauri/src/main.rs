#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

#[cfg(all(not(debug_assertions), not(feature = "custom-protocol")))]
compile_error!("release desktop builds must enable the custom-protocol feature");

mod connection;
mod first_setup;
mod host_capabilities;
mod product_contract;
mod proxy;
mod vault;

use std::{
    env, fs,
    path::{Path, PathBuf},
    sync::{
        Arc, Mutex, RwLock,
        atomic::{AtomicBool, AtomicI32, Ordering},
    },
    time::{Duration, Instant},
};

use base64::{Engine as _, engine::general_purpose::URL_SAFE_NO_PAD};
use first_setup::{FirstSetupInput, FirstSetupOutcome, FirstSetupState};
use proxy::{
    DesktopRequest, DesktopResponse, IdentityResult, LogoutResult, PublicMenu, PublicProfile,
    TransportProxy,
};
use serde::Serialize;
use tauri::{Manager, RunEvent, State};
use tauri_plugin_shell::{
    ShellExt,
    process::{CommandChild, CommandEvent},
};
use zeroize::Zeroizing;

const STARTUP_TIMEOUT: Duration = Duration::from_secs(30);
const HOST_READY_TIMEOUT: Duration = Duration::from_secs(120);
const SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(5);
const MAX_DIAGNOSTIC_BYTES: usize = 4096;
struct HostState {
    proxy: RwLock<Option<Arc<TransportProxy>>>,
    child: Mutex<Option<CommandChild>>,
    child_exited: Arc<AtomicBool>,
    shutting_down: Arc<AtomicBool>,
    runtime_terminal: AtomicBool,
    runtime_changed: tokio::sync::Notify,
    requested_exit_code: AtomicI32,
    requests: Arc<tokio::sync::Semaphore>,
    file_requests: Arc<tokio::sync::Semaphore>,
}

impl HostState {
    fn new() -> Self {
        Self {
            proxy: RwLock::new(None),
            child: Mutex::new(None),
            child_exited: Arc::new(AtomicBool::new(true)),
            shutting_down: Arc::new(AtomicBool::new(false)),
            runtime_terminal: AtomicBool::new(false),
            runtime_changed: tokio::sync::Notify::new(),
            requested_exit_code: AtomicI32::new(0),
            requests: Arc::new(tokio::sync::Semaphore::new(1)),
            file_requests: Arc::new(tokio::sync::Semaphore::new(2)),
        }
    }

    fn proxy(&self) -> Result<Arc<TransportProxy>, &'static str> {
        self.proxy
            .read()
            .map_err(|_| "desktop runtime unavailable")?
            .clone()
            .ok_or("desktop runtime unavailable")
    }

    async fn wait_for_proxy(&self) -> Result<Arc<TransportProxy>, &'static str> {
        let deadline = tokio::time::Instant::now() + HOST_READY_TIMEOUT;
        loop {
            let changed = self.runtime_changed.notified();
            if let Ok(proxy) = self.proxy() {
                return Ok(proxy);
            }
            if self.runtime_terminal.load(Ordering::Acquire) {
                return Err("desktop runtime unavailable");
            }
            let now = tokio::time::Instant::now();
            if now >= deadline {
                return Err("desktop runtime startup timed out");
            }
            let recheck = std::cmp::min(deadline, now + Duration::from_millis(50));
            let _ = tokio::time::timeout_at(recheck, changed).await;
        }
    }

    fn child_spawned(&self) {
        self.child_exited.store(false, Ordering::Release);
    }

    fn child_terminated(&self) {
        self.child_exited.store(true, Ordering::Release);
    }

    fn fail_and_exit(&self, app: &tauri::AppHandle) {
        self.runtime_terminal.store(true, Ordering::Release);
        self.runtime_changed.notify_waiters();
        self.requested_exit_code.store(1, Ordering::Release);
        app.exit(1);
    }

    fn shutdown(&self) {
        self.shutting_down.store(true, Ordering::Release);
        self.runtime_terminal.store(true, Ordering::Release);
        self.runtime_changed.notify_waiters();
        if let Ok(proxy) = self.proxy() {
            proxy.shutdown();
        }
        let deadline = Instant::now() + SHUTDOWN_TIMEOUT;
        while !self.child_exited.load(Ordering::Acquire) && Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(25));
        }
        if !self.child_exited.load(Ordering::Acquire)
            && let Ok(mut owner) = self.child.lock()
            && let Some(child) = owner.take()
        {
            let _ = child.kill();
            let kill_deadline = Instant::now() + SHUTDOWN_TIMEOUT;
            while !self.child_exited.load(Ordering::Acquire) && Instant::now() < kill_deadline {
                std::thread::sleep(Duration::from_millis(25));
            }
        }
        if let Ok(mut owner) = self.child.lock() {
            owner.take();
        }
        if let Ok(mut owner) = self.proxy.write() {
            owner.take();
        }
    }
}

#[tauri::command]
fn desktop_connection_get(app: tauri::AppHandle) -> Result<connection::Status, &'static str> {
    connection::read(&app)
}

#[tauri::command]
async fn desktop_connection_test(settings: connection::Settings) -> Result<(), &'static str> {
    tauri::async_runtime::spawn_blocking(move || connection::test(settings))
        .await
        .map_err(|_| "connection test failed")?
}

#[tauri::command]
async fn desktop_connection_set(
    app: tauri::AppHandle,
    state: State<'_, Arc<HostState>>,
    settings: connection::Settings,
) -> Result<(), &'static str> {
    let settings = connection::validate(settings)?;
    connection::save(&app, &settings)?;
    if let Ok(proxy) = state.proxy() {
        let _ = tauri::async_runtime::spawn_blocking(move || proxy.logout()).await;
    }
    state.shutdown();
    app.restart();
}

#[cfg(not(feature = "native-e2e"))]
fn show_main_window(app: &tauri::AppHandle) -> Result<(), &'static str> {
    let window = app
        .get_webview_window("main")
        .ok_or("desktop main window unavailable")?;
    window.show().map_err(|_| "desktop main window failed")?;
    let _ = window.set_focus();
    Ok(())
}

#[tauri::command]
async fn desktop_request(
    state: State<'_, Arc<HostState>>,
    request: DesktopRequest,
) -> Result<DesktopResponse, &'static str> {
    let proxy = state.wait_for_proxy().await?;
    let permit = state
        .requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    let result = tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        proxy.business(request)
    })
    .await
    .map_err(|_| "desktop request failed")?;
    #[cfg(feature = "native-e2e")]
    if let Err(error) = &result {
        eprintln!("desktop native request failed: {error}");
    }
    result
}

#[tauri::command]
async fn desktop_identity(
    state: State<'_, Arc<HostState>>,
) -> Result<IdentityResult, &'static str> {
    let proxy = state.wait_for_proxy().await?;
    let permit = state
        .requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    let result = tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        proxy.identity()
    })
    .await
    .map_err(|_| "desktop identity failed")?;
    #[cfg(feature = "native-e2e")]
    if let Err(error) = &result {
        eprintln!("desktop native identity failed: {error}");
    }
    #[cfg(feature = "native-e2e")]
    if matches!(&result, Ok(IdentityResult::Unauthenticated)) {
        eprintln!("desktop native identity state: unauthenticated");
    }
    result
}

#[tauri::command]
async fn desktop_first_setup_state(
    state: State<'_, Arc<HostState>>,
) -> Result<FirstSetupState, &'static str> {
    let proxy = state.wait_for_proxy().await?;
    let permit = state
        .requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        proxy.first_setup_state()
    })
    .await
    .map_err(|_| "desktop first setup state failed")?
}

#[tauri::command]
async fn desktop_first_setup_submit(
    state: State<'_, Arc<HostState>>,
    input: FirstSetupInput,
) -> Result<FirstSetupOutcome, &'static str> {
    let proxy = state.wait_for_proxy().await?;
    let permit = state
        .requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        proxy.first_setup_submit(input)
    })
    .await
    .map_err(|_| "desktop first setup failed")?
}

#[tauri::command]
async fn desktop_navigation(
    state: State<'_, Arc<HostState>>,
) -> Result<Vec<PublicMenu>, &'static str> {
    let proxy = state.wait_for_proxy().await?;
    let permit = state
        .requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        proxy.navigation()
    })
    .await
    .map_err(|_| "desktop navigation failed")?
}

#[tauri::command]
async fn desktop_login(
    state: State<'_, Arc<HostState>>,
    username: String,
    password: String,
) -> Result<PublicProfile, &'static str> {
    let proxy = state.wait_for_proxy().await?;
    let permit = state
        .requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    let username = Zeroizing::new(username);
    let password = Zeroizing::new(password);
    let result = tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        proxy.login(username, password)
    })
    .await
    .map_err(|_| "desktop login failed")?;
    #[cfg(feature = "native-e2e")]
    if let Err(error) = &result {
        eprintln!("desktop native login failed: {error}");
    }
    result
}

#[tauri::command]
async fn desktop_logout(state: State<'_, Arc<HostState>>) -> Result<LogoutResult, &'static str> {
    let proxy = state.wait_for_proxy().await?;
    let permit = state
        .requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    #[cfg(feature = "native-e2e")]
    eprintln!("desktop native logout state: started");
    let result = tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        proxy.logout()
    })
    .await
    .map_err(|_| "desktop logout failed")?;
    #[cfg(feature = "native-e2e")]
    if let Err(error) = &result {
        eprintln!("desktop native logout failed: {error}");
    }
    #[cfg(feature = "native-e2e")]
    if result.is_ok() {
        eprintln!("desktop native logout state: command-complete");
    }
    result
}

async fn run_session_maintenance(
    state: State<'_, Arc<HostState>>,
    renew: bool,
) -> Result<PublicProfile, &'static str> {
    let proxy = state.wait_for_proxy().await?;
    let permit = state
        .requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        if renew {
            proxy.renew()
        } else {
            proxy.heartbeat()
        }
    })
    .await
    .map_err(|_| "desktop session maintenance failed")?
}

#[tauri::command]
async fn desktop_session_heartbeat(
    state: State<'_, Arc<HostState>>,
) -> Result<PublicProfile, &'static str> {
    run_session_maintenance(state, false).await
}

#[tauri::command]
async fn desktop_session_renew(
    state: State<'_, Arc<HostState>>,
) -> Result<PublicProfile, &'static str> {
    run_session_maintenance(state, true).await
}

#[tauri::command]
async fn desktop_upload_managed_file(
    app: tauri::AppHandle,
    state: State<'_, Arc<HostState>>,
) -> Result<bool, &'static str> {
    use tauri_plugin_dialog::DialogExt;
    let proxy = state.proxy()?;
    let permit = state
        .file_requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        let Some(selected) = app.dialog().file().blocking_pick_file() else {
            return Ok(false);
        };
        let path = selected.into_path().map_err(|_| "selected file invalid")?;
        proxy.upload_file(path)?;
        Ok(true)
    })
    .await
    .map_err(|_| "upload failed")?
}
#[tauri::command]
async fn desktop_download_managed_file(
    app: tauri::AppHandle,
    state: State<'_, Arc<HostState>>,
    id: String,
    name: String,
) -> Result<bool, &'static str> {
    use tauri_plugin_dialog::DialogExt;
    if name.is_empty() || name.contains(['/', '\\']) || name.chars().any(char::is_control) {
        return Err("file name invalid");
    }
    let proxy = state.proxy()?;
    let permit = state
        .file_requests
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| "desktop runtime unavailable")?;
    tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        let Some(selected) = app.dialog().file().set_file_name(name).blocking_save_file() else {
            return Ok(false);
        };
        let path = selected.into_path().map_err(|_| "save path invalid")?;
        proxy.download_file(&id, path)?;
        Ok(true)
    })
    .await
    .map_err(|_| "download failed")?
}

#[tauri::command]
async fn desktop_pick_file(
    app: tauri::AppHandle,
) -> Result<Option<host_capabilities::PickedFileOutput>, &'static str> {
    host_capabilities::pick_file(app).await
}

#[tauri::command]
async fn desktop_save_file(
    app: tauri::AppHandle,
    file: host_capabilities::SaveFileInput,
) -> Result<host_capabilities::SaveFileOutput, &'static str> {
    host_capabilities::save_file(app, file).await
}

#[tauri::command]
async fn desktop_notify(app: tauri::AppHandle, message: String) -> Result<(), &'static str> {
    host_capabilities::notify(&app, message)
}

#[tauri::command]
async fn desktop_write_clipboard(app: tauri::AppHandle, text: String) -> Result<(), &'static str> {
    host_capabilities::write_clipboard(app, text).await
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct LaunchWire<'a> {
    data_directory: &'a Path,
    log_directory: &'a Path,
    loopback_port: u16,
    readiness_nonce: &'a str,
    control_token: &'a str,
}

async fn start_runtime(app: tauri::AppHandle, state: Arc<HostState>) -> Result<(), &'static str> {
    #[cfg(not(feature = "native-e2e"))]
    {
        show_main_window(&app)?;
        let status = match connection::read(&app) {
            Ok(status) => status,
            Err(_) => return Ok(()),
        };
        if !status.configured {
            show_main_window(&app)?;
            return Ok(());
        }
        if status.settings.mode == connection::Mode::Remote {
            let (data_path, _) = runtime_paths(&app)?;
            let result = tauri::async_runtime::spawn_blocking(move || {
                connection::test(status.settings.clone())?;
                let vault_root =
                    prepare_root(data_path.join(connection::vault_directory(&status.settings)))?;
                let vault = vault::SessionVault::open(&vault_root)?;
                TransportProxy::remote(&status.settings, vault).map(Arc::new)
            })
            .await
            .map_err(|_| "remote startup failed")
            .and_then(|value| value);
            let proxy = match result {
                Ok(proxy) => proxy,
                Err(_) => {
                    // 远程故障保留设置入口，用户可以修复地址/证书或切回本地模式。
                    state.runtime_terminal.store(true, Ordering::Release);
                    state.runtime_changed.notify_waiters();
                    eprintln!("远程服务连接失败，请检查连接设置、网络和证书");
                    return Ok(());
                }
            };
            *state
                .proxy
                .write()
                .map_err(|_| "desktop runtime unavailable")? = Some(proxy);
            state.runtime_changed.notify_waiters();
            show_main_window(&app)?;
            return Ok(());
        }
    }

    let (data_path, log_path) = runtime_paths(&app)?;
    let data_root = prepare_root(data_path)?;
    let log_root = prepare_root(log_path)?;
    reject_overlap(&data_root, &log_root)?;
    let readiness_nonce = Zeroizing::new(random_secret()?);
    let control_token = Zeroizing::new(random_secret()?);
    let mut launch = serde_json::to_vec(&LaunchWire {
        data_directory: &data_root,
        log_directory: &log_root,
        loopback_port: 0,
        readiness_nonce: &readiness_nonce,
        control_token: &control_token,
    })
    .map_err(|_| "desktop launch material encode failed")?;
    launch.push(b'\n');

    let command = app
        .shell()
        .sidecar("go-admin-sidecar")
        .map_err(|_| "desktop sidecar command unavailable")?
        .env_clear();
    let (mut events, mut child) = command
        .spawn()
        .map_err(|_| "desktop sidecar spawn failed")?;
    state.child_spawned();
    if child.write(&launch).is_err() {
        launch.fill(0);
        stop_startup_child(&state, child, &mut events).await?;
        return Err("desktop sidecar input failed");
    }
    launch.fill(0);

    let (port, observed) = match wait_for_listening(&mut events).await {
        Ok(value) => value,
        Err(StartupFailure::Terminated) => {
            state.child_terminated();
            return Err("desktop sidecar stopped during startup");
        }
        Err(StartupFailure::Active(error)) => {
            stop_startup_child(&state, child, &mut events).await?;
            return Err(error);
        }
    };
    let origin = format!("http://127.0.0.1:{port}");
    let observed = match readiness_handshake_with_events(
        &origin,
        readiness_nonce,
        &mut events,
        observed,
    )
    .await
    {
        Ok(observed) => observed,
        Err(error) => {
            stop_startup_child(&state, child, &mut events).await?;
            return Err(error);
        }
    };
    let vault = match vault::SessionVault::open(&data_root) {
        Ok(vault) => vault,
        Err(error) => {
            stop_startup_child(&state, child, &mut events).await?;
            return Err(error);
        }
    };
    let proxy = match TransportProxy::new(origin, control_token, vault) {
        Ok(proxy) => Arc::new(proxy),
        Err(error) => {
            stop_startup_child(&state, child, &mut events).await?;
            return Err(error);
        }
    };
    *state
        .proxy
        .write()
        .map_err(|_| "desktop runtime unavailable")? = Some(proxy);
    state.runtime_changed.notify_waiters();
    *state
        .child
        .lock()
        .map_err(|_| "desktop runtime unavailable")? = Some(child);

    let exited = Arc::clone(&state.child_exited);
    let shutting_down = Arc::clone(&state.shutting_down);
    let monitor_state = Arc::clone(&state);
    let monitor_app = app.clone();
    tauri::async_runtime::spawn(async move {
        let mut observed = observed;
        let mut output_failed = false;
        while let Some(event) = events.recv().await {
            match event {
                CommandEvent::Stdout(bytes) | CommandEvent::Stderr(bytes) => {
                    observed = observed
                        .saturating_add(bytes.len())
                        .min(MAX_DIAGNOSTIC_BYTES + 1);
                    if observed > MAX_DIAGNOSTIC_BYTES {
                        output_failed = true;
                        if let Ok(mut owner) = monitor_state.child.lock()
                            && let Some(child) = owner.take()
                        {
                            let _ = child.kill();
                        }
                    }
                }
                CommandEvent::Terminated(_) => {
                    exited.store(true, Ordering::Release);
                    if !shutting_down.load(Ordering::Acquire) {
                        monitor_state.fail_and_exit(&monitor_app);
                    }
                    return;
                }
                _ => {}
            }
        }
        if let Ok(mut owner) = monitor_state.child.lock()
            && let Some(child) = owner.take()
        {
            let _ = child.kill();
        }
        exited.store(true, Ordering::Release);
        if output_failed || !shutting_down.load(Ordering::Acquire) {
            monitor_state.fail_and_exit(&monitor_app);
        }
    });

    let window = app
        .get_webview_window("main")
        .ok_or("desktop main window unavailable")?;
    window.show().map_err(|_| "desktop main window failed")?;
    #[cfg(target_os = "macos")]
    app.show().map_err(|_| "desktop application failed")?;
    let _ = window.set_focus();
    Ok(())
}

fn runtime_paths(app: &tauri::AppHandle) -> Result<(PathBuf, PathBuf), &'static str> {
    #[cfg(debug_assertions)]
    if let Some(root) = std::env::var_os("GO_ADMIN_DESKTOP_DATA_ROOT") {
        let root = PathBuf::from(root);
        if !root.is_absolute() {
            return Err("desktop development data root invalid");
        }
        return Ok((root.join("data"), root.join("logs")));
    }
    #[cfg(feature = "native-e2e")]
    if let Some(root) = std::env::var_os("GO_ADMIN_DESKTOP_E2E_ROOT") {
        let root = PathBuf::from(root);
        if !root.is_absolute() {
            return Err("desktop e2e root invalid");
        }
        let root = fs::canonicalize(root).map_err(|_| "desktop e2e root invalid")?;
        return Ok((root.join("data"), root.join("logs")));
    }
    let data = app
        .path()
        .app_local_data_dir()
        .map_err(|_| "desktop application data path unavailable")?;
    let logs = app
        .path()
        .app_log_dir()
        .map_err(|_| "desktop application log path unavailable")?;
    Ok((data.join("data"), logs))
}

enum StartupFailure {
    Active(&'static str),
    Terminated,
}

async fn wait_for_listening(
    events: &mut tokio::sync::mpsc::Receiver<CommandEvent>,
) -> Result<(u16, usize), StartupFailure> {
    let deadline = tokio::time::Instant::now() + STARTUP_TIMEOUT;
    let mut stdout = Vec::new();
    let mut observed = 0usize;
    loop {
        let event = tokio::time::timeout_at(deadline, events.recv())
            .await
            .map_err(|_| StartupFailure::Active("desktop sidecar startup timed out"))?
            .ok_or(StartupFailure::Terminated)?;
        match event {
            CommandEvent::Stdout(bytes) => {
                if count_startup_output(&mut observed, bytes.len()).is_err() {
                    return Err(StartupFailure::Active(
                        "desktop sidecar startup output rejected",
                    ));
                }
                stdout.extend_from_slice(&bytes);
                if let Some(position) = stdout.iter().position(|byte| *byte == b'\n') {
                    #[derive(serde::Deserialize)]
                    #[serde(deny_unknown_fields)]
                    struct Listening {
                        state: String,
                        port: u16,
                    }
                    let value: Listening =
                        serde_json::from_slice(&stdout[..position]).map_err(|_| {
                            StartupFailure::Active("desktop sidecar startup response invalid")
                        })?;
                    stdout.fill(0);
                    if value.state == "listening" && value.port > 0 {
                        return Ok((value.port, observed));
                    }
                    return Err(StartupFailure::Active(
                        "desktop sidecar startup response invalid",
                    ));
                }
            }
            CommandEvent::Stderr(bytes) => {
                if count_startup_output(&mut observed, bytes.len()).is_err() {
                    return Err(StartupFailure::Active(
                        "desktop sidecar startup output rejected",
                    ));
                }
            }
            CommandEvent::Terminated(_) => return Err(StartupFailure::Terminated),
            CommandEvent::Error(_) => {
                return Err(StartupFailure::Active(
                    "desktop sidecar startup output rejected",
                ));
            }
            _ => {}
        }
    }
}

fn count_startup_output(observed: &mut usize, bytes: usize) -> Result<(), &'static str> {
    *observed = observed.saturating_add(bytes);
    if *observed > MAX_DIAGNOSTIC_BYTES {
        return Err("desktop sidecar startup output rejected");
    }
    Ok(())
}

async fn readiness_handshake_with_events(
    origin: &str,
    nonce: Zeroizing<String>,
    events: &mut tokio::sync::mpsc::Receiver<CommandEvent>,
    mut observed: usize,
) -> Result<usize, &'static str> {
    let handshake = readiness_handshake(origin, nonce);
    tokio::pin!(handshake);
    loop {
        tokio::select! {
            result = &mut handshake => return result.map(|_| observed),
            event = events.recv() => match event {
                Some(CommandEvent::Stdout(bytes)) | Some(CommandEvent::Stderr(bytes)) => {
                    count_startup_output(&mut observed, bytes.len())?;
                }
                Some(CommandEvent::Terminated(_)) | None => return Err("desktop sidecar stopped during startup"),
                Some(CommandEvent::Error(_)) => return Err("desktop sidecar startup output rejected"),
                Some(_) => {}
            }
        }
    }
}

async fn stop_startup_child(
    state: &HostState,
    child: CommandChild,
    events: &mut tokio::sync::mpsc::Receiver<CommandEvent>,
) -> Result<(), &'static str> {
    let _ = child.kill();
    let deadline = tokio::time::Instant::now() + SHUTDOWN_TIMEOUT;
    loop {
        match tokio::time::timeout_at(deadline, events.recv()).await {
            Ok(Some(CommandEvent::Terminated(_))) | Ok(None) => {
                state.child_terminated();
                return Ok(());
            }
            Ok(Some(_)) => {}
            Err(_) => return Err("desktop sidecar startup cleanup failed"),
        }
    }
}

async fn readiness_handshake(origin: &str, nonce: Zeroizing<String>) -> Result<(), &'static str> {
    let origin = origin.to_owned();
    tauri::async_runtime::spawn_blocking(move || {
        let deadline = Instant::now() + STARTUP_TIMEOUT;
        while Instant::now() < deadline {
            let result = ureq::get(format!("{origin}/__desktop/ready"))
                .header("X-Go-Admin-Desktop-Nonce", nonce.as_str())
                .config()
                .timeout_global(Some(Duration::from_secs(1)))
                .build()
                .call();
            if let Ok(mut response) = result
                && response.status() == 200
            {
                let value: serde_json::Value = response
                    .body_mut()
                    .read_json()
                    .map_err(|_| "desktop readiness response invalid")?;
                if value == serde_json::json!({"state":"ready"}) {
                    return Ok(());
                }
                return Err("desktop readiness response invalid");
            }
            std::thread::sleep(Duration::from_millis(50));
        }
        Err("desktop readiness timed out")
    })
    .await
    .map_err(|_| "desktop readiness failed")?
}

fn prepare_root(path: PathBuf) -> Result<PathBuf, &'static str> {
    validate_existing_ancestors(&path)?;
    fs::create_dir_all(&path).map_err(|_| "desktop runtime directory unavailable")?;
    let canonical = fs::canonicalize(&path).map_err(|_| "desktop runtime directory unavailable")?;
    let info = fs::symlink_metadata(&path).map_err(|_| "desktop runtime directory unavailable")?;
    if !info.is_dir() || info.file_type().is_symlink() {
        return Err("desktop runtime directory is not canonical");
    }
    if !canonical_paths_equal(&canonical, &path) {
        return Err("desktop runtime directory is not canonical");
    }
    set_private_directory(&path)?;
    Ok(canonical)
}

fn validate_existing_ancestors(path: &Path) -> Result<(), &'static str> {
    use std::path::Component;

    if !path.is_absolute()
        || path
            .components()
            .any(|part| matches!(part, Component::CurDir | Component::ParentDir))
    {
        return Err("desktop runtime directory path is invalid");
    }
    let mut ancestors: Vec<_> = path.ancestors().collect();
    ancestors.reverse();
    for ancestor in ancestors {
        match fs::symlink_metadata(ancestor) {
            Ok(info) if info.is_dir() && !info.file_type().is_symlink() => {}
            Ok(_) => return Err("desktop runtime directory ancestor is unsafe"),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(_) => return Err("desktop runtime directory unavailable"),
        }
    }
    Ok(())
}

fn canonical_paths_equal(canonical: &Path, requested: &Path) -> bool {
    #[cfg(windows)]
    {
        normalize_windows_extended_path(canonical) == normalize_windows_extended_path(requested)
    }
    #[cfg(not(windows))]
    {
        canonical == requested
    }
}

#[cfg(windows)]
fn normalize_windows_extended_path(path: &Path) -> PathBuf {
    use std::{
        ffi::OsString,
        os::windows::ffi::{OsStrExt, OsStringExt},
    };

    const EXTENDED_PREFIX: &[u16] = &[b'\\' as u16, b'\\' as u16, b'?' as u16, b'\\' as u16];
    const EXTENDED_UNC_PREFIX: &[u16] = &[
        b'\\' as u16,
        b'\\' as u16,
        b'?' as u16,
        b'\\' as u16,
        b'U' as u16,
        b'N' as u16,
        b'C' as u16,
        b'\\' as u16,
    ];
    let value: Vec<u16> = path.as_os_str().encode_wide().collect();
    let normalized = if value.starts_with(EXTENDED_UNC_PREFIX) {
        let mut result = vec![b'\\' as u16, b'\\' as u16];
        result.extend_from_slice(&value[EXTENDED_UNC_PREFIX.len()..]);
        result
    } else if value.starts_with(EXTENDED_PREFIX) {
        value[EXTENDED_PREFIX.len()..].to_vec()
    } else {
        value
    };
    PathBuf::from(OsString::from_wide(&normalized))
}

#[cfg(unix)]
fn set_private_directory(path: &Path) -> Result<(), &'static str> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))
        .map_err(|_| "desktop runtime directory cannot be secured")
}
#[cfg(not(unix))]
fn set_private_directory(_path: &Path) -> Result<(), &'static str> {
    Ok(())
}

fn reject_overlap(first: &Path, second: &Path) -> Result<(), &'static str> {
    if first == second || first.starts_with(second) || second.starts_with(first) {
        return Err("desktop runtime directories overlap");
    }
    Ok(())
}

fn random_secret() -> Result<String, &'static str> {
    let mut value = [0_u8; 32];
    getrandom::fill(&mut value).map_err(|_| "desktop launch secret generation failed")?;
    let encoded = URL_SAFE_NO_PAD.encode(value);
    value.fill(0);
    Ok(encoded)
}

fn host_exit_code(runtime: i32, requested: i32) -> i32 {
    if requested == 0 { runtime } else { requested }
}

#[cfg(feature = "native-e2e")]
const NATIVE_E2E_DATA_STORE_IDENTIFIER: [u8; 16] = [
    103, 111, 97, 100, 109, 105, 78, 80, 172, 117, 115, 101, 50, 101, 48, 49,
];

fn desktop_context() -> tauri::Context<tauri::Wry> {
    let context = tauri::generate_context!();
    #[cfg(feature = "native-e2e")]
    let mut context = context;
    #[cfg(feature = "native-e2e")]
    {
        let windows = &mut context.config_mut().app.windows;
        assert_eq!(windows.len(), 1, "native E2E requires exactly one window");
        assert_eq!(
            windows[0].label, "main",
            "native E2E requires the main window"
        );
        windows[0].data_store_identifier = Some(NATIVE_E2E_DATA_STORE_IDENTIFIER);
    }
    context
}

fn main() {
    let state = Arc::new(HostState::new());
    let managed = Arc::clone(&state);
    let exit_state = Arc::clone(&state);
    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _, _| {
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.set_focus();
            }
        }))
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_clipboard_manager::init())
        .manage(managed)
        .invoke_handler(tauri::generate_handler![
            desktop_connection_get,
            desktop_connection_test,
            desktop_connection_set,
            desktop_request,
            desktop_identity,
            desktop_first_setup_state,
            desktop_first_setup_submit,
            desktop_navigation,
            desktop_login,
            desktop_logout,
            desktop_session_heartbeat,
            desktop_session_renew,
            desktop_upload_managed_file,
            desktop_download_managed_file,
            desktop_pick_file,
            desktop_save_file,
            desktop_notify,
            desktop_write_clipboard
        ])
        .setup({
            let state = Arc::clone(&state);
            move |app| {
                let handle = app.handle().clone();
                tauri::async_runtime::spawn(async move {
                    if let Err(_error) = start_runtime(handle.clone(), Arc::clone(&state)).await {
                        #[cfg(feature = "native-e2e")]
                        eprintln!("desktop native startup failed: {_error}");
                        state.fail_and_exit(&handle);
                    }
                });
                Ok(())
            }
        });
    let app = builder
        .build(desktop_context())
        .expect("desktop host initialization failed");
    let exit_code = app.run_return(move |_handle, event| {
        if let RunEvent::Exit = event {
            state.shutdown();
        }
    });
    std::process::exit(host_exit_code(
        exit_code,
        exit_state.requested_exit_code.load(Ordering::Acquire),
    ));
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(feature = "native-e2e")]
    #[test]
    fn native_e2e_context_uses_the_isolated_data_store() {
        let context = desktop_context();
        let windows = &context.config().app.windows;
        assert_eq!(windows.len(), 1);
        assert_eq!(windows[0].label, "main");
        assert_eq!(
            windows[0].data_store_identifier,
            Some(NATIVE_E2E_DATA_STORE_IDENTIFIER)
        );
    }

    #[test]
    fn random_material_is_url_safe_and_independent() {
        let first = random_secret().unwrap();
        let second = random_secret().unwrap();
        assert_eq!(first.len(), 43);
        assert_ne!(first, second);
        assert!(
            first
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-' || byte == b'_')
        );
    }

    #[test]
    fn nested_runtime_roots_are_rejected() {
        assert!(reject_overlap(Path::new("/data"), Path::new("/data/logs")).is_err());
        assert!(reject_overlap(Path::new("/data"), Path::new("/logs")).is_ok());
    }

    #[cfg(windows)]
    #[test]
    fn windows_extended_paths_remain_strictly_canonical() {
        assert!(canonical_paths_equal(
            Path::new(r"\\?\C:\private\data"),
            Path::new(r"C:\private\data"),
        ));
        assert!(canonical_paths_equal(
            Path::new(r"\\?\UNC\server\share\data"),
            Path::new(r"\\server\share\data"),
        ));
        assert!(!canonical_paths_equal(
            Path::new(r"\\?\C:\private\other"),
            Path::new(r"C:\private\data"),
        ));
    }

    #[test]
    fn host_without_a_spawned_child_is_already_reaped() {
        let state = HostState::new();
        assert!(state.child_exited.load(Ordering::Acquire));
        let started = Instant::now();
        state.shutdown();
        assert!(started.elapsed() < Duration::from_secs(1));
    }

    #[test]
    fn child_lifecycle_and_nonzero_host_exit_are_preserved() {
        let state = HostState::new();
        state.child_spawned();
        assert!(!state.child_exited.load(Ordering::Acquire));
        state.child_terminated();
        assert!(state.child_exited.load(Ordering::Acquire));
        assert_eq!(host_exit_code(0, 1), 1);
        assert_eq!(host_exit_code(7, 0), 7);
        assert_eq!(state.requests.available_permits(), 1);
        let permit = state.requests.try_acquire().unwrap();
        assert!(state.requests.try_acquire().is_err());
        drop(permit);
        assert!(state.requests.try_acquire().is_ok());
    }

    #[tokio::test]
    async fn concurrent_host_exchanges_wait_for_the_singleflight_permit() {
        let requests = Arc::new(tokio::sync::Semaphore::new(1));
        let first = Arc::clone(&requests).acquire_owned().await.unwrap();
        let queued_requests = Arc::clone(&requests);
        let queued = tokio::spawn(async move { queued_requests.acquire_owned().await.unwrap() });
        tokio::task::yield_now().await;
        assert!(!queued.is_finished());
        drop(first);
        let second = tokio::time::timeout(Duration::from_secs(1), queued)
            .await
            .unwrap()
            .unwrap();
        drop(second);
        assert_eq!(requests.available_permits(), 1);
    }

    #[cfg(unix)]
    #[test]
    fn symlinked_parent_is_rejected_before_creating_child() {
        use std::os::unix::fs::symlink;

        let unique = format!("go-admin-desktop-root-{}", random_secret().unwrap());
        let temporary = fs::canonicalize(std::env::temp_dir()).unwrap().join(unique);
        let outside = temporary.join("outside");
        let linked = temporary.join("linked");
        fs::create_dir_all(&outside).unwrap();
        symlink(&outside, &linked).unwrap();
        let requested = linked.join("missing");
        assert!(prepare_root(requested).is_err());
        assert!(!outside.join("missing").exists());
        fs::remove_dir_all(&temporary).unwrap();
    }
}
