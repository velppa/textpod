use axum::{
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
use tower_http::services::ServeDir;
use tracing::{error, info};
use tracing_subscriber;

const INDEX_HTML: &str = include_str!("index.html");
const FAVICON_SVG: &[u8] = include_bytes!("favicon.svg");

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

    if let Err(e) = fs::create_dir_all("attachments") {
        error!(
            "could not create attachments directory in {}: {e}",
            env::current_dir().unwrap().display()
        );
        process::exit(1);
    }

    let favicon = Base64Display::new(FAVICON_SVG, &STANDARD);
    let html = INDEX_HTML.replace(
        "{{FAVICON}}",
        format!("data:image/svg+xml;base64,{favicon}").as_str(),
    );

    let notes = Arc::new(Mutex::new(load_notes(&args.notes_file)));

    let state = AppState {
        html,
        notes,
        notes_file: args.notes_file,
        token: args.token,
    };

    // Watch notes file for external changes
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
                            let new_notes = load_notes(&watch_file);
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

    let app = Router::new()
        .route("/", get(index))
        .route("/notes", get(get_notes).post(save_note))
        .route("/notes/:id", get(get_note_by_id).put(update_note_by_id).delete(delete_note_by_id))
        .route("/note/:id", get(note_page))
        .route("/upload", post(upload_file))
        .fallback(get(fallback_md))
        .layer(DefaultBodyLimit::max(CONTENT_LENGTH_LIMIT))
        .nest_service("/attachments", ServeDir::new("attachments"))
        .with_state(state);

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

fn load_notes(file: &PathBuf) -> Vec<Note> {
    if let Ok(content) = fs::read_to_string(file) {
        content
            .split("\n\n---\n\n")
            .filter(|s| !s.trim().is_empty())
            .map(|block| {
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
                let content = content.trim_end_matches("\n---").trim().to_string();

                let html = md_to_html(&content);
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
    // If ?token=X is provided and matches, set cookie and redirect to /
    if let Some(provided) = params.get("token") {
        if state.token.as_deref() == Some(provided.as_str()) {
            return (
                StatusCode::SEE_OTHER,
                [
                    (header::SET_COOKIE, format!("textpod_token={}; Path=/; HttpOnly; SameSite=Strict", provided)),
                    (header::LOCATION, "/".to_string()),
                ],
            )
                .into_response();
        }
    }

    let readonly = match &state.token {
        None => false,
        Some(token) => !has_valid_token(&headers, token),
    };

    let html = state.html.replace("{{READONLY}}", if readonly { "true" } else { "false" });
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
async fn fallback_md(uri: axum::http::Uri) -> Response {
    let path = uri.path();
    if path.ends_with(".md") {
        let filename = path.rsplit('/').next().unwrap_or(path);
        let location = format!("/?q={}", urlencoding::encode(filename));
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
            let page = format!(
                r#"<!DOCTYPE html>
<html>
<head>
    <title>Note {id} - Textpod</title>
    <meta name="color-scheme" content="light dark" />
    <style>
        body {{
            font-family: system-ui, -apple-system, sans-serif;
            font-size: 150%;
            max-width: 800px;
            margin: 0 auto;
            padding: 20px;
        }}
        .note code {{ padding: 0.25em; background-color: #eee; }}
        .note pre {{ padding: 0.5em; background-color: #eee; }}
        .note pre code {{ padding: 0; background-color: transparent; }}
        .note img, .note video {{ max-width: 100%; }}
        .metadata {{ font-family: monospace; color: #888; margin-top: 2em; }}
        .metadata a {{ color: #888; text-decoration: none; }}
        .metadata a:hover {{ text-decoration: underline; }}
    </style>
</head>
<body>
    <div class="note">{html}</div>
    <div class="metadata">
        <time datetime="{timestamp}">{timestamp}</time>
        &middot; <a href="/">back</a>
    </div>
</body>
</html>"#,
                id = note.id,
                html = note.html,
                timestamp = note.timestamp,
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
    let mut notes = state.notes.lock().unwrap();
    match notes.iter_mut().find(|n| n.id == id) {
        None => Err((StatusCode::NOT_FOUND, format!("note with id {id} not found"))),
        Some(note) => {
            let (processed, links_to_download) = process_plus_links(&content);
            note.content = processed;
            note.html = md_to_html(&note.content);
            let note_id = note.id.clone();
            let file_content = notes
                .iter()
                .map(|n| format!("{}\n{}\n\n---\n\n", n.timestamp, n.content))
                .collect::<String>();
            drop(notes);
            if let Err(e) = fs::write(&state.notes_file, &file_content) {
                return Err((StatusCode::INTERNAL_SERVER_ERROR, e.to_string()));
            }
            if !links_to_download.is_empty() {
                spawn_downloads(
                    links_to_download,
                    note_id,
                    state.notes.clone(),
                    state.notes_file.clone(),
                );
            }
            info!("Note updated: {}", id);
            Ok(StatusCode::OK)
        }
    }
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
                .map(|note| format!("{}\n{}\n\n---\n\n", note.timestamp, note.content))
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
    let mut content = content.clone();

    // Replace "---" with "<hr>" in the content
    content = content.replace("---", "<hr>");

    let (content, links_to_download) = process_plus_links(&content);

    let timestamp = Local::now().format("%Y-%m-%d %H:%M:%S").to_string();
    let id = timestamp_to_id(&timestamp);
    let html = md_to_html(&content);
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

    write!(file, "{}\n{}\n\n---\n\n", timestamp, content)
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;

    info!("Note created: {}", timestamp);

    if !links_to_download.is_empty() {
        spawn_downloads(
            links_to_download,
            id.clone(),
            state.notes.clone(),
            state.notes_file.clone(),
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

        let original_path = PathBuf::from("attachments").join(&name);
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
            "/attachments/{}",
            path.file_name().unwrap().to_str().unwrap()
        )));
    }

    error!("Error uploading file");
    Err(StatusCode::BAD_REQUEST)
}

// UTILS

/// Process +link patterns in content: replace with display text and return URLs to download.
/// Returns (processed_content, links_to_download) where links_to_download is Vec<(url, filepath)>.
fn process_plus_links(content: &str) -> (String, Vec<(String, String)>) {
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

    fs::create_dir_all("attachments/webpages").unwrap();

    let mut result = content.to_string();
    let mut to_download = vec![];

    for (full_match, url, label) in &links {
        let escaped_filename = url_to_safe_filename(url);
        let filepath = format!("attachments/webpages/{}.html", escaped_filename);
        let display = match label {
            Some(l) => format!("[{}]({}) ([local copy](/{}))", l, url, filepath),
            None => format!("{} ([local copy](/{}))", url, filepath),
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
) {
    spawn(async move {
        for (url, filepath) in links {
            let result = Command::new("monolith")
                .args(&[url.as_str(), "-o", &filepath])
                .output()
                .await;

            info!("Downloading webpage: {}", url);

            if result.is_err() {
                error!("Failed to download webpage: {}", url);
                let mut notes_lock = notes.lock().unwrap();
                if let Some(note) = notes_lock.iter_mut().find(|n| n.id == note_id) {
                    note.content = note.content.replace(
                        &format!("([local copy](/{}))", filepath),
                        "(local copy failed)",
                    );
                    note.html = md_to_html(&note.content);
                }
                let file_content = notes_lock
                    .iter()
                    .map(|n| format!("{}\n{}\n\n---\n\n", n.timestamp, n.content))
                    .collect::<String>();
                drop(notes_lock);
                if let Err(e) = fs::write(&notes_file, file_content) {
                    error!("Failed to write notes file: {}", e);
                }
            }
        }
    });
}

/// Convert "2024-05-19 07:34:56" to "20240519073456"
fn timestamp_to_id(ts: &str) -> String {
    ts.chars().filter(|c| c.is_ascii_digit()).collect()
}

fn md_to_html(markdown: &str) -> String {
    let mut options = Options::default();
    options.extension.strikethrough = true;
    options.extension.tagfilter = true;
    options.extension.table = true;
    options.extension.autolink = true;
    options.extension.tasklist = true;
    options.extension.superscript = true;
    options.render.unsafe_ = true;
    options.render.hardbreaks = false;
    markdown_to_html(markdown, &options)
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
