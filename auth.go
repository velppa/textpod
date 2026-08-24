package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie   = "textpod_session"
	oidcStateCookie = "textpod_oidc_state"
	sessionTTL      = 30 * 24 * time.Hour
	stateTTL        = 10 * time.Minute
)

// OIDC is the authorization code client used for browser sign-in.
// Sessions are stateless: the cookie carries the username and an
// expiry, signed with the client secret.
type OIDC struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURI  string

	mu            sync.Mutex
	authURL       string
	tokenURL      string
	endSessionURL string
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// discover reads the provider metadata, keeping the endpoints for
// later calls.
func (o *OIDC) discover() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.authURL != "" {
		return nil
	}
	resp, err := http.Get(strings.TrimRight(o.Issuer, "/") + "/.well-known/openid-configuration")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discovery returned %s", resp.Status)
	}
	var meta struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		EndSessionEndpoint    string `json:"end_session_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return err
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return fmt.Errorf("discovery document lacks endpoints")
	}
	o.authURL, o.tokenURL, o.endSessionURL =
		meta.AuthorizationEndpoint, meta.TokenEndpoint, meta.EndSessionEndpoint
	return nil
}

// signSession returns a cookie value binding the username to an expiry.
func (o *OIDC) signSession(user string, exp time.Time) string {
	payload := b64([]byte(user + "|" + strconv.FormatInt(exp.Unix(), 10)))
	mac := hmac.New(sha256.New, []byte(o.ClientSecret))
	mac.Write([]byte(payload))
	return payload + "." + b64(mac.Sum(nil))
}

// sessionUser reads the username out of a valid, unexpired session
// cookie value.
func (o *OIDC) sessionUser(value string) (string, bool) {
	payload, sig, ok := strings.Cut(value, ".")
	if !ok {
		return "", false
	}
	mac := hmac.New(sha256.New, []byte(o.ClientSecret))
	mac.Write([]byte(payload))
	if subtle.ConstantTimeCompare([]byte(b64(mac.Sum(nil))), []byte(sig)) != 1 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	user, expStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() >= exp {
		return "", false
	}
	return user, true
}

// claimsFromIDToken pulls the claim set out of an id_token. The token
// arrives over TLS straight from the token endpoint on an
// authenticated client request, so the signature adds nothing here.
func claimsFromIDToken(idToken string) (map[string]any, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed id_token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// localCredentials is what home-auth hands a co-hosted app: its OIDC
// client, and the API tokens registered for it with the user each one
// acts as.
type localCredentials struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Tokens       []struct {
		Name     string `json:"name"`
		Token    string `json:"token"`
		Username string `json:"username"`
	} `json:"tokens"`
}

// tokenUsers maps each API token to the user it acts as.
func (c localCredentials) tokenUsers() map[string]string {
	out := map[string]string{}
	for _, t := range c.Tokens {
		if t.Token != "" {
			out[t.Token] = t.Username
		}
	}
	return out
}

// fetchLocalCredentials asks a home-auth instance on this host for the
// credentials of the client registered under name, so neither the
// secret nor the API tokens have to live in the command line.  Retried
// a few times: the provider may still be coming up.
func fetchLocalCredentials(localURL, name string) (localCredentials, error) {
	endpoint := strings.TrimRight(localURL, "/") + "/local/client?name=" + url.QueryEscape(name)
	var reply localCredentials
	var err error
	// A refused connection means the provider has not come up yet;
	// anything it answers is a verdict, retrying will not change it.
	for attempt := 0; ; attempt++ {
		var reachable bool
		reachable, err = func() (bool, error) {
			resp, err := http.Get(endpoint)
			if err != nil {
				return false, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
				return true, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
			}
			return true, json.NewDecoder(resp.Body).Decode(&reply)
		}()
		if err == nil {
			break
		}
		if reachable || attempt == 4 {
			return localCredentials{}, fmt.Errorf("asking %s for %q credentials: %w",
				localURL, name, err)
		}
		time.Sleep(2 * time.Second)
	}
	if reply.ClientID == "" || reply.ClientSecret == "" {
		return localCredentials{}, fmt.Errorf("%s returned no credentials for %q", localURL, name)
	}
	return reply, nil
}

// -- server glue --

func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) cookiePath() string {
	if s.BasePath == "" {
		return "/"
	}
	return s.BasePath
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: s.cookiePath(),
		HttpOnly: true, Secure: requestIsSecure(r),
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

// sessionUsername returns the signed-in user of the request, if any.
func (s *Server) sessionUsername(r *http.Request) (string, bool) {
	if s.OIDC == nil {
		return "", false
	}
	c, ok := getCookieValue(r, sessionCookie)
	if !ok {
		return "", false
	}
	return s.OIDC.sessionUser(c)
}

// returnTo keeps redirects on this app: only local paths are honored.
func (s *Server) returnTo(raw string) string {
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return raw
	}
	if s.BasePath != "" {
		return s.BasePath + "/"
	}
	return "/"
}

// fullPath is the request path as the browser sees it, with the base
// path the proxy strips put back.
func (s *Server) fullPath(r *http.Request) string {
	p := s.BasePath + r.URL.Path
	if q := r.URL.RawQuery; q != "" {
		p += "?" + q
	}
	return p
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	if err := s.OIDC.discover(); err != nil {
		log.Printf("oidc discovery failed: %v", err)
		http.Error(w, "auth provider unreachable", http.StatusServiceUnavailable)
		return
	}
	state, verifier := randomString(16), randomString(32)
	ret := s.returnTo(r.URL.Query().Get("return"))
	s.setCookie(w, r, oidcStateCookie,
		strings.Join([]string{state, verifier, b64([]byte(ret))}, "."),
		int(stateTTL.Seconds()))

	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id":             {s.OIDC.ClientID},
		"redirect_uri":          {s.OIDC.RedirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"code_challenge":        {b64(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, s.OIDC.authURL+"?"+q.Encode(), http.StatusFound)
}

func (s *Server) authCallback(w http.ResponseWriter, r *http.Request) {
	if err := s.OIDC.discover(); err != nil {
		http.Error(w, "auth provider unreachable", http.StatusServiceUnavailable)
		return
	}
	raw, ok := getCookieValue(r, oidcStateCookie)
	if !ok {
		http.Error(w, "no login in progress", http.StatusBadRequest)
		return
	}
	s.setCookie(w, r, oidcStateCookie, "", -1)

	parts := strings.Split(raw, ".")
	if len(parts) != 3 || subtle.ConstantTimeCompare(
		[]byte(parts[0]), []byte(r.URL.Query().Get("state"))) != 1 {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "no code in callback", http.StatusBadRequest)
		return
	}

	user, err := s.OIDC.exchange(code, parts[1])
	if err != nil {
		log.Printf("token exchange failed: %v", err)
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}
	s.setCookie(w, r, sessionCookie,
		s.OIDC.signSession(user, time.Now().Add(sessionTTL)),
		int(sessionTTL.Seconds()))

	ret := "/"
	if b, err := base64.RawURLEncoding.DecodeString(parts[2]); err == nil {
		ret = s.returnTo(string(b))
	}
	http.Redirect(w, r, ret, http.StatusFound)
}

// exchange trades the authorization code for tokens and returns the
// username of the signed-in user.
func (o *OIDC) exchange(code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {o.RedirectURI},
		"code_verifier": {verifier},
		"client_id":     {o.ClientID},
		"client_secret": {o.ClientSecret},
	}
	resp, err := http.PostForm(o.tokenURL, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %s", resp.Status)
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	claims, err := claimsFromIDToken(tok.IDToken)
	if err != nil {
		return "", err
	}
	for _, k := range []string{"preferred_username", "username", "sub"} {
		if v, ok := claims[k].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("id_token carries no username")
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w, r, sessionCookie, "", -1)
	target := s.returnTo("")
	if err := s.OIDC.discover(); err == nil && s.OIDC.endSessionURL != "" {
		q := url.Values{
			"client_id":                {s.OIDC.ClientID},
			"post_logout_redirect_uri": {s.OIDC.postLogoutURI()},
		}
		target = s.OIDC.endSessionURL + "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// postLogoutURI is the app URL the provider bounces back to; derived
// from the redirect URI so it stays on a registered origin.
func (o *OIDC) postLogoutURI() string {
	return strings.TrimSuffix(o.RedirectURI, "/auth/callback") + "/"
}

// authLinkHTML renders the sign-in / sign-out footer link.
func (s *Server) authLinkHTML(r *http.Request) string {
	if s.OIDC == nil {
		return ""
	}
	if user, ok := s.sessionUsername(r); ok {
		return fmt.Sprintf(` &middot; %s (<a href="%s/auth/logout">sign out</a>)`,
			user, s.BasePath)
	}
	ret := url.QueryEscape(s.fullPath(r))
	return fmt.Sprintf(` &middot; <a href="%s/auth/login?return=%s">sign in</a>`,
		s.BasePath, ret)
}
