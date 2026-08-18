package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

//go:embed index.html
var indexHTML string

//go:embed favicon.svg
var faviconSVG []byte

//go:embed shared.css
var sharedCSS string

//go:embed blog.css
var blogCSS string

const noteSeparator = "\u000C"
const contentLengthLimit = 500 * 1024 * 1024
const timestampLayout = "2006-01-02 15:04:05"
const timestampDisplayLayout = "2006-01-02 Mon 15:04:05"

type Note struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
	HTML      string `json:"html"`
}

type Config struct {
	BaseDir   string
	Port      int
	Addr      string
	NotesFile string
	Token     string
	HasToken  bool
	BasePath  string
}

type Server struct {
	Config
	HTML  string
	Notes []Note
	mu    sync.Mutex
}

// knownIDs returns a predicate over the current note id set. Optional
// extra ids are added to the snapshot (handy when the caller is about
// to insert a note whose id should be self-resolvable).
// Must be called with s.mu held.
func (s *Server) knownIDs(extra ...string) func(string) bool {
	ids := make(map[string]struct{}, len(s.Notes)+len(extra))
	for i := range s.Notes {
		ids[s.Notes[i].ID] = struct{}{}
	}
	for _, e := range extra {
		ids[e] = struct{}{}
	}
	return func(s string) bool { _, ok := ids[s]; return ok }
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var cfg Config
	flag.StringVar(&cfg.BaseDir, "base-directory", "", "Change to DIR before doing anything")
	flag.IntVar(&cfg.Port, "port", 3000, "Port number for the server")
	flag.StringVar(&cfg.Addr, "listen", "127.0.0.1", "Addr address for the server")
	flag.StringVar(&cfg.NotesFile, "notes-file", "notes.md", "Save notes in FILE")
	flag.StringVar(&cfg.Token, "token", "", "Require token for write access; without it the UI is read-only")
	flag.StringVar(&cfg.BasePath, "base-path", "", "Base URL path prefix (e.g., /textpod)")
	flag.Parse()

	flag.Visit(func(f *flag.Flag) {
		if f.Name == "token" {
			cfg.HasToken = cfg.Token != ""
		}
	})

	if cfg.BaseDir != "" {
		if err := os.Chdir(cfg.BaseDir); err != nil {
			log.Fatalf("could not change directory to %s: %v", cfg.BaseDir, err)
		}
	}

	if err := os.MkdirAll("assets", 0o755); err != nil {
		cwd, _ := os.Getwd()
		log.Fatalf("could not create assets directory in %s: %v", cwd, err)
	}

	bp := strings.Trim(cfg.BasePath, "/")
	if bp != "" {
		cfg.BasePath = "/" + bp
	} else {
		cfg.BasePath = ""
	}

	favicon := base64.StdEncoding.EncodeToString(faviconSVG)
	htmlStr := strings.ReplaceAll(indexHTML, "{{FAVICON}}", "data:image/svg+xml;base64,"+favicon)
	htmlStr = strings.ReplaceAll(htmlStr, "{{BASE_PATH}}", cfg.BasePath)

	server := &Server{
		Config: cfg,
		HTML:   htmlStr,
		Notes:  loadNotes(cfg.NotesFile, cfg.BasePath),
	}

	// Watch notes file for external changes.
	server.startWatcher()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", server.index)
	mux.HandleFunc("GET /notes", server.getNotes)
	mux.HandleFunc("POST /notes", server.saveNote)
	mux.HandleFunc("GET /notes/{id}", server.getNoteByID)
	mux.HandleFunc("PUT /notes/{id}", server.updateNoteByID)
	mux.HandleFunc("DELETE /notes/{id}", server.deleteNoteByID)
	mux.HandleFunc("GET /note/{id}", server.notePage)
	mux.HandleFunc("POST /upload", server.uploadFile)
	mux.HandleFunc("GET /assets/{name...}", server.getAsset)
	mux.HandleFunc("PUT /assets/{name...}", server.putAsset)
	mux.HandleFunc("HEAD /assets/{name...}", server.headAsset)
	var handler http.Handler = mux
	if cfg.BasePath != "" {
		root := http.NewServeMux()
		root.Handle(cfg.BasePath+"/", http.StripPrefix(cfg.BasePath, mux))
		handler = root
	}
	handler = limitBody(handler, contentLengthLimit)

	addr := fmt.Sprintf("%s:%d", cfg.Addr, cfg.Port)
	log.Printf("Starting server on http://%s", addr)

	srv := &http.Server{Addr: addr, Handler: handler}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Server error: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func limitBody(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) startWatcher() {
	var lastMod time.Time
	var lastSize int64
	if fi, err := os.Stat(s.NotesFile); err == nil {
		lastMod = fi.ModTime()
		lastSize = fi.Size()
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			fi, err := os.Stat(s.NotesFile)
			if err != nil {
				continue
			}
			if fi.ModTime().Equal(lastMod) && fi.Size() == lastSize {
				continue
			}
			lastMod = fi.ModTime()
			lastSize = fi.Size()
			newNotes := loadNotes(s.NotesFile, s.BasePath)
			s.mu.Lock()
			s.Notes = newNotes
			n := len(s.Notes)
			s.mu.Unlock()
			log.Printf("Reloaded notes from disk (%d notes)", n)
		}
	}()
}

func loadNotes(file, basePath string) []Note {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	type raw struct{ timestamp, id, content string }
	var raws []raw
	ids := map[string]struct{}{}
	for block := range strings.SplitSeq(string(data), noteSeparator) {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var timestamp, id, content string
		if first, rest, ok := strings.Cut(block, "\n"); ok {
			timestamp, id = parseHeaderLine(first)
			content = strings.TrimSpace(rest)
		} else {
			timestamp = time.Now().Format(timestampLayout)
			content = block
		}
		if id == "" {
			id = timestampToID(timestamp)
		}
		raws = append(raws, raw{timestamp, id, content})
		ids[id] = struct{}{}
	}
	isKnown := func(s string) bool { _, ok := ids[s]; return ok }
	notes := make([]Note, 0, len(raws))
	for _, r := range raws {
		notes = append(notes, Note{
			ID:        r.id,
			Timestamp: r.timestamp,
			Content:   r.content,
			HTML:      noteToHTML(r.content, basePath, isKnown),
		})
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Timestamp < notes[j].Timestamp })
	return notes
}

// -- handlers --

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if provided := q.Get("token"); provided != "" && s.HasToken && provided == s.Token {
		cookiePath := "/"
		redirect := "/"
		if s.BasePath != "" {
			cookiePath = s.BasePath
			redirect = s.BasePath
		}
		w.Header().Set("Set-Cookie", fmt.Sprintf("textpod_token=%s; Path=%s; HttpOnly; SameSite=Strict", provided, cookiePath))
		w.Header().Set("Location", redirect)
		w.WriteHeader(http.StatusSeeOther)
		return
	}

	search := strings.TrimSpace(q.Get("q"))
	page := parsePage(q.Get("page"))

	s.mu.Lock()
	notes := make([]Note, 0, len(s.Notes))
	for i := len(s.Notes) - 1; i >= 0; i-- {
		notes = append(notes, s.Notes[i])
	}
	s.mu.Unlock()

	notes = filterNotes(notes, search)
	pageNotes, page, pages := paginateNotes(notes, page, pageSize)

	var b strings.Builder
	for _, n := range pageNotes {
		body := wrapH3InDetails(n.HTML)
		link := fmt.Sprintf(`<a href="%s/note/%s"><time datetime="%s">%s</time></a>`,
			s.BasePath, n.ID, n.Timestamp, formatTimestampWithDay(n.Timestamp))
		b.WriteString(`<section class="note">`)
		b.WriteString(injectSubtitle(body, link))
		b.WriteString("</section>\n")
	}

	out := strings.ReplaceAll(s.HTML, "{{BLOG_CSS}}", blogCSS)
	out = strings.ReplaceAll(out, "{{SHARED_CSS}}", sharedCSS)
	out = strings.ReplaceAll(out, "{{SEARCH_BOX}}", searchBoxHTML(s.BasePath, search))
	out = strings.ReplaceAll(out, "{{NOTES}}", b.String())
	out = strings.ReplaceAll(out, "{{PAGER}}", pagerHTML(s.BasePath, search, page, pages))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, out)
}

func getCookieValue(r *http.Request, name string) (string, bool) {
	c, err := r.Cookie(name)
	if err != nil {
		return "", false
	}
	return c.Value, true
}

func hasValidToken(r *http.Request, token string) bool {
	for _, c := range r.Cookies() {
		if c.Name == "textpod_token" && c.Value == token {
			return true
		}
	}
	auth := r.Header.Get("Authorization")
	if rest, ok := strings.CutPrefix(auth, "Bearer "); ok {
		if rest == token {
			return true
		}
	}
	return false
}

func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if !s.HasToken {
		return true
	}
	if hasValidToken(r, s.Token) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (s *Server) getNotes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := make([]Note, 0, len(s.Notes))
	for i := len(s.Notes) - 1; i >= 0; i-- {
		n := s.Notes[i]
		n.HTML = wrapH3InDetails(n.HTML)
		out = append(out, n)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) notePage(w http.ResponseWriter, r *http.Request) {
	if provided := r.URL.Query().Get("token"); provided != "" && s.HasToken && provided == s.Token {
		cookiePath := "/"
		if s.BasePath != "" {
			cookiePath = s.BasePath
		}
		w.Header().Set("Set-Cookie", fmt.Sprintf("textpod_token=%s; Path=%s; HttpOnly; SameSite=Strict", provided, cookiePath))
		w.Header().Set("Location", r.URL.Path)
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	var note *Note
	for i := range s.Notes {
		if s.Notes[i].ID == id {
			n := s.Notes[i]
			note = &n
			break
		}
	}
	s.mu.Unlock()
	if note == nil {
		http.Error(w, fmt.Sprintf("note with id %s not found", id), http.StatusNotFound)
		return
	}
	title := html.EscapeString(noteTitle(note))
	subtitleInner := fmt.Sprintf(`<time datetime="%s">%s</time> &middot; <a href="%s">back</a>`,
		note.Timestamp, formatTimestampWithDay(note.Timestamp), s.BasePath)
	noteBody := injectSubtitle(note.HTML, subtitleInner)
	page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <title>%s | Hotter</title>
    <meta name="color-scheme" content="light dark" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
    <link href="https://fonts.googleapis.com/css2?family=Inter:ital,opsz,wght@0,14..32,100..900;1,14..32,100..900&display=swap" rel="stylesheet">
    <style>
        %s
        %s
    </style>
</head>
<body>
    <section id="noteView" class="note">%s</section>
    <footer>Hotter</footer>
    <script>
        (function() {
            const headings = document.querySelectorAll('.note h1, .note h2, .note h3, .note h4, .note h5, .note h6');
            const subtitle = document.querySelector('.note .subtitle');
            if (headings.length >= 2 && subtitle) {
                const ul = document.createElement('ul');
                ul.style.margin = '0';
                ul.style.paddingLeft = '0';
                const minLevel = Math.min(...[...headings].map(h => parseInt(h.tagName[1])));
                headings.forEach((h, i) => {
                    const id = 'heading-' + i;
                    h.id = id;
                    const li = document.createElement('li');
                    const level = parseInt(h.tagName[1]) - minLevel;
                    li.style.marginLeft = (level * 0.6) + 'em';
                    li.style.listStyle = 'none';
                    const a = document.createElement('a');
                    const text = h.querySelector('span') ? h.querySelector('span').textContent : h.textContent;
                    a.textContent = text.trim();
                    a.href = '#' + id;
                    li.appendChild(a);
                    ul.appendChild(li);
                });
                const box = document.createElement('aside');
                box.className = 'note-toc';
                box.appendChild(ul);
                subtitle.parentNode.insertBefore(box, subtitle.nextSibling);
            }
        })();
    </script>
</body>
</html>`, title, blogCSS, sharedCSS, noteBody)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, page)
}

func (s *Server) getNoteByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Notes {
		if s.Notes[i].ID == id {
			writeJSON(w, http.StatusOK, s.Notes[i])
			return
		}
	}
	http.Error(w, fmt.Sprintf("note with id %s not found", id), http.StatusNotFound)
}

func (s *Server) updateNoteByID(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	id := r.PathValue("id")
	var content string
	if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	processed, toDownload := processPlusLinks(content, s.BasePath)

	s.mu.Lock()
	isKnown := s.knownIDs(id)
	created := true
	for i := range s.Notes {
		if s.Notes[i].ID == id {
			s.Notes[i].Content = processed
			s.Notes[i].HTML = noteToHTML(processed, s.BasePath, isKnown)
			created = false
			break
		}
	}
	if created {
		ts, ok := idToTimestamp(id)
		if !ok {
			ts = time.Now().Format(timestampLayout)
		}
		s.Notes = append(s.Notes, Note{
			ID:        id,
			Timestamp: ts,
			Content:   processed,
			HTML:      noteToHTML(processed, s.BasePath, isKnown),
		})
		sort.Slice(s.Notes, func(i, j int) bool { return s.Notes[i].Timestamp < s.Notes[j].Timestamp })
	}
	fileContent := notesToFile(s.Notes)
	s.mu.Unlock()

	if err := os.WriteFile(s.NotesFile, []byte(fileContent), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(toDownload) > 0 {
		go s.runDownloads(toDownload, id)
	}
	if created {
		log.Printf("Note created: %s", id)
		w.WriteHeader(http.StatusCreated)
	} else {
		log.Printf("Note updated: %s", id)
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) deleteNoteByID(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	idx := -1
	for i := range s.Notes {
		if s.Notes[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		s.mu.Unlock()
		http.Error(w, fmt.Sprintf("note with id %s not found", id), http.StatusNotFound)
		return
	}
	s.Notes = append(s.Notes[:idx], s.Notes[idx+1:]...)
	fileContent := notesToFile(s.Notes)
	s.mu.Unlock()

	if err := os.WriteFile(s.NotesFile, []byte(fileContent), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Note deleted by id: %s", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) saveNote(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	var content string
	if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	content, toDownload := processPlusLinks(content, s.BasePath)

	timestamp := time.Now().Format(timestampLayout)
	id := timestampToID(timestamp)

	s.mu.Lock()
	note := Note{
		ID:        id,
		Timestamp: timestamp,
		Content:   content,
		HTML:      noteToHTML(content, s.BasePath, s.knownIDs(id)),
	}
	s.Notes = append(s.Notes, note)
	sort.Slice(s.Notes, func(i, j int) bool { return s.Notes[i].Timestamp < s.Notes[j].Timestamp })
	s.mu.Unlock()

	f, err := os.OpenFile(s.NotesFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if _, err := fmt.Fprintf(f, "%s\n%s\n\n%s\n\n", formatHeaderLine(timestamp, id), content, noteSeparator); err != nil {
		f.Close()
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	f.Close()

	log.Printf("Note created: %s", timestamp)

	if len(toDownload) > 0 {
		go s.runDownloads(toDownload, id)
	}

	writeJSON(w, http.StatusCreated, id)
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if err := r.ParseMultipartForm(contentLengthLimit); err != nil {
		log.Printf("Error uploading file: %v", err)
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	var fh *multipart.FileHeader
	for _, fhs := range r.MultipartForm.File {
		if len(fhs) > 0 {
			fh = fhs[0]
			break
		}
	}
	if fh == nil {
		log.Printf("Error uploading file")
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	name := fh.Filename
	log.Printf("Uploading file: %s", name)

	f, err := fh.Open()
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	originalPath := filepath.Join("assets", name)
	stem := strings.TrimSuffix(filepath.Base(originalPath), filepath.Ext(originalPath))
	ext := strings.TrimPrefix(filepath.Ext(originalPath), ".")
	path := originalPath
	counter := 1
	for {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		var newName string
		if ext == "" {
			newName = fmt.Sprintf("%s-%d", stem, counter)
		} else {
			newName = fmt.Sprintf("%s-%d.%s", stem, counter, ext)
		}
		path = filepath.Join(filepath.Dir(originalPath), newName)
		counter++
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	log.Printf("File saved as %s", path)
	writeJSON(w, http.StatusOK, fmt.Sprintf("%s/assets/%s", s.BasePath, filepath.Base(path)))
}

func assetPath(name string) (string, bool) {
	path := filepath.Join("assets", name)
	if path != "assets" && !strings.HasPrefix(path, "assets"+string(filepath.Separator)) {
		return "", false
	}
	return path, true
}

func (s *Server) putAsset(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	name := r.PathValue("name")
	path, ok := assetPath(name)
	if !ok {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	log.Printf("Asset saved: %s", path)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) getAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := assetPath(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	// Sniffed fallback serves text files inline instead of forcing a download.
	ct := http.DetectContentType(data)
	switch ext {
	case "html":
		ct = "text/html"
	case "png":
		ct = "image/png"
	case "jpg", "jpeg":
		ct = "image/jpeg"
	case "gif":
		ct = "image/gif"
	case "webp":
		ct = "image/webp"
	case "svg":
		ct = "image/svg+xml"
	case "pdf":
		ct = "application/pdf"
	case "css":
		ct = "text/css"
	case "js":
		ct = "application/javascript"
	case "json":
		ct = "application/json"
	case "txt", "md":
		ct = "text/plain"
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(data)
}

func (s *Server) headAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path, ok := assetPath(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}

// -- utils --

func filterNotes(notes []Note, q string) []Note {
	if q == "" {
		return notes
	}
	ql := strings.ToLower(q)
	out := make([]Note, 0, len(notes))
	for _, n := range notes {
		if strings.Contains(strings.ToLower(n.Content), ql) || strings.Contains(n.Timestamp, q) {
			out = append(out, n)
		}
	}
	return out
}

const pageSize = 10

// paginateNotes returns the slice of notes for the given 1-based page,
// the clamped page number, and the total number of pages (>=1).
func paginateNotes(notes []Note, page, size int) ([]Note, int, int) {
	pages := (len(notes) + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	if start > len(notes) {
		start = len(notes)
	}
	end := start + size
	if end > len(notes) {
		end = len(notes)
	}
	return notes[start:end], page, pages
}

func parsePage(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func searchBoxHTML(basePath, q string) string {
	return fmt.Sprintf(`<form class="search" method="get" action="%s/">`+
		`<input type="search" name="q" value="%s" placeholder="Search…"></form>`,
		basePath, html.EscapeString(q))
}

func pagerHTML(basePath, q string, page, pages int) string {
	if pages <= 1 {
		return ""
	}
	link := func(p int) string {
		v := url.Values{}
		if q != "" {
			v.Set("q", q)
		}
		v.Set("page", strconv.Itoa(p))
		return basePath + "/?" + v.Encode()
	}
	var b strings.Builder
	b.WriteString(`<nav class="pager">`)
	if page > 1 {
		fmt.Fprintf(&b, `<a class="newer" href="%s">&larr; Newer posts</a>`, link(page-1))
	} else {
		b.WriteString(`<span class="newer"></span>`)
	}
	fmt.Fprintf(&b, `<span class="page-of">Page %d of %d</span>`, page, pages)
	if page < pages {
		fmt.Fprintf(&b, `<a class="older" href="%s">Older posts &rarr;</a>`, link(page+1))
	} else {
		b.WriteString(`<span class="older"></span>`)
	}
	b.WriteString(`</nav>`)
	return b.String()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notesToFile(notes []Note) string {
	var b strings.Builder
	for _, n := range notes {
		fmt.Fprintf(&b, "%s\n%s\n\n%s\n\n", formatHeaderLine(n.Timestamp, n.ID), n.Content, noteSeparator)
	}
	return b.String()
}

// parseHeaderLine splits a note header into (timestamp, id). The on-disk
// format is `timestamp[ id]` with any amount of whitespace between the
// fixed-width timestamp and the id. When the id is omitted (timestamp-only
// header), id is empty and the caller derives it from the timestamp.
func parseHeaderLine(line string) (string, string) {
	line = strings.TrimSpace(line)
	n := len(timestampLayout)
	if len(line) <= n {
		return line, ""
	}
	return strings.TrimSpace(line[:n]), strings.TrimSpace(line[n:])
}

func formatHeaderLine(timestamp, id string) string {
	if id == "" || id == timestampToID(timestamp) {
		return timestamp
	}
	return timestamp + " " + id
}

var plusLinkRe = regexp.MustCompile(`\+\[([^\]]+)\]\((https?://[^)]+)\)|\+<(https?://[^>]+)>|\+(https?://\S+)`)

// processPlusLinks returns (processed, toDownload) where toDownload is [(url, filepath), ...].
func processPlusLinks(content, basePath string) (string, [][2]string) {
	matches := plusLinkRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content, nil
	}
	_ = os.MkdirAll("assets/webpages", 0o755)

	type link struct {
		full, url, label string
		hasLabel         bool
	}
	var links []link
	for _, m := range matches {
		full := content[m[0]:m[1]]
		var l link
		l.full = full
		if m[2] != -1 && m[4] != -1 {
			l.label = content[m[2]:m[3]]
			l.url = content[m[4]:m[5]]
			l.hasLabel = true
		} else if m[6] != -1 {
			l.url = content[m[6]:m[7]]
		} else if m[8] != -1 {
			l.url = content[m[8]:m[9]]
		}
		links = append(links, l)
	}

	result := content
	var toDownload [][2]string
	for _, l := range links {
		escaped := urlToSafeFilename(l.url)
		filepathStr := fmt.Sprintf("assets/webpages/%s.html", escaped)
		var display string
		if l.hasLabel {
			display = fmt.Sprintf("[%s](%s) ([local copy](%s/%s))", l.label, l.url, basePath, filepathStr)
		} else {
			display = fmt.Sprintf("%s ([local copy](%s/%s))", l.url, basePath, filepathStr)
		}
		result = strings.Replace(result, l.full, display, 1)
		toDownload = append(toDownload, [2]string{l.url, filepathStr})
	}
	return result, toDownload
}

func (s *Server) runDownloads(links [][2]string, noteID string) {
	for _, lk := range links {
		urlStr, fp := lk[0], lk[1]
		log.Printf("Downloading webpage: %s", urlStr)
		cmd := exec.Command("monolith", urlStr, "-o", fp)
		out, err := cmd.CombinedOutput()
		failed := false
		if err != nil {
			log.Printf("Failed to run monolith: %v: %s", err, string(out))
			failed = true
		} else if !cmd.ProcessState.Success() {
			log.Printf("monolith exited with %v: %s", cmd.ProcessState, string(out))
			failed = true
		}
		if !failed {
			continue
		}
		log.Printf("Failed to download webpage: %s", urlStr)
		s.mu.Lock()
		isKnown := s.knownIDs()
		for i := range s.Notes {
			if s.Notes[i].ID == noteID {
				old := fmt.Sprintf("([local copy](%s/%s))", s.BasePath, fp)
				s.Notes[i].Content = strings.ReplaceAll(s.Notes[i].Content, old, "(local copy failed)")
				s.Notes[i].HTML = noteToHTML(s.Notes[i].Content, s.BasePath, isKnown)
				break
			}
		}
		fileContent := notesToFile(s.Notes)
		s.mu.Unlock()
		if err := os.WriteFile(s.NotesFile, []byte(fileContent), 0o644); err != nil {
			log.Printf("Failed to write notes file: %v", err)
		}
	}
}

func formatTimestampWithDay(ts string) string {
	t, err := time.Parse(timestampLayout, ts)
	if err != nil {
		return ts
	}
	return t.Format(timestampDisplayLayout)
}

var (
	headingContentRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)`)
	htmlHeadingRe    = regexp.MustCompile(`(?is)<h[1-6][^>]*>(.*?)</h[1-6]>`)
	htmlTagStripRe   = regexp.MustCompile(`<[^>]+>`)
	headingCloseRe   = regexp.MustCompile(`(?i)</h[1-6]>`)
	headingFullRe    = regexp.MustCompile(`(?is)(<h[1-6][^>]*>)(.*?)(</h[1-6]>)`)
	headingTagsSpan  = regexp.MustCompile(`(?is)\s*<span class="tags">(.*?)</span>\s*`)
	tagAnchorRe      = regexp.MustCompile(`(?is)<a class="tag" href="([^"]*)">([^<]+)</a>`)
)

// injectSubtitle inserts a Tufte-style <p class="subtitle">…</p> right
// after the first closing heading tag in noteHTML.  Tag chips already
// rendered inside that heading are extracted and re-rendered as
// `:Tag:`-style entries appended to the subtitle so the heading row
// stays clean and the tags live with the rest of the metadata.
func injectSubtitle(noteHTML, inner string) string {
	out := noteHTML
	tagsInner := ""
	if loc := headingFullRe.FindStringSubmatchIndex(out); loc != nil {
		innerStart, innerEnd := loc[4], loc[5]
		headInner := out[innerStart:innerEnd]
		if m := headingTagsSpan.FindStringSubmatchIndex(headInner); m != nil {
			tagsHTML := headInner[m[2]:m[3]]
			tagsInner = formatTagsAsColons(tagsHTML)
			cleaned := headInner[:m[0]] + headInner[m[1]:]
			out = out[:innerStart] + cleaned + out[innerEnd:]
		}
	}
	combined := inner
	if tagsInner != "" {
		combined += " &middot; " + tagsInner
	}
	subtitle := `<p class="subtitle">` + combined + `</p>`
	loc := headingCloseRe.FindStringIndex(out)
	if loc == nil {
		return subtitle + out
	}
	return out[:loc[1]] + subtitle + out[loc[1]:]
}

func formatTagsAsColons(tagsHTML string) string {
	matches := tagAnchorRe.FindAllStringSubmatch(tagsHTML, -1)
	if len(matches) == 0 {
		return ""
	}
	parts := make([]string, 0, len(matches))
	for i, m := range matches {
		text := m[2] + ":"
		if i == 0 {
			text = ":" + text
		}
		parts = append(parts, fmt.Sprintf(`<a class="tag" href="%s">%s</a>`, m[1], text))
	}
	return `<span class="tags">` + strings.Join(parts, "") + `</span>`
}

func noteTitle(n *Note) string {
	if m := headingContentRe.FindStringSubmatch(n.Content); m != nil {
		return strings.TrimSpace(m[1])
	}
	if m := htmlHeadingRe.FindStringSubmatch(n.Content); m != nil {
		text := htmlTagStripRe.ReplaceAllString(m[1], "")
		if s := strings.TrimSpace(text); s != "" {
			return s
		}
	}
	if i := strings.IndexByte(n.Content, '\n'); i != -1 {
		return strings.TrimSpace(n.Content[:i])
	}
	if s := strings.TrimSpace(n.Content); s != "" {
		return s
	}
	return "Note"
}

func timestampToID(ts string) string {
	var b strings.Builder
	for _, c := range ts {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// idToTimestamp recovers a human-readable timestamp from a temporal id.
// Recognized id shapes:
//   - `YYYYMMDDhhmmss`                14 digits, native textpod
//   - `YYYYMMDDThhmmss[.fraction]`    org-id-method ts
//
// Non-temporal ids (UUID and anything else) return false; the caller
// uses the current time when it needs a timestamp for such an id.
func idToTimestamp(id string) (string, bool) {
	if noteIDNumRe.MatchString(id) {
		return fmt.Sprintf("%s-%s-%s %s:%s:%s",
			id[0:4], id[4:6], id[6:8], id[8:10], id[10:12], id[12:14]), true
	}
	if noteIDTSRe.MatchString(id) {
		// `YYYYMMDDThhmmss[.fraction]`
		return fmt.Sprintf("%s-%s-%s %s:%s:%s",
			id[0:4], id[4:6], id[6:8], id[9:11], id[11:13], id[13:15]), true
	}
	return "", false
}

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

func noteToHTML(content, basePath string, isKnownID func(string) bool) string {
	var out string
	if isHTML(content) {
		out = content
	} else {
		var buf bytes.Buffer
		if err := md.Convert([]byte(content), &buf); err != nil {
			return ""
		}
		out = buf.String()
	}
	out = rewriteFileLinks(out, basePath, isKnownID)
	out = rewriteAttachmentLinks(out, basePath)
	out = addLazyLoadingToImages(out)
	return processTags(out, basePath)
}

// isHTML reports whether content looks like pre-rendered HTML
// (e.g. from the org Tufte exporter) rather than markdown.
func isHTML(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "<")
}

var (
	internalLinkRe = regexp.MustCompile(`href="((?:\.\./)*([^":?#]+)\.(md|html)(#[^"]*)?)"`)
	noteIDNumRe    = regexp.MustCompile(`^\d{14}$`)
	noteIDTSRe     = regexp.MustCompile(`^\d{8}T\d{6}(?:\.\d+)?$`)
)

// rewriteFileLinks rewrites internal links produced by org/markdown export.
// When an anchor strips down to a known note id (optionally prefixed with
// `ID-`), the href becomes `<basePath>/note/<id>`. Otherwise (no anchor,
// unknown anchor, or non-id-shaped anchor), it falls back to the filename
// search `<basePath>?q=<filename>.`.
func rewriteFileLinks(htmlStr, basePath string, isKnownID func(string) bool) string {
	return internalLinkRe.ReplaceAllStringFunc(htmlStr, func(match string) string {
		sub := internalLinkRe.FindStringSubmatch(match)
		pathNoExt := sub[2]
		anchor := sub[4]
		if anchor != "" && isKnownID != nil {
			id := strings.TrimPrefix(anchor[1:], "ID-")
			if isKnownID(id) {
				return fmt.Sprintf(`href="%s/note/%s"`, basePath, id)
			}
		}
		filename := pathNoExt
		if i := strings.LastIndex(filename, "/"); i >= 0 {
			filename = filename[i+1:]
		}
		return fmt.Sprintf(`href="%s?q=%s."`, basePath, url.QueryEscape(filename))
	})
}

func addLazyLoadingToImages(htmlStr string) string {
	return strings.ReplaceAll(htmlStr, "<img ", `<img loading="lazy" `)
}

func rewriteAttachmentLinks(htmlStr, basePath string) string {
	if basePath == "" {
		return htmlStr
	}
	htmlStr = strings.ReplaceAll(htmlStr, `"/assets/`, `"`+basePath+"/assets/")
	htmlStr = strings.ReplaceAll(htmlStr, `'/assets/`, `'`+basePath+"/assets/")
	return htmlStr
}

var (
	// Match either a bare <h2>…</h2> or, for ox-html output, the
	// enclosing <section><h2>…</h2> pair so the surrounding section
	// gets replaced by <details> rather than left dangling.
	sectionH2Re = regexp.MustCompile(`(?s)<section>\s*<h2(?:\s[^>]*)?>(.*?)</h2>`)
	bareH2Re    = regexp.MustCompile(`(?s)<h2(?:\s[^>]*)?>(.*?)</h2>`)
)

// wrapH3InDetails turns each <h2> block on the index into a
// collapsible <details>/<summary>.  Each <details> wraps the
// content following the heading up to the next <h2> (or end of
// input).  For ox-html notes the <h2> is wrapped in a <section>;
// that wrapping section is consumed so the resulting markup has a
// clean <details>…</details> boundary.
func wrapH3InDetails(htmlStr string) string {
	if strings.Contains(htmlStr, "<section>") &&
		sectionH2Re.MatchString(htmlStr) {
		return wrapSectionH2InDetails(htmlStr)
	}
	return wrapBareH2InDetails(htmlStr)
}

func wrapBareH2InDetails(htmlStr string) string {
	locs := bareH2Re.FindAllStringSubmatchIndex(htmlStr, -1)
	if len(locs) == 0 {
		return htmlStr
	}
	var b strings.Builder
	last := 0
	inDetails := false
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		titleStart, titleEnd := loc[2], loc[3]
		before := htmlStr[last:start]
		if inDetails {
			b.WriteString(before)
			b.WriteString("</details>\n")
		} else {
			b.WriteString(before)
		}
		inDetails = true
		fmt.Fprintf(&b, "<details><summary>%s</summary>\n", htmlStr[titleStart:titleEnd])
		last = end
	}
	b.WriteString(htmlStr[last:])
	if inDetails {
		b.WriteString("</details>\n")
	}
	return b.String()
}

// wrapSectionH2InDetails consumes the <section> wrapper that
// ox-html places around an <h2> headline, replacing it with a
// <details> element whose body runs to the matching </section>.
// The scan tracks balanced <section>/</section> nesting so the
// inner outline-text section (also a <section>) doesn't terminate
// the wrap prematurely.
func wrapSectionH2InDetails(s string) string {
	var b strings.Builder
	for {
		loc := sectionH2Re.FindStringSubmatchIndex(s)
		if loc == nil {
			b.WriteString(s)
			return b.String()
		}
		sectionStart, h2End := loc[0], loc[1]
		titleStart, titleEnd := loc[2], loc[3]
		depth := 1
		i := h2End
		closeStart := -1
		for i < len(s) {
			openIdx := strings.Index(s[i:], "<section>")
			closeIdx := strings.Index(s[i:], "</section>")
			if closeIdx == -1 {
				break
			}
			if openIdx != -1 && openIdx < closeIdx {
				depth++
				i += openIdx + len("<section>")
				continue
			}
			depth--
			if depth == 0 {
				closeStart = i + closeIdx
				break
			}
			i += closeIdx + len("</section>")
		}
		if closeStart == -1 {
			b.WriteString(s)
			return b.String()
		}
		body := s[h2End:closeStart]
		title := s[titleStart:titleEnd]
		b.WriteString(s[:sectionStart])
		b.WriteString("<details><summary>")
		b.WriteString(title)
		b.WriteString("</summary>")
		b.WriteString(body)
		b.WriteString("</details>\n")
		s = s[closeStart+len("</section>"):]
	}
}

var (
	// The trailing `([^A-Za-z0-9_@#]|$)` group enforces that the
	// closing `:` of a tag block is not followed by another tag-char.
	// Without it, input like `:line:column` would greedily match
	// `:line:` (treating `line` as a tag) and drop `column`.
	tagRe        = regexp.MustCompile(`(^|\s)(:[A-Za-z0-9_@#]+(?::[A-Za-z0-9_@#]+)*:)([^A-Za-z0-9_@#]|$)`)
	headingTagRe = regexp.MustCompile(`(?s)(<h[1-6][^>]*>)(.*?)(</h[1-6]>)`)
	// spanTagRe matches ox-html tag spans: <span class="tag"><span class="Blog">Blog</span></span>
	spanTagRe    = regexp.MustCompile(`(?:&#xa0;|\s)*<span class="tag">(.+?)</span>\s*$`)
	innerSpanRe  = regexp.MustCompile(`<span class="[^"]+">([^<]+)</span>`)
)

func processTags(htmlStr, basePath string) string {
	result := headingTagRe.ReplaceAllStringFunc(htmlStr, func(match string) string {
		sub := headingTagRe.FindStringSubmatch(match)
		open, inner, close := sub[1], sub[2], sub[3]
		// Try colon-format tags first (:Blog:)
		if loc := tagRe.FindStringSubmatchIndex(inner); loc != nil {
			tagStart, tagEnd := loc[4], loc[5]
			trailStart := loc[6]
			title := strings.TrimRight(inner[:tagStart], " \t")
			tagsHTML := tagsToLinks(inner[tagStart:tagEnd], basePath)
			rest := inner[trailStart:]
			return fmt.Sprintf(`%s<span>%s</span><span class="tags">%s</span>%s%s`, open, title, tagsHTML, rest, close)
		}
		// Try span-format tags from ox-html (<span class="tag"><span class="Blog">Blog</span></span>)
		if loc := spanTagRe.FindStringSubmatchIndex(inner); loc != nil {
			title := strings.TrimRight(inner[:loc[0]], " \t\n")
			spanContent := inner[loc[2]:loc[3]]
			matches := innerSpanRe.FindAllStringSubmatch(spanContent, -1)
			if len(matches) > 0 {
				tags := make([]string, 0, len(matches))
				for _, m := range matches {
					tags = append(tags, m[1])
				}
				colonTags := ":" + strings.Join(tags, ":") + ":"
				tagsHTML := tagsToLinks(colonTags, basePath)
				return fmt.Sprintf(`%s<span>%s</span><span class="tags">%s</span>%s`, open, title, tagsHTML, close)
			}
		}
		return fmt.Sprintf("%s<span>%s</span>%s", open, inner, close)
	})
	return tagRe.ReplaceAllStringFunc(result, func(match string) string {
		sub := tagRe.FindStringSubmatch(match)
		return sub[1] + tagsToLinks(sub[2], basePath) + sub[3]
	})
}

func tagsToLinks(tagStr, basePath string) string {
	trimmed := strings.Trim(tagStr, ":")
	tags := strings.Split(trimmed, ":")
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		parts = append(parts, fmt.Sprintf(`<a class="tag" href="%s?q=%s">%s</a>`, basePath, url.QueryEscape(":"+t+":"), t))
	}
	return strings.Join(parts, " ")
}

func urlToSafeFilename(u string) string {
	s := strings.TrimSpace(u)
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		switch c {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteByte('_')
		default:
			if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '.' || c == '_' {
				b.WriteRune(c)
			} else {
				b.WriteByte('_')
			}
		}
	}
	return strings.Trim(b.String(), ". ")
}
