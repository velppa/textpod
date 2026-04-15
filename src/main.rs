use axum::{
    body::Bytes,
    extract::{DefaultBodyLimit, Multipart, Path, Query, State},
    http::{header, HeaderMap, StatusCode},
    response::{Html, IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};
use base64::{display::Base64Display, engine::general_purpose::STANDARD};
use chrono::Local;
use clap::Parser;
use comrak::{markdown_to_html, Options};
use serde::{Deserialize, Serialize};
use std::{
    env,
    fs::{self},
    io::Write,
    net::SocketAddr,
    path::PathBuf,
    process,
    sync::{Arc, Mutex},
};
use notify::{Event, EventKind, RecommendedWatcher, RecursiveMode, Watcher};
use regex::Regex;
use tokio::process::Command;
use tokio::spawn;
use tracing::{error, info};
use tracing_subscriber;

const INDEX_HTML: &str = include_str!("index.html");
const FAVICON_SVG: &[u8] = include_bytes!("favicon.svg");
const SHARED_CSS: &str = include_str!("shared.css");
const NOTE_SEPARATOR: &str = "\u{000C}";

#[derive(Parser)]
#[command(author, version, about, long_about = None)]
struct Args {
    /// Change to DIR before doing anything
    #[arg(short = 'C', long, value_name = "DIR")]
    base_directory: Option<PathBuf>,
    /// Port number for the server
    #[arg(short, long, default_value_t = 3000)]
    port: u16,
    /// Listen address for the server
    #[arg(short, long, default_value = "127.0.0.1")]
    listen: String,
    /// Save notes in FILE
    #[arg(short = 'f', long, value_name = "FILE", default_value = "notes.md")]
    notes_file: PathBuf,
    /// Require token for write access; without it the UI is read-only
    #[arg(short, long)]
    token: Option<String>,
    /// Base URL path prefix (e.g., /textpod)
    #[arg(long, default_value = "")]
    base_path: String,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
struct Note {
    id: String,
    timestamp: String,
    content: String,
    html: String,
}

#[derive(Clone)]
struct AppState {
    html: String,
    notes: Arc<Mutex<Vec<Note>>>,
    notes_file: PathBuf,
    token: Option<String>,
    base_path: String,
}

const CONTENT_LENGTH_LIMIT: usize = 500 * 1024 * 1024; // allow uploading up to 500mb files... overkill?

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt::init();

    let args = Args::parse();

    if let Some(path) = args.base_directory {
        if let Err(e) = env::set_current_dir(&path) {
            error!("could not change directory to {}: {e}", path.display());
            process::exit(1);
        }
    }

    if let Err(e) = fs::create_dir_all("assets") {
        error!(
            "could not create assets directory in {}: {e}",
            env::current_dir().unwrap().display()
        );
        process::exit(1);
    }

    // Normalize base_path: ensure leading slash, no trailing slash, or empty
    let base_path = {
        let bp = args.base_path.trim_matches('/');
        if bp.is_empty() {
            String::new()
        } else {
            format!("/{}", bp)
        }
    };

    let favicon = Base64Display::new(FAVICON_SVG, &STANDARD);
    let html = INDEX_HTML
        .replace(
            "{{FAVICON}}",
            format!("data:image/svg+xml;base64,{favicon}").as_str(),
        )
        .replace("{{BASE_PATH}}", &base_path);

    let notes = Arc::new(Mutex::new(load_notes(&args.notes_file, &base_path)));

    let state = AppState {
        html,
        notes,
        notes_file: args.notes_file,
        token: args.token,
        base_path: base_path.clone(),
    };

    // Watch notes file for external changes
    let watch_base_path = base_path.clone();
    let watch_notes = state.notes.clone();
    let watch_file = state.notes_file.clone();
    let watch_file_canon = fs::canonicalize(&watch_file).unwrap_or_else(|_| watch_file.clone());
    let _watcher = {
        let (tx, rx) = std::sync::mpsc::channel::<notify::Result<Event>>();
        let mut watcher = RecommendedWatcher::new(tx, notify::Config::default())
            .expect("Failed to create file watcher");
        let watch_dir = watch_file_canon.parent().unwrap_or(std::path::Path::new("."));
        watcher
            .watch(watch_dir, RecursiveMode::NonRecursive)
            .expect("Failed to watch notes file");
        spawn(async move {
            let rx = rx;
            loop {
                match tokio::task::block_in_place(|| rx.recv()) {
                    Ok(Ok(event)) => {
                        let dominated = matches!(
                            event.kind,
                            EventKind::Modify(_) | EventKind::Create(_)
                        );
                        let affects_file = event
                            .paths
                            .iter()
                            .any(|p| {
                                fs::canonicalize(p).unwrap_or_else(|_| p.clone()) == watch_file_canon
                            });
                        if dominated && affects_file {
                            let new_notes = load_notes(&watch_file, &watch_base_path);
                            let mut notes = watch_notes.lock().unwrap();
                            *notes = new_notes;
                            info!("Reloaded notes from disk ({} notes)", notes.len());
                        }
                    }
                    Ok(Err(e)) => error!("File watch error: {}", e),
                    Err(_) => break,
                }
            }
        });
        watcher // keep alive
    };

    let inner = Router::new()
        .route("/", get(index))
        .route("/notes", get(get_notes).post(save_note))
        .route("/notes/:id", get(get_note_by_id).put(update_note_by_id).delete(delete_note_by_id))
        .route("/note/:id", get(note_page))
        .route("/upload", post(upload_file))
        .route("/assets/:name", get(get_asset).put(put_asset).head(head_asset))
        .fallback(get(fallback_md))
        .layer(DefaultBodyLimit::max(CONTENT_LENGTH_LIMIT))
        .with_state(state);

    let app = if base_path.is_empty() {
        inner
    } else {
        Router::new().nest(&base_path, inner)
    };

    let server_details = format!("{}:{}", args.listen, args.port);
    let addr: SocketAddr = server_details
        .parse()
        .expect("Unable to parse socket address");
    info!("Starting server on http://{}", addr);

    if let Ok(listener) = tokio::net::TcpListener::bind(&addr).await {
        if let Err(e) = axum::serve(listener, app)
            .with_graceful_shutdown(shutdown())
            .await
        {
            error!("Server error: {}", e);
        }
    }
}

async fn shutdown() {
    let ctrl_c = async {
        tokio::signal::ctrl_c()
            .await
            .expect("Failed to install ctrl + c handler");
    };

    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("It's supposed to run in a unix system.")
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {},
        _ = terminate => {},
    }
}

fn load_notes(file: &PathBuf, base_path: &str) -> Vec<Note> {
    if let Ok(content) = fs::read_to_string(file) {
        content
            .split('\u{000C}')
            .filter(|s| !s.trim().is_empty())
            .map(|block| {
                let block = block.trim();
                let parts: Vec<&str> = block.splitn(2, '\n').collect();
                let (timestamp, content) = match parts.as_slice() {
                    [timestamp, content] => {
                        (timestamp.trim().to_string(), content.trim().to_string())
                    }
                    _ => (
                        Local::now().format("%Y-%m-%d %H:%M:%S").to_string(),
                        block.to_string(),
                    ),
                };
                let content = content.trim().to_string();

                let html = md_to_html(&content, base_path);
                let id = timestamp_to_id(&timestamp);
                Note {
                    id,
                    timestamp,
                    content: content.to_string(),
                    html,
                }
            })
            .collect()
    } else {
        Vec::new()
    }
}

// route / (root)
async fn index(
    State(state): State<AppState>,
    headers: HeaderMap,
    Query(params): Query<std::collections::HashMap<String, String>>,
) -> Response {
    // If ?token=X is provided and matches, set cookie and redirect to base
    if let Some(provided) = params.get("token") {
        if state.token.as_deref() == Some(provided.as_str()) {
            let cookie_path = if state.base_path.is_empty() { "/".to_string() } else { state.base_path.clone() };
            let redirect_to = if state.base_path.is_empty() { "/".to_string() } else { state.base_path.clone() };
            return (
                StatusCode::SEE_OTHER,
                [
                    (header::SET_COOKIE, format!("textpod_token={}; Path={}; HttpOnly; SameSite=Strict", provided, cookie_path)),
                    (header::LOCATION, redirect_to),
                ],
            )
                .into_response();
        }
    }

    let readonly = match &state.token {
        None => false,
        Some(token) => !has_valid_token(&headers, token),
    };

    let html = state.html
        .replace("{{SHARED_CSS}}", SHARED_CSS)
        .replace("{{READONLY}}", if readonly { "true" } else { "false" });
    Html(html).into_response()
}

fn get_cookie_value<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    headers
        .get_all(header::COOKIE)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|s| s.split(';'))
        .map(|s| s.trim())
        .find_map(|cookie| {
            let mut parts = cookie.splitn(2, '=');
            let key = parts.next()?;
            let val = parts.next()?;
            if key == name { Some(val) } else { None }
        })
}

fn has_valid_token(headers: &HeaderMap, token: &str) -> bool {
    // Check cookie
    if let Some(cookie_token) = get_cookie_value(headers, "textpod_token") {
        if cookie_token == token {
            return true;
        }
    }
    // Check Authorization: Bearer header
    if let Some(auth) = headers.get(header::AUTHORIZATION) {
        if let Ok(auth_str) = auth.to_str() {
            if let Some(bearer_token) = auth_str.strip_prefix("Bearer ") {
                if bearer_token == token {
                    return true;
                }
            }
        }
    }
    false
}

fn require_auth(headers: &HeaderMap, token: &Option<String>) -> Result<(), StatusCode> {
    match token {
        None => Ok(()),
        Some(t) => {
            if has_valid_token(headers, t) {
                Ok(())
            } else {
                Err(StatusCode::UNAUTHORIZED)
            }
        }
    }
}

// Fallback: redirect *.md requests to /?q=filename.md
async fn fallback_md(State(state): State<AppState>, uri: axum::http::Uri) -> Response {
    let path = uri.path();
    if path.ends_with(".md") {
        let filename = path.rsplit('/').next().unwrap_or(path);
        let location = format!("{}?q={}", state.base_path, urlencoding::encode(filename));
        (StatusCode::SEE_OTHER, [(header::LOCATION, location)]).into_response()
    } else {
        StatusCode::NOT_FOUND.into_response()
    }
}

// GET /notes
async fn get_notes(State(state): State<AppState>) -> Json<Vec<Note>> {
    let notes = state.notes.lock().unwrap();
    Json(notes.iter().cloned().collect::<Vec<_>>())
}

// GET /note/:id (HTML page)
async fn note_page(
    State(state): State<AppState>,
    Path(id): Path<String>,
) -> Result<Html<String>, (StatusCode, String)> {
    let notes = state.notes.lock().unwrap();
    match notes.iter().find(|n| n.id == id) {
        Some(note) => {
            let title = note_title(note);
            let page = format!(
                r#"<!DOCTYPE html>
<html>
<head>
    <title>{title} - Textpod</title>
    <meta name="color-scheme" content="light dark" />
    <style>
        {shared_css}
    </style>
</head>
<body>
    <nav id="toc"></nav>
    <div class="note">{html}</div>
    <div class="metadata">
        <time datetime="{timestamp}">{display_timestamp}</time>
        &middot; <a href="{base_path}">back</a>
    </div>
    <script>
        (function() {{
            const headings = document.querySelectorAll('.note h1, .note h2, .note h3, .note h4, .note h5, .note h6');
            if (headings.length < 2) return;
            const toc = document.getElementById('toc');
            const ul = document.createElement('ul');
            const minLevel = Math.min(...[...headings].map(h => parseInt(h.tagName[1])));
            headings.forEach((h, i) => {{
                const id = 'heading-' + i;
                h.id = id;
                const li = document.createElement('li');
                const level = parseInt(h.tagName[1]) - minLevel;
                li.style.marginLeft = (level * 1.2) + 'em';
                const a = document.createElement('a');
                const text = h.querySelector('span') ? h.querySelector('span').textContent : h.textContent;
                a.textContent = text.trim();
                a.href = '#' + id;
                li.appendChild(a);
                ul.appendChild(li);
            }});
            toc.appendChild(ul);
        }})();
    </script>
</body>
</html>"#,
                title = title,
                shared_css = SHARED_CSS,
                html = note.html,
                timestamp = note.timestamp,
                display_timestamp = format_timestamp_with_day(&note.timestamp),
                base_path = state.base_path,
            );
            Ok(Html(page))
        }
        None => Err((
            StatusCode::NOT_FOUND,
            format!("note with id {id} not found"),
        )),
    }
}

// GET /notes/:id (JSON API)
async fn get_note_by_id(
    State(state): State<AppState>,
    Path(id): Path<String>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    let notes = state.notes.lock().unwrap();
    match notes.iter().find(|n| n.id == id) {
        Some(note) => Ok(Json(note.clone())),
        None => Err((
            StatusCode::NOT_FOUND,
            format!("note with id {id} not found"),
        )),
    }
}

// PUT /notes/:id
async fn update_note_by_id(
    State(state): State<AppState>,
    headers: HeaderMap,
    Path(id): Path<String>,
    Json(content): Json<String>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    require_auth(&headers, &state.token).map_err(|s| (s, "unauthorized".to_string()))?;
    let (processed, links_to_download) = process_plus_links(&content, &state.base_path);
    let mut notes = state.notes.lock().unwrap();
    let created = if let Some(note) = notes.iter_mut().find(|n| n.id == id) {
        note.content = processed;
        note.html = md_to_html(&note.content, &state.base_path);
        false
    } else {
        let timestamp = id_to_timestamp(&id).unwrap_or_else(|| {
            Local::now().format("%Y-%m-%d %H:%M:%S").to_string()
        });
        let html = md_to_html(&processed, &state.base_path);
        notes.push(Note {
            id: id.clone(),
            timestamp,
            content: processed,
            html,
        });
        true
    };
    let file_content = notes
        .iter()
        .map(|n| format!("{}\n{}\n\n{}\n\n", n.timestamp, n.content, NOTE_SEPARATOR))
        .collect::<String>();
    drop(notes);
    if let Err(e) = fs::write(&state.notes_file, &file_content) {
        return Err((StatusCode::INTERNAL_SERVER_ERROR, e.to_string()));
    }
    if !links_to_download.is_empty() {
        spawn_downloads(
            links_to_download,
            id.clone(),
            state.notes.clone(),
            state.notes_file.clone(),
            state.base_path.clone(),
        );
    }
    info!("Note {}: {}", if created { "created" } else { "updated" }, id);
    Ok(if created { StatusCode::CREATED } else { StatusCode::OK })
}

// DELETE /notes/:id
async fn delete_note_by_id(
    State(state): State<AppState>,
    headers: HeaderMap,
    Path(id): Path<String>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    require_auth(&headers, &state.token).map_err(|s| (s, "unauthorized".to_string()))?;
    let mut notes = state.notes.lock().unwrap();
    let pos = notes.iter().position(|n| n.id == id);
    match pos {
        None => Err((StatusCode::NOT_FOUND, format!("note with id {id} not found"))),
        Some(i) => {
            notes.remove(i);
            let content = notes
                .iter()
                .map(|note| format!("{}\n{}\n\n{}\n\n", note.timestamp, note.content, NOTE_SEPARATOR))
                .collect::<String>();
            if let Err(e) = fs::write(&state.notes_file, content) {
                return Err((StatusCode::INTERNAL_SERVER_ERROR, e.to_string()));
            }
            info!("Note deleted by id: {}", id);
            Ok(StatusCode::NO_CONTENT)
        }
    }
}

// POST /notes
async fn save_note(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(content): Json<String>,
) -> Result<(StatusCode, Json<String>), StatusCode> {
    require_auth(&headers, &state.token)?;
    let content = content.clone();

    let (content, links_to_download) = process_plus_links(&content, &state.base_path);

    let timestamp = Local::now().format("%Y-%m-%d %H:%M:%S").to_string();
    let id = timestamp_to_id(&timestamp);
    let html = md_to_html(&content, &state.base_path);
    let note = Note {
        id: id.clone(),
        timestamp: timestamp.clone(),
        content: content.clone(),
        html,
    };

    let mut notes = state.notes.lock().unwrap();
    notes.push(note);
    drop(notes);

    let mut file = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&state.notes_file)
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;

    write!(file, "{}\n{}\n\n{}\n\n", timestamp, content, NOTE_SEPARATOR)
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;

    info!("Note created: {}", timestamp);

    if !links_to_download.is_empty() {
        spawn_downloads(
            links_to_download,
            id.clone(),
            state.notes.clone(),
            state.notes_file.clone(),
            state.base_path.clone(),
        );
    }

    Ok((StatusCode::CREATED, Json(id)))
}

// route POST /upload
async fn upload_file(
    State(state): State<AppState>,
    headers: HeaderMap,
    mut multipart: Multipart,
) -> Result<Json<String>, StatusCode> {
    require_auth(&headers, &state.token)?;
    while let Some(field) = multipart.next_field().await.unwrap() {
        let name = field.file_name().unwrap().to_string();
        let data = field.bytes().await.unwrap();

        info!("Uploading file: {}", name);

        let original_path = PathBuf::from("assets").join(&name);
        let mut counter = 1;

        let original_stem = original_path
            .file_stem()
            .and_then(|s| s.to_str())
            .unwrap_or("");
        let original_ext = original_path
            .extension()
            .and_then(|s| s.to_str())
            .unwrap_or("");

        // Generate unique filename if already exists
        let mut path = original_path.clone();
        while path.exists() {
            // e.g: file-1.txt
            let new_name = if original_ext.is_empty() {
                format!("{}-{}", original_stem, counter)
            } else {
                format!("{}-{}.{}", original_stem, counter, original_ext)
            };

            path = original_path.parent().unwrap().join(new_name);
            counter += 1;
        }

        fs::write(&path, data).map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;

        info!("File saved as {}", path.display());
        return Ok(Json(format!(
            "{}/assets/{}",
            state.base_path,
            path.file_name().unwrap().to_str().unwrap()
        )));
    }

    error!("Error uploading file");
    Err(StatusCode::BAD_REQUEST)
}

async fn put_asset(
    State(state): State<AppState>,
    headers: HeaderMap,
    Path(name): Path<String>,
    body: Bytes,
) -> Result<StatusCode, StatusCode> {
    require_auth(&headers, &state.token)?;
    let path = PathBuf::from("assets").join(&name);
    fs::write(&path, &body).map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;
    info!("Asset saved: {}", path.display());
    Ok(StatusCode::CREATED)
}

async fn get_asset(Path(name): Path<String>) -> Response {
    let path = PathBuf::from("assets").join(&name);
    match fs::read(&path) {
        Ok(data) => {
            let content_type = match path.extension().and_then(|e| e.to_str()) {
                Some("html") => "text/html",
                Some("png") => "image/png",
                Some("jpg" | "jpeg") => "image/jpeg",
                Some("gif") => "image/gif",
                Some("webp") => "image/webp",
                Some("svg") => "image/svg+xml",
                Some("pdf") => "application/pdf",
                Some("css") => "text/css",
                Some("js") => "application/javascript",
                Some("json") => "application/json",
                Some("txt" | "md") => "text/plain",
                _ => "application/octet-stream",
            };
            ([(header::CONTENT_TYPE, content_type)], data).into_response()
        }
        Err(_) => StatusCode::NOT_FOUND.into_response(),
    }
}

async fn head_asset(Path(name): Path<String>) -> StatusCode {
    if PathBuf::from("assets").join(&name).exists() {
        StatusCode::NO_CONTENT
    } else {
        StatusCode::NOT_FOUND
    }
}

// UTILS

/// Process +link patterns in content: replace with display text and return URLs to download.
/// Returns (processed_content, links_to_download) where links_to_download is Vec<(url, filepath)>.
fn process_plus_links(content: &str, base_path: &str) -> (String, Vec<(String, String)>) {
    let link_re = Regex::new(r"\+\[([^\]]+)\]\((https?://[^)]+)\)|\+<(https?://[^>]+)>|\+(https?://\S+)").unwrap();
    let links: Vec<(String, String, Option<String>)> = link_re
        .captures_iter(content)
        .map(|cap| {
            let full = cap.get(0).unwrap().as_str().to_string();
            if let (Some(label), Some(url)) = (cap.get(1), cap.get(2)) {
                (full, url.as_str().to_string(), Some(label.as_str().to_string()))
            } else if let Some(url) = cap.get(3) {
                (full, url.as_str().to_string(), None)
            } else {
                let url = cap.get(4).unwrap().as_str().to_string();
                (full, url, None)
            }
        })
        .collect();

    if links.is_empty() {
        return (content.to_string(), vec![]);
    }

    fs::create_dir_all("assets/webpages").unwrap();

    let mut result = content.to_string();
    let mut to_download = vec![];

    for (full_match, url, label) in &links {
        let escaped_filename = url_to_safe_filename(url);
        let filepath = format!("assets/webpages/{}.html", escaped_filename);
        let display = match label {
            Some(l) => format!("[{}]({}) ([local copy]({}/{}))", l, url, base_path, filepath),
            None => format!("{} ([local copy]({}/{}))", url, base_path, filepath),
        };
        result = result.replace(full_match, &display);
        to_download.push((url.clone(), filepath));
    }

    (result, to_download)
}

/// Spawn background monolith downloads and update notes on failure.
fn spawn_downloads(
    links: Vec<(String, String)>,
    note_id: String,
    notes: Arc<Mutex<Vec<Note>>>,
    notes_file: PathBuf,
    base_path: String,
) {
    spawn(async move {
        for (url, filepath) in links {
            info!("Downloading webpage: {}", url);
            let result = Command::new("monolith")
                .args(&[url.as_str(), "-o", &filepath])
                .output()
                .await;

            let failed = match &result {
                Err(e) => {
                    error!("Failed to run monolith: {}", e);
                    true
                }
                Ok(output) if !output.status.success() => {
                    error!(
                        "monolith exited with {}: {}",
                        output.status,
                        String::from_utf8_lossy(&output.stderr)
                    );
                    true
                }
                _ => false,
            };

            if failed {
                error!("Failed to download webpage: {}", url);
                let mut notes_lock = notes.lock().unwrap();
                if let Some(note) = notes_lock.iter_mut().find(|n| n.id == note_id) {
                    note.content = note.content.replace(
                        &format!("([local copy]({}/{}))", base_path, filepath),
                        "(local copy failed)",
                    );
                    note.html = md_to_html(&note.content, &base_path);
                }
                let file_content = notes_lock
                    .iter()
                    .map(|n| format!("{}\n{}\n\n{}\n\n", n.timestamp, n.content, NOTE_SEPARATOR))
                    .collect::<String>();
                drop(notes_lock);
                if let Err(e) = fs::write(&notes_file, file_content) {
                    error!("Failed to write notes file: {}", e);
                }
            }
        }
    });
}

/// Format "2026-04-14 21:34:18" as "2026-04-14 Mon 21:34:18"
fn format_timestamp_with_day(ts: &str) -> String {
    if let Ok(dt) = chrono::NaiveDateTime::parse_from_str(ts, "%Y-%m-%d %H:%M:%S") {
        dt.format("%Y-%m-%d %a %H:%M:%S").to_string()
    } else {
        ts.to_string()
    }
}

/// Extract a title from a note: first heading, or first line of content.
fn note_title(note: &Note) -> String {
    let re = Regex::new(r"(?m)^#{1,6}\s+(.+)").unwrap();
    if let Some(cap) = re.captures(&note.content) {
        cap[1].trim().to_string()
    } else {
        note.content.lines().next().unwrap_or("Note").trim().to_string()
    }
}

/// Convert "2024-05-19 07:34:56" to "20240519073456"
fn timestamp_to_id(ts: &str) -> String {
    ts.chars().filter(|c| c.is_ascii_digit()).collect()
}

/// Convert "20240519073456" to "2024-05-19 07:34:56"
fn id_to_timestamp(id: &str) -> Option<String> {
    if id.len() != 14 || !id.chars().all(|c| c.is_ascii_digit()) {
        return None;
    }
    Some(format!(
        "{}-{}-{} {}:{}:{}",
        &id[0..4], &id[4..6], &id[6..8], &id[8..10], &id[10..12], &id[12..14]
    ))
}

fn md_to_html(markdown: &str, base_path: &str) -> String {
    let mut options = Options::default();
    options.extension.strikethrough = true;
    options.extension.tagfilter = true;
    options.extension.table = true;
    options.extension.autolink = true;
    options.extension.tasklist = true;
    options.extension.superscript = true;
    options.render.unsafe_ = true;
    options.render.hardbreaks = false;
    let html = markdown_to_html(markdown, &options);
    let html = rewrite_md_links(&html, base_path);
    let html = rewrite_attachment_links(&html, base_path);
    process_tags(&html, base_path)
}

/// Rewrite relative .md links to absolute search URLs.
fn rewrite_md_links(html: &str, base_path: &str) -> String {
    let link_re = Regex::new(r#"href="([^"]*\.md)""#).unwrap();
    link_re.replace_all(html, |caps: &regex::Captures| {
        let href = &caps[1];
        // Extract just the filename, stripping any relative path components
        let filename = href.rsplit('/').next().unwrap_or(href);
        format!(r#"href="{}?q={}""#, base_path, urlencoding::encode(filename))
    }).to_string()
}

/// Rewrite absolute /assets/ paths to include the base path prefix.
fn rewrite_attachment_links(html: &str, base_path: &str) -> String {
    if base_path.is_empty() {
        return html.to_string();
    }
    html.replace("\"/assets/", &format!("\"{}/assets/", base_path))
        .replace("'/assets/", &format!("'{}/assets/", base_path))
}

/// Convert :Tag1:Tag2: patterns into clickable search links.
/// Inside headings, tags are wrapped in a right-aligned span.
fn process_tags(html: &str, base_path: &str) -> String {
    // Match :Word1:Word2: patterns (one or more tags between colons)
    let tag_re = Regex::new(r"(:[A-Za-z0-9_@#]+(?::[A-Za-z0-9_@#]+)*:)").unwrap();

    // Process headings first: move tags into a <span class="tags">
    let heading_re = Regex::new(r"(<h[1-6][^>]*>)(.*?)(</h[1-6]>)").unwrap();
    let result = heading_re.replace_all(html, |caps: &regex::Captures| {
        let open = &caps[1];
        let inner = &caps[2];
        let close = &caps[3];
        if let Some(m) = tag_re.find(inner) {
            let title = inner[..m.start()].trim_end();
            let tags_html = tags_to_links(m.as_str(), base_path);
            format!(r#"{}<span>{}</span><span class="tags">{}</span>{}"#, open, title, tags_html, close)
        } else {
            format!("{}<span>{}</span>{}", open, inner, close)
        }
    });

    // Process remaining (non-heading) tags in body text
    tag_re.replace_all(&result, |caps: &regex::Captures| {
        tags_to_links(&caps[1], base_path)
    }).to_string()
}

/// Convert a ":Tag1:Tag2:" string into individual <a class="tag"> links.
fn tags_to_links(tag_str: &str, base_path: &str) -> String {
    tag_str
        .trim_matches(':')
        .split(':')
        .map(|tag| {
            format!(
                r#"<a class="tag" href="{}?q={}">{}</a>"#,
                base_path,
                urlencoding::encode(tag),
                tag
            )
        })
        .collect::<Vec<_>>()
        .join(" ")
}

fn url_to_safe_filename(url: &str) -> String {
    let mut safe_name = String::with_capacity(url.len());

    let stripped_url = url
        .trim()
        .strip_prefix("http://")
        .unwrap_or(url)
        .strip_prefix("https://")
        .unwrap_or(url);

    for c in stripped_url.chars() {
        match c {
            '/' | '\\' | ':' | '*' | '?' | '"' | '<' | '>' | '|' => safe_name.push('_'),
            c if c.is_alphanumeric() || c == '-' || c == '.' || c == '_' => safe_name.push(c),
            _ => safe_name.push('_'),
        }
    }

    safe_name.trim_matches(|c| c == '.' || c == ' ').to_string()
}
