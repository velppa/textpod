package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeProvider stands in for home-auth: discovery, plus a token
// endpoint that hands back an id_token for user "velppa".
func fakeProvider(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/oidc/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 base + "/oidc",
			"authorization_endpoint": base + "/oidc/auth",
			"token_endpoint":         base + "/oidc/token",
			"end_session_endpoint":   base + "/oidc/session/end",
		})
	})
	mux.HandleFunc("/oidc/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" ||
			r.PostForm.Get("code") != "thecode" ||
			r.PostForm.Get("code_verifier") == "" ||
			r.PostForm.Get("client_secret") != "s3cret" {
			http.Error(w, "bad request", 400)
			return
		}
		claims := base64.RawURLEncoding.EncodeToString(
			[]byte(`{"sub":"velppa","preferred_username":"velppa"}`))
		json.NewEncoder(w).Encode(map[string]string{
			"access_token": "at", "token_type": "Bearer",
			"id_token": "h." + claims + ".sig"})
	})
	ts := httptest.NewServer(mux)
	base = ts.URL
	t.Cleanup(ts.Close)
	return ts
}

func oidcTestServer(t *testing.T) *Server {
	t.Helper()
	ts := fakeProvider(t)
	s := newTestServer(nil)
	s.OIDC = &OIDC{
		Issuer:       ts.URL + "/oidc",
		ClientID:     "textpod",
		ClientSecret: "s3cret",
		RedirectURI:  "https://example.com/notes/auth/callback",
	}
	return s
}

func TestSessionCookieRoundTrip(t *testing.T) {
	o := &OIDC{ClientSecret: "s3cret"}
	value := o.signSession("velppa", time.Now().Add(time.Hour))
	if user, ok := o.sessionUser(value); !ok || user != "velppa" {
		t.Fatalf("got %q %v, want velppa true", user, ok)
	}
}

func TestSessionCookieRejected(t *testing.T) {
	o := &OIDC{ClientSecret: "s3cret"}
	cases := map[string]string{
		"tampered": o.signSession("velppa", time.Now().Add(time.Hour)) + "x",
		"expired":  o.signSession("velppa", time.Now().Add(-time.Minute)),
		"garbage":  "not-a-cookie",
		"other key": (&OIDC{ClientSecret: "other"}).
			signSession("velppa", time.Now().Add(time.Hour)),
	}
	for name, value := range cases {
		if user, ok := o.sessionUser(value); ok {
			t.Errorf("%s: accepted, user %q", name, user)
		}
	}
}

func TestRequireAuthOpenWithoutAuth(t *testing.T) {
	s := newTestServer(nil)
	rec := httptest.NewRecorder()
	if !s.requireAuth(rec, httptest.NewRequest("POST", "/notes", nil)) {
		t.Fatal("writes should be open with no token and no OIDC")
	}
}

// The API keeps working on the token alone, even with OIDC enabled.
func TestRequireAuthTokenStillWorks(t *testing.T) {
	s := oidcTestServer(t)
	s.Token, s.HasToken = "tok", true

	req := httptest.NewRequest("POST", "/notes", nil)
	req.Header.Set("Authorization", "Bearer tok")
	if !s.requireAuth(httptest.NewRecorder(), req) {
		t.Fatal("bearer token rejected")
	}

	req = httptest.NewRequest("POST", "/notes", nil)
	req.AddCookie(&http.Cookie{Name: "textpod_token", Value: "tok"})
	if !s.requireAuth(httptest.NewRecorder(), req) {
		t.Fatal("token cookie rejected")
	}
}

func TestRequireAuthSession(t *testing.T) {
	s := oidcTestServer(t)

	rec := httptest.NewRecorder()
	if s.requireAuth(rec, httptest.NewRequest("POST", "/notes", nil)) {
		t.Fatal("anonymous write allowed")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}

	req := httptest.NewRequest("POST", "/notes", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie,
		Value: s.OIDC.signSession("velppa", time.Now().Add(time.Hour))})
	if !s.requireAuth(httptest.NewRecorder(), req) {
		t.Fatal("session write rejected")
	}
}

func TestAuthLoginRedirect(t *testing.T) {
	s := oidcTestServer(t)
	req := httptest.NewRequest("GET", "/auth/login?return=/notes/note/42", nil)
	rec := httptest.NewRecorder()
	s.authLogin(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("got %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	for k, want := range map[string]string{
		"client_id":             "textpod",
		"redirect_uri":          "https://example.com/notes/auth/callback",
		"response_type":         "code",
		"code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Error("state or code_challenge missing")
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), oidcStateCookie) {
		t.Error("state cookie not set")
	}
}

// stateCookie drives a login to grab the state cookie the callback needs.
func stateCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	s.authLogin(rec, httptest.NewRequest("GET", "/auth/login?return=/notes/", nil))
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcStateCookie {
			return c
		}
	}
	t.Fatal("no state cookie")
	return nil
}

func TestAuthCallbackSignsIn(t *testing.T) {
	s := oidcTestServer(t)
	c := stateCookie(t, s)
	state := strings.Split(c.Value, ".")[0]

	req := httptest.NewRequest("GET", "/auth/callback?code=thecode&state="+state, nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	s.authCallback(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("got %d (%s), want 302", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/notes/" {
		t.Errorf("Location = %q, want /notes/", got)
	}
	var session string
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == sessionCookie && sc.Value != "" {
			session = sc.Value
		}
	}
	if session == "" {
		t.Fatal("no session cookie")
	}
	if user, ok := s.OIDC.sessionUser(session); !ok || user != "velppa" {
		t.Fatalf("session user %q %v, want velppa true", user, ok)
	}
}

func TestAuthCallbackStateMismatch(t *testing.T) {
	s := oidcTestServer(t)
	c := stateCookie(t, s)
	req := httptest.NewRequest("GET", "/auth/callback?code=thecode&state=wrong", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	s.authCallback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestReturnToRejectsExternal(t *testing.T) {
	s := newTestServer(nil)
	s.BasePath = "/notes"
	for _, raw := range []string{"https://evil.example", "//evil.example", "evil"} {
		if got := s.returnTo(raw); got != "/notes/" {
			t.Errorf("returnTo(%q) = %q, want /notes/", raw, got)
		}
	}
	if got := s.returnTo("/notes/note/42"); got != "/notes/note/42" {
		t.Errorf("local path mangled: %q", got)
	}
}

func TestAuthLinkHTML(t *testing.T) {
	s := newTestServer(nil)
	if got := s.authLinkHTML(httptest.NewRequest("GET", "/", nil)); got != "" {
		t.Fatalf("no OIDC should render nothing, got %q", got)
	}

	s = oidcTestServer(t)
	s.BasePath = "/notes"
	out := s.authLinkHTML(httptest.NewRequest("GET", "/?q=tag", nil))
	if !strings.Contains(out, "/notes/auth/login?return="+url.QueryEscape("/notes/?q=tag")) {
		t.Errorf("sign-in link wrong: %q", out)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie,
		Value: s.OIDC.signSession("velppa", time.Now().Add(time.Hour))})
	out = s.authLinkHTML(req)
	if !strings.Contains(out, "velppa") || !strings.Contains(out, "/notes/auth/logout") {
		t.Errorf("sign-out link wrong: %q", out)
	}
}

func TestAuthLogoutClearsSessionAndEndsProviderSession(t *testing.T) {
	s := oidcTestServer(t)
	req := httptest.NewRequest("GET", "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie,
		Value: s.OIDC.signSession("velppa", time.Now().Add(time.Hour))})
	rec := httptest.NewRecorder()
	s.authLogout(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("got %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/oidc/session/end") {
		t.Errorf("Location = %q, want provider end_session", loc)
	}
	u, _ := url.Parse(loc)
	if got := u.Query().Get("post_logout_redirect_uri"); got != "https://example.com/notes/" {
		t.Errorf("post_logout_redirect_uri = %q", got)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge >= 0 {
			t.Errorf("session cookie not cleared: %+v", c)
		}
	}
}

func TestNotePageDeleteLinkGated(t *testing.T) {
	s := oidcTestServer(t)
	s.Notes = []Note{{ID: "42", Timestamp: "2026-08-20 10:00:00",
		Content: "hi", HTML: "<p>hi</p>"}}

	page := func(withSession bool) string {
		req := httptest.NewRequest("GET", "/note/42", nil)
		req.SetPathValue("id", "42")
		if withSession {
			req.AddCookie(&http.Cookie{Name: sessionCookie,
				Value: s.OIDC.signSession("velppa", time.Now().Add(time.Hour))})
		}
		rec := httptest.NewRecorder()
		s.notePage(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", rec.Code)
		}
		return rec.Body.String()
	}

	if body := page(false); strings.Contains(body, `id="deleteLink"`) {
		t.Error("anonymous reader sees the delete link")
	}
	body := page(true)
	if !strings.Contains(body, `id="deleteLink"`) {
		t.Error("signed-in user has no delete link")
	}
	if !strings.Contains(body, `'/notes/' + "42"`) {
		t.Error("delete script does not target the note")
	}
}

func TestFetchLocalCredentials(t *testing.T) {
	var gotName string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/local/client" {
			http.Error(w, "not found", 404)
			return
		}
		gotName = r.URL.Query().Get("name")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":    "https://auth.example/oidc",
			"client_id": "textpod", "client_secret": "s3cret",
			"tokens": []map[string]string{
				{"name": "api", "token": "t0ken", "username": "velppa"}}})
	}))
	defer ts.Close()

	creds, err := fetchLocalCredentials(ts.URL, "Textpod")
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "Textpod" {
		t.Errorf("name = %q, want Textpod", gotName)
	}
	if creds.Issuer != "https://auth.example/oidc" ||
		creds.ClientID != "textpod" || creds.ClientSecret != "s3cret" {
		t.Errorf("got %+v", creds)
	}
	if got := creds.tokenUsers(); len(got) != 1 || got["t0ken"] != "velppa" {
		t.Errorf("tokenUsers = %v, want t0ken -> velppa", got)
	}
}

func TestFetchLocalCredentialsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no client named Nope", http.StatusNotFound)
	}))
	defer ts.Close()

	if _, err := fetchLocalCredentials(ts.URL, "Nope"); err == nil {
		t.Fatal("want an error for an unknown client")
	} else if !strings.Contains(err.Error(), "no client named Nope") {
		t.Errorf("error loses the provider's reason: %v", err)
	}
}

// A token fetched from home-auth authorizes writes on its own, with no
// --token flag in sight.
func TestRequireAuthFetchedAPIToken(t *testing.T) {
	s := oidcTestServer(t)
	s.APITokens = map[string]string{"t0ken": "velppa"}

	req := httptest.NewRequest("POST", "/notes", nil)
	req.Header.Set("Authorization", "Bearer t0ken")
	if !s.requireAuth(httptest.NewRecorder(), req) {
		t.Error("bearer API token rejected")
	}

	req = httptest.NewRequest("POST", "/notes", nil)
	req.AddCookie(&http.Cookie{Name: "textpod_token", Value: "t0ken"})
	if !s.requireAuth(httptest.NewRecorder(), req) {
		t.Error("API token cookie rejected")
	}

	req = httptest.NewRequest("POST", "/notes", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	if s.requireAuth(httptest.NewRecorder(), req) {
		t.Error("unknown token accepted")
	}
}

// The ?token= link works for a fetched token and sets the cookie.
func TestIndexTokenLinkAcceptsFetchedToken(t *testing.T) {
	s := oidcTestServer(t)
	s.APITokens = map[string]string{"t0ken": "velppa"}

	rec := httptest.NewRecorder()
	s.index(rec, httptest.NewRequest("GET", "/?token=t0ken", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want 303", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "textpod_token=t0ken") {
		t.Errorf("token cookie not set: %q", rec.Header().Get("Set-Cookie"))
	}
}

func TestKnownToken(t *testing.T) {
	s := newTestServer(nil)
	s.Token, s.HasToken = "flagtok", true
	s.APITokens = map[string]string{"t0ken": "velppa"}
	for value, want := range map[string]bool{
		"flagtok": true, "t0ken": true, "": false, "nope": false} {
		if got := s.knownToken(value); got != want {
			t.Errorf("knownToken(%q) = %v, want %v", value, got, want)
		}
	}
}
