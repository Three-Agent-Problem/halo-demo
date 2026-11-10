package main

// The browser side of the demo. Browsers can't speak SSH, so the page in
// docs/index.html runs xterm.js and talks to /ws over a WebSocket: each
// connection gets its own `halo --demo` on a PTY, exactly like an SSH session.
//
// Protocol: binary frames carry terminal bytes both ways; a text frame from
// the client is a JSON resize, {"cols":N,"rows":N}. The first resize sizes
// the PTY before the demo starts.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"charm.land/log/v2"
	"github.com/coder/websocket"
	"github.com/creack/pty"
	"golang.org/x/time/rate"
)

//go:embed docs/index.html
var indexHTML []byte

type webServer struct {
	bin      string
	slots    *slots
	idle     time.Duration
	max      time.Duration
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

func newWebServer(bin string, s *slots) *webServer {
	return &webServer{bin: bin, slots: s, idle: 10 * time.Minute, max: 30 * time.Minute, limiters: map[string]*rate.Limiter{}}
}

func (w *webServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = rw.Write(indexHTML)
	})
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /ws", w.serveWS)
	return mux
}

// clientIP is the visitor's address. Behind Traefik that is the first
// X-Forwarded-For hop; it is only used for rate limiting.
func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (w *webServer) allow(ip string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.limiters) > 10_000 {
		clear(w.limiters)
	}
	l, ok := w.limiters[ip]
	if !ok {
		l = rate.NewLimiter(rate.Every(2*time.Second), 5)
		w.limiters[ip] = l
	}
	return l.Allow()
}

type resize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func (w *webServer) serveWS(rw http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !w.allow(ip) {
		http.Error(rw, "slow down", http.StatusTooManyRequests)
		return
	}
	// Any origin: the page is also served from GitHub Pages.
	c, err := websocket.Accept(rw, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 10)

	if !w.slots.take() {
		_ = c.Write(r.Context(), websocket.MessageBinary, []byte("The Halo demo is full right now. Try again in a minute.\r\n"))
		_ = c.Close(websocket.StatusTryAgainLater, "full")
		return
	}
	defer w.slots.give()

	ctx, cancel := context.WithTimeout(r.Context(), w.max)
	defer cancel()

	size := resize{Cols: 120, Rows: 36}
	first, cancelFirst := context.WithTimeout(ctx, 5*time.Second)
	if typ, data, err := c.Read(first); err == nil && typ == websocket.MessageText {
		_ = json.Unmarshal(data, &size)
	}
	cancelFirst()

	home, err := os.MkdirTemp("", "halo-demo-")
	if err != nil {
		log.Error("tempdir", "err", err)
		return
	}
	defer os.RemoveAll(home)

	cmd := demoCmd(ctx, w.bin, home, "xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: size.Cols, Rows: size.Rows})
	if err != nil {
		log.Error("start", "err", err)
		return
	}
	defer f.Close()
	log.Info("web session", "remote", ip)

	// Browser → PTY. A quiet browser for longer than idle ends the session.
	go func() {
		defer cancel()
		for {
			rctx, rcancel := context.WithTimeout(ctx, w.idle)
			typ, data, err := c.Read(rctx)
			rcancel()
			if err != nil {
				return
			}
			if typ == websocket.MessageText {
				var s resize
				if json.Unmarshal(data, &s) == nil && s.Cols > 0 && s.Rows > 0 {
					_ = pty.Setsize(f, &pty.Winsize{Cols: s.Cols, Rows: s.Rows})
				}
				continue
			}
			if _, err := f.Write(data); err != nil {
				return
			}
		}
	}()

	// PTY → browser, until the demo exits.
	buf := make([]byte, 32<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	_ = cmd.Wait()
	if ctx.Err() == nil || errors.Is(ctx.Err(), context.Canceled) {
		_ = c.Write(context.Background(), websocket.MessageBinary, []byte(goodbye))
	}
	_ = c.Close(websocket.StatusNormalClosure, "bye")
}
