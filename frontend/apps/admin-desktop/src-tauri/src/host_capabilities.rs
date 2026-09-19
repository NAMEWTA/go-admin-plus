use std::{
    fs::{self, File, OpenOptions},
    io::{Read, Write},
    path::{Path, PathBuf},
};

use base64::{Engine as _, engine::general_purpose::STANDARD};
use serde::{Deserialize, Serialize};
use tauri::AppHandle;
use tauri_plugin_clipboard_manager::ClipboardExt;
use tauri_plugin_dialog::DialogExt;
use tauri_plugin_notification::NotificationExt;
use zeroize::Zeroizing;

const MAXIMUM_FILE_BYTES: usize = 10 * 1024 * 1024;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct SaveFileInput {
    name: String,
    media_type: String,
    data: String,
}

struct DecodedHostFile {
    name: String,
    media_type: &'static str,
    bytes: Zeroizing<Vec<u8>>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PickedFileOutput {
    name: String,
    media_type: &'static str,
    size_bytes: usize,
    data: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct SaveFileOutput {
    status: &'static str,
}

fn valid_file_name(name: &str) -> bool {
    let trimmed = name.trim();
    let length = trimmed.chars().count();
    (1..=255).contains(&length)
        && trimmed != "."
        && trimmed != ".."
        && !trimmed.contains(['/', '\\'])
        && !trimmed.chars().any(char::is_control)
}

fn classify_file(name: &str, bytes: &[u8]) -> Result<&'static str, &'static str> {
    if !valid_file_name(name) || bytes.len() > MAXIMUM_FILE_BYTES {
        return Err("desktop host file invalid");
    }
    let extension = Path::new(name)
        .extension()
        .and_then(|value| value.to_str())
        .map(str::to_ascii_lowercase)
        .ok_or("desktop host file invalid")?;
    match extension.as_str() {
        "pdf" if bytes.starts_with(b"%PDF-") => Ok("application/pdf"),
        "jpg" | "jpeg" if bytes.starts_with(&[0xff, 0xd8, 0xff]) => Ok("image/jpeg"),
        "png" if bytes.starts_with(b"\x89PNG\r\n\x1a\n") => Ok("image/png"),
        "txt"
            if std::str::from_utf8(bytes).is_ok()
                && !bytes
                    .iter()
                    .any(|byte| matches!(*byte, 0..=8 | 11..=12 | 14..=31 | 127)) =>
        {
            Ok("text/plain")
        }
        _ => Err("desktop host file invalid"),
    }
}

fn decode_save_file(input: SaveFileInput) -> Result<DecodedHostFile, &'static str> {
    let encoded = Zeroizing::new(input.data);
    let bytes = STANDARD
        .decode(encoded.as_bytes())
        .map_err(|_| "desktop host file invalid")?;
    if bytes.len() > MAXIMUM_FILE_BYTES || STANDARD.encode(&bytes) != encoded.as_str() {
        return Err("desktop host file invalid");
    }
    let media_type = classify_file(&input.name, &bytes)?;
    if media_type != input.media_type {
        return Err("desktop host file invalid");
    }
    Ok(DecodedHostFile {
        name: input.name,
        media_type,
        bytes: Zeroizing::new(bytes),
    })
}

fn read_selected_file(path: PathBuf) -> Result<PickedFileOutput, &'static str> {
    let metadata = fs::symlink_metadata(&path).map_err(|_| "desktop selected file unavailable")?;
    if !metadata.is_file() || metadata.file_type().is_symlink() {
        return Err("desktop selected file invalid");
    }
    let name = path
        .file_name()
        .and_then(|value| value.to_str())
        .ok_or("desktop selected file invalid")?
        .to_owned();
    let file = File::open(&path).map_err(|_| "desktop selected file unavailable")?;
    let mut bytes = Zeroizing::new(Vec::with_capacity(
        usize::try_from(metadata.len())
            .unwrap_or(MAXIMUM_FILE_BYTES + 1)
            .min(MAXIMUM_FILE_BYTES + 1),
    ));
    file.take((MAXIMUM_FILE_BYTES + 1) as u64)
        .read_to_end(&mut bytes)
        .map_err(|_| "desktop selected file unavailable")?;
    let media_type = classify_file(&name, &bytes)?;
    Ok(PickedFileOutput {
        name,
        media_type,
        size_bytes: bytes.len(),
        data: STANDARD.encode(bytes.as_slice()),
    })
}

fn temporary_download_path(parent: &Path) -> Result<PathBuf, &'static str> {
    let mut random = [0_u8; 16];
    getrandom::fill(&mut random).map_err(|_| "desktop save file failed")?;
    let suffix = random
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect::<String>();
    random.fill(0);
    Ok(parent.join(format!(".go-admin-plus-{suffix}.tmp")))
}

fn replace_download(path: &Path, file: DecodedHostFile) -> Result<(), &'static str> {
    let target_name = path
        .file_name()
        .and_then(|value| value.to_str())
        .ok_or("desktop save path invalid")?;
    if classify_file(target_name, &file.bytes)? != file.media_type {
        return Err("desktop save path invalid");
    }
    match fs::symlink_metadata(path) {
        Ok(metadata) if !metadata.is_file() || metadata.file_type().is_symlink() => {
            return Err("desktop save path invalid");
        }
        Ok(_) => {}
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(_) => return Err("desktop save path unavailable"),
    }
    let parent = path.parent().ok_or("desktop save path invalid")?;
    let temporary = temporary_download_path(parent)?;
    let result = (|| {
        let mut output = OpenOptions::new()
            .create_new(true)
            .write(true)
            .open(&temporary)
            .map_err(|_| "desktop save file failed")?;
        output
            .write_all(&file.bytes)
            .and_then(|_| output.sync_all())
            .map_err(|_| "desktop save file failed")?;
        #[cfg(target_os = "windows")]
        if path.exists() {
            fs::remove_file(path).map_err(|_| "desktop save file failed")?;
        }
        fs::rename(&temporary, path).map_err(|_| "desktop save file failed")
    })();
    if result.is_err() {
        let _ = fs::remove_file(temporary);
    }
    result
}

pub(crate) async fn pick_file(app: AppHandle) -> Result<Option<PickedFileOutput>, &'static str> {
    let selected = app
        .dialog()
        .file()
        .add_filter("支持的文件", &["pdf", "jpg", "jpeg", "png", "txt"])
        .blocking_pick_file();
    let Some(selected) = selected else {
        return Ok(None);
    };
    let path = selected
        .into_path()
        .map_err(|_| "desktop selected file invalid")?;
    tauri::async_runtime::spawn_blocking(move || read_selected_file(path))
        .await
        .map_err(|_| "desktop selected file failed")?
        .map(Some)
}

pub(crate) async fn save_file(
    app: AppHandle,
    input: SaveFileInput,
) -> Result<SaveFileOutput, &'static str> {
    let file = decode_save_file(input)?;
    let extension = Path::new(&file.name)
        .extension()
        .and_then(|value| value.to_str())
        .ok_or("desktop host file invalid")?
        .to_owned();
    let selected = app
        .dialog()
        .file()
        .set_file_name(&file.name)
        .add_filter("当前文件", &[&extension])
        .blocking_save_file();
    let Some(selected) = selected else {
        return Ok(SaveFileOutput {
            status: "cancelled",
        });
    };
    let path = selected
        .into_path()
        .map_err(|_| "desktop save path invalid")?;
    tauri::async_runtime::spawn_blocking(move || replace_download(&path, file))
        .await
        .map_err(|_| "desktop save file failed")??;
    Ok(SaveFileOutput { status: "saved" })
}

pub(crate) fn notify(app: &AppHandle, message: String) -> Result<(), &'static str> {
    let length = message.chars().count();
    if !(1..=240).contains(&length) || message.chars().any(char::is_control) {
        return Err("desktop notification invalid");
    }
    app.notification()
        .builder()
        .title("Go Admin Plus")
        .body(message)
        .show()
        .map_err(|_| "desktop notification failed")
}

pub(crate) async fn write_clipboard(app: AppHandle, text: String) -> Result<(), &'static str> {
    if text.is_empty() || text.chars().count() > 4096 || text.contains('\0') {
        return Err("desktop clipboard value invalid");
    }
    #[cfg(target_os = "macos")]
    {
        let clipboard = app.clone();
        let (sender, receiver) = tokio::sync::oneshot::channel();
        app.run_on_main_thread(move || {
            let result = clipboard
                .clipboard()
                .write_text(text)
                .map_err(|_| "desktop clipboard write failed");
            let _ = sender.send(result);
        })
        .map_err(|_| "desktop clipboard write failed")?;
        receiver
            .await
            .map_err(|_| "desktop clipboard write failed")?
    }
    #[cfg(not(target_os = "macos"))]
    {
        app.clipboard()
            .write_text(text)
            .map_err(|_| "desktop clipboard write failed")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn selected_files_require_content_matching_the_product_extension() {
        assert_eq!(
            classify_file("report.pdf", b"%PDF-1.7\ncontent").unwrap(),
            "application/pdf"
        );
        assert_eq!(
            classify_file("photo.jpeg", &[0xff, 0xd8, 0xff, 0xe0]).unwrap(),
            "image/jpeg"
        );
        assert_eq!(
            classify_file("image.png", b"\x89PNG\r\n\x1a\ncontent").unwrap(),
            "image/png"
        );
        assert_eq!(
            classify_file("notes.txt", b"safe text\n").unwrap(),
            "text/plain"
        );
        assert!(classify_file("spoofed.pdf", b"plain text").is_err());
        assert!(classify_file("control.txt", b"safe\0hidden").is_err());
    }

    #[test]
    fn save_payloads_are_canonical_and_match_their_declared_type() {
        let decoded = decode_save_file(SaveFileInput {
            name: "notes.txt".into(),
            media_type: "text/plain".into(),
            data: "c2FmZSB0ZXh0Cg==".into(),
        })
        .unwrap();
        assert_eq!(decoded.name, "notes.txt");
        assert_eq!(decoded.bytes.as_slice(), b"safe text\n");

        for invalid in [
            SaveFileInput {
                name: "../notes.txt".into(),
                media_type: "text/plain".into(),
                data: "c2FmZSB0ZXh0Cg==".into(),
            },
            SaveFileInput {
                name: "notes.txt".into(),
                media_type: "application/pdf".into(),
                data: "c2FmZSB0ZXh0Cg==".into(),
            },
            SaveFileInput {
                name: "notes.txt".into(),
                media_type: "text/plain".into(),
                data: "not-base64".into(),
            },
        ] {
            assert!(decode_save_file(invalid).is_err());
        }
    }

    #[test]
    fn downloads_replace_regular_files_and_reject_invalid_targets() {
        let root = temporary_download_path(&std::env::temp_dir()).unwrap();
        fs::create_dir(&root).unwrap();
        let target = root.join("notes.txt");
        fs::write(&target, b"old text\n").unwrap();
        let file = decode_save_file(SaveFileInput {
            name: "notes.txt".into(),
            media_type: "text/plain".into(),
            data: "bmV3IHRleHQK".into(),
        })
        .unwrap();
        replace_download(&target, file).unwrap();
        assert_eq!(fs::read(&target).unwrap(), b"new text\n");

        let mismatched = decode_save_file(SaveFileInput {
            name: "notes.txt".into(),
            media_type: "text/plain".into(),
            data: "bmV3IHRleHQK".into(),
        })
        .unwrap();
        assert!(replace_download(&root.join("notes.pdf"), mismatched).is_err());

        #[cfg(unix)]
        {
            let linked = root.join("linked.txt");
            std::os::unix::fs::symlink(&target, &linked).unwrap();
            let file = decode_save_file(SaveFileInput {
                name: "notes.txt".into(),
                media_type: "text/plain".into(),
                data: "bmV3IHRleHQK".into(),
            })
            .unwrap();
            assert!(replace_download(&linked, file).is_err());
        }

        fs::remove_dir_all(root).unwrap();
    }
}
