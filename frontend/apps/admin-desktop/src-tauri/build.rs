fn main() {
    let manifest = tauri_build::AppManifest::new().commands(&[
        "desktop_connection_get",
        "desktop_connection_test",
        "desktop_connection_set",
        "desktop_request",
        "desktop_identity",
        "desktop_first_setup_state",
        "desktop_first_setup_submit",
        "desktop_navigation",
        "desktop_login",
        "desktop_logout",
        "desktop_session_heartbeat",
        "desktop_session_renew",
        "desktop_upload_managed_file",
        "desktop_download_managed_file",
        "desktop_pick_file",
        "desktop_save_file",
        "desktop_notify",
        "desktop_write_clipboard",
    ]);
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(manifest))
        .expect("failed to build Tauri application manifest");
}
