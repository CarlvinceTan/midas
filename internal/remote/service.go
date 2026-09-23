package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	sessionCookie = "midas_remote"
	loginLimit    = 8
	loginWindow   = 5 * time.Minute
	// maxLoginAttempts bounds the per-address throttle table, so a scan from many
	// source addresses cannot grow it for as long as the session lives.
	maxLoginAttempts = 4096
)

// Tunnel is an active public route to the local server.
type Tunnel interface {
	URL() string
	Close() error
}

// TunnelStarter creates an ephemeral public route without an account.
type TunnelStarter func(context.Context, int) (Tunnel, error)

// Options configures one remote session.
type Options struct {
	Terminal        *Terminal
	Password        string
	Tunnel          TunnelStarter
	PreventSleep    func() (io.Closer, error)
	VerifyPublic    func(context.Context, string) error
	ReadHeaderLimit time.Duration
}

// Service owns the local web server, public tunnel, authenticated browser
// sessions and sleep inhibitor for one /remote activation.
type Service struct {
	terminal *Terminal
	password [sha256.Size]byte
	listener net.Listener
	server   *http.Server
	tunnel   Tunnel
	awake    io.Closer
	url      string
	localURL string

	mu       sync.Mutex
	tokens   map[string]struct{}
	attempts map[string]loginAttempt
	sockets  map[*websocket.Conn]struct{}
	stopOnce sync.Once
}

type loginAttempt struct {
	count   int
	resetAt time.Time
}

type clientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// Start creates the loopback server, holds the computer awake and waits for a
// Cloudflare Quick Tunnel URL. Failure unwinds every partially-started resource.
func Start(ctx context.Context, options Options) (*Service, error) {
	if options.Terminal == nil {
		return nil, errors.New("remote: terminal is required")
	}
	if strings.TrimSpace(options.Password) == "" {
		return nil, errors.New("remote: password is required")
	}
	startTunnel := options.Tunnel
	if startTunnel == nil {
		startTunnel = StartQuickTunnel
	}
	preventSleep := options.PreventSleep
	if preventSleep == nil {
		preventSleep = PreventSleep
	}
	headerTimeout := options.ReadHeaderLimit
	if headerTimeout <= 0 {
		headerTimeout = 5 * time.Second
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("remote: listen: %w", err)
	}
	service := &Service{
		terminal: options.Terminal, password: sha256.Sum256([]byte(options.Password)), listener: listener,
		tokens: make(map[string]struct{}), attempts: make(map[string]loginAttempt), sockets: make(map[*websocket.Conn]struct{}),
	}
	service.localURL = "http://" + listener.Addr().String()
	service.server = &http.Server{
		Handler: service.routes(), ReadHeaderTimeout: headerTimeout,
		// WebSocket clients can be idle between frames; the read loop is what keeps
		// the connection honest, so only plain keep-alive requests are capped.
		IdleTimeout: 2 * time.Minute,
	}
	go func() {
		if serveErr := service.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			_ = service.Stop(context.Background())
		}
	}()
	service.awake, err = preventSleep()
	if err != nil {
		_ = service.Stop(context.Background())
		return nil, fmt.Errorf("remote: prevent sleep: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	service.tunnel, err = startTunnel(ctx, port)
	if err != nil {
		_ = service.Stop(context.Background())
		return nil, err
	}
	service.url = service.tunnel.URL()
	if !strings.HasPrefix(service.url, "https://") {
		_ = service.Stop(context.Background())
		return nil, errors.New("remote: tunnel did not return a secure public URL")
	}
	verifyPublic := options.VerifyPublic
	if verifyPublic == nil {
		verifyPublic = waitForPublicHealth
	}
	if err := verifyPublic(ctx, service.url+"/healthz"); err != nil {
		_ = service.Stop(context.Background())
		return nil, fmt.Errorf("remote: public link did not become reachable: %w", err)
	}
	return service, nil
}

func (s *Service) URL() string      { return s.url }
func (s *Service) LocalURL() string { return s.localURL }

// Stop revokes browser sessions, closes sockets, ends the tunnel and releases
// the sleep inhibitor. It is safe to call more than once.
func (s *Service) Stop(ctx context.Context) error {
	var result error
	s.stopOnce.Do(func() {
		s.mu.Lock()
		sockets := make([]*websocket.Conn, 0, len(s.sockets))
		for socket := range s.sockets {
			sockets = append(sockets, socket)
		}
		s.tokens = make(map[string]struct{})
		s.mu.Unlock()
		for _, socket := range sockets {
			socket.CloseNow()
		}
		if s.tunnel != nil {
			result = errors.Join(result, s.tunnel.Close())
		}
		if s.server != nil {
			shutdownCtx := ctx
			if shutdownCtx == nil {
				shutdownCtx = context.Background()
			}
			bounded, cancel := context.WithTimeout(shutdownCtx, 3*time.Second)
			graceful := s.server.Shutdown(bounded)
			cancel()
			if graceful != nil {
				// Shutdown will not close a connection that has not sent its first
				// request until it has been idle for five seconds, so a client that
				// only opened a socket outlasts the graceful budget. Stop must
				// always release the listener, so force the remaining connections
				// closed and report only a failure to close them.
				result = errors.Join(result, s.server.Close())
			}
		}
		if s.awake != nil {
			result = errors.Join(result, s.awake.Close())
		}
	})
	return result
}

func (s *Service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /client.js", s.handleClientJS)
	mux.HandleFunc("GET /login.js", s.handleLoginJS)
	mux.HandleFunc("GET /client.css", s.handleCSS)
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		s.send(response, http.StatusOK, "text/plain; charset=utf-8", "ok")
	})
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /ws", s.handleWebSocket)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		setSecurityHeaders(response)
		mux.ServeHTTP(response, request)
	})
}

func (s *Service) handleIndex(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" && request.URL.Path != "/index.html" {
		s.send(response, http.StatusNotFound, "text/plain; charset=utf-8", "not found")
		return
	}
	if s.authenticated(request) {
		s.send(response, http.StatusOK, "text/html; charset=utf-8", terminalHTML)
		return
	}
	s.send(response, http.StatusOK, "text/html; charset=utf-8", loginHTML)
}

func (s *Service) handleClientJS(response http.ResponseWriter, _ *http.Request) {
	s.send(response, http.StatusOK, "text/javascript; charset=utf-8", clientJS)
}

func (s *Service) handleLoginJS(response http.ResponseWriter, _ *http.Request) {
	s.send(response, http.StatusOK, "text/javascript; charset=utf-8", loginJS)
}

func (s *Service) handleCSS(response http.ResponseWriter, _ *http.Request) {
	s.send(response, http.StatusOK, "text/css; charset=utf-8", clientCSS)
}

func (s *Service) handleLogin(response http.ResponseWriter, request *http.Request) {
	key := clientAddress(request)
	if s.loginLocked(key, time.Now()) {
		s.send(response, http.StatusTooManyRequests, "text/plain; charset=utf-8", "Too many attempts Try again later")
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 4096)
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		s.send(response, http.StatusBadRequest, "text/plain; charset=utf-8", "bad request")
		return
	}
	actual := sha256.Sum256([]byte(body.Password))
	if subtle.ConstantTimeCompare(actual[:], s.password[:]) != 1 {
		s.loginFailed(key, time.Now())
		s.send(response, http.StatusUnauthorized, "text/plain; charset=utf-8", "Wrong password")
		return
	}
	s.loginSucceeded(key)
	token, err := randomToken()
	if err != nil {
		s.send(response, http.StatusInternalServerError, "text/plain; charset=utf-8", "login unavailable")
		return
	}
	s.mu.Lock()
	s.tokens[token] = struct{}{}
	s.mu.Unlock()
	cookie := &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: forwardedHTTPS(request), MaxAge: 30 * 24 * 60 * 60,
	}
	http.SetCookie(response, cookie)
	s.send(response, http.StatusOK, "application/json", `{"ok":true}`)
}

func (s *Service) handleLogout(response http.ResponseWriter, request *http.Request) {
	if cookie, err := request.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.tokens, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(response, &http.Cookie{Name: sessionCookie, Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteStrictMode})
	s.send(response, http.StatusOK, "application/json", `{"ok":true}`)
}

func (s *Service) handleWebSocket(response http.ResponseWriter, request *http.Request) {
	if !s.authenticated(request) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	s.mu.Lock()
	s.sockets[socket] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sockets, socket)
		s.mu.Unlock()
		_ = socket.CloseNow()
	}()
	client := s.terminal.Attach(defaultColumns, defaultRows)
	defer client.Close()
	connectionContext, cancel := context.WithCancel(request.Context())
	defer cancel()
	writesDone := make(chan struct{})
	go func() {
		defer close(writesDone)
		for output := range client.Output() {
			writeContext, writeCancel := context.WithTimeout(connectionContext, 10*time.Second)
			err := socket.Write(writeContext, websocket.MessageText, []byte(output))
			writeCancel()
			if err != nil {
				cancel()
				return
			}
		}
	}()
	for {
		_, raw, readErr := socket.Read(connectionContext)
		if readErr != nil {
			break
		}
		var message clientMessage
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		switch message.Type {
		case "input":
			if len(message.Data) <= 1<<20 {
				client.Input(message.Data)
			}
		case "resize":
			client.Resize(message.Cols, message.Rows)
		}
	}
	cancel()
	client.Close()
	<-writesDone
}

func (s *Service) authenticated(request *http.Request) bool {
	cookie, err := request.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	s.mu.Lock()
	_, ok := s.tokens[cookie.Value]
	s.mu.Unlock()
	return ok
}

func (s *Service) loginLocked(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.attempts[key]
	if !ok {
		return false
	}
	if !now.Before(attempt.resetAt) {
		delete(s.attempts, key)
		return false
	}
	return attempt.count >= loginLimit
}

func (s *Service) loginFailed(key string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLoginAttemptsLocked(now)
	attempt := s.attempts[key]
	if !now.Before(attempt.resetAt) {
		attempt = loginAttempt{resetAt: now.Add(loginWindow)}
	}
	attempt.count++
	s.attempts[key] = attempt
}

// pruneLoginAttemptsLocked drops expired entries, and the entry closest to
// expiring when the table is at its bound. The caller holds s.mu.
func (s *Service) pruneLoginAttemptsLocked(now time.Time) {
	if len(s.attempts) < maxLoginAttempts {
		return
	}
	oldestKey, oldest := "", time.Time{}
	for key, attempt := range s.attempts {
		if !now.Before(attempt.resetAt) {
			delete(s.attempts, key)
			continue
		}
		if oldest.IsZero() || attempt.resetAt.Before(oldest) {
			oldestKey, oldest = key, attempt.resetAt
		}
	}
	if len(s.attempts) >= maxLoginAttempts && oldestKey != "" {
		delete(s.attempts, oldestKey)
	}
}

func (s *Service) loginSucceeded(key string) {
	s.mu.Lock()
	delete(s.attempts, key)
	s.mu.Unlock()
}

func (s *Service) send(response http.ResponseWriter, status int, contentType, body string) {
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_, _ = io.WriteString(response, body)
}

func setSecurityHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; connect-src 'self'; img-src 'self' data:; font-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Frame-Options", "DENY")
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func clientAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	if net.ParseIP(host).IsLoopback() {
		if forwarded := strings.TrimSpace(request.Header.Get("CF-Connecting-IP")); net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	return host
}

func forwardedHTTPS(request *http.Request) bool {
	if request.TLS != nil || strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	return strings.Contains(strings.ToLower(request.Header.Get("CF-Visitor")), `"scheme":"https"`)
}

func waitForPublicHealth(ctx context.Context, healthURL string) error {
	deadlineContext, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(resolveContext context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(resolveContext, network, "1.1.1.1:53")
		},
	}
	transport := &http.Transport{
		DialContext: func(dialContext context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := resolver.LookupHost(dialContext, host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, resolved := range addresses {
				connection, dialErr := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(dialContext, network, net.JoinHostPort(resolved, port))
				if dialErr == nil {
					return connection, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout: 6 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 8 * time.Second, Transport: transport}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(deadlineContext, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", response.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-deadlineContext.Done():
			if lastErr != nil {
				// The last failure is wrapped so a caller can see why the link never
				// became reachable.
				return fmt.Errorf("%w: %w", deadlineContext.Err(), lastErr)
			}
			return deadlineContext.Err()
		case <-ticker.C:
		}
	}
}
