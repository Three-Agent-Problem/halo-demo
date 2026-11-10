// Command halo-demo serves the Halo demo over SSH: every session gets its own
// `halo --demo` process on a real PTY, so `ssh halo.3agents.dev` is all a
// visitor needs. Halo itself is untouched; this only runs its binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"charm.land/log/v2"
	"charm.land/ssh"
	"charm.land/wish/v2"
	"charm.land/wish/v2/activeterm"
	"charm.land/wish/v2/logging"
	"charm.land/wish/v2/ratelimiter"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/time/rate"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := env("HALO_DEMO_ADDR", ":2222")
	hostKey := env("HALO_DEMO_HOST_KEY", ".ssh/halo_demo_ed25519")
	bin := env("HALO_DEMO_BIN", "halo")
	maxSessions, _ := strconv.Atoi(env("HALO_DEMO_MAX_SESSIONS", "50"))

	if _, err := exec.LookPath(bin); err != nil {
		log.Fatal("halo binary not found", "bin", bin, "err", err)
	}

	open := func(ssh.Context, ssh.PublicKey) bool { return true }
	srv, err := wish.NewServer(
		wish.WithAddress(addr),
		wish.WithHostKeyPath(hostKey),
		// Anyone may come in: any key, any password, or none at all.
		wish.WithPublicKeyAuth(open),
		wish.WithPasswordAuth(func(ssh.Context, string) bool { return true }),
		wish.WithKeyboardInteractiveAuth(func(ssh.Context, gossh.KeyboardInteractiveChallenge) bool { return true }),
		wish.WithIdleTimeout(10*time.Minute),
		wish.WithMaxTimeout(30*time.Minute),
		ssh.AllocatePty(),
		wish.WithMiddleware(
			demo(bin),
			capacity(maxSessions),
			ratelimiter.Middleware(ratelimiter.NewRateLimiter(rate.Every(2*time.Second), 5, 10_000)),
			activeterm.Middleware(),
			logging.Middleware(),
		),
	)
	if err != nil {
		log.Fatal("server", "err", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("serving the Halo demo", "addr", addr, "max_sessions", maxSessions)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("listen", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}

// capacity turns visitors away once max sessions are running.
func capacity(max int) wish.Middleware {
	var n atomic.Int64
	return func(next ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			if n.Add(1) > int64(max) {
				n.Add(-1)
				wish.Println(s, "The Halo demo is full right now. Try again in a minute.")
				_ = s.Exit(1)
				return
			}
			defer n.Add(-1)
			next(s)
		}
	}
}

// demo runs `halo --demo` on the session's PTY. Commands and subsystems are
// refused: the only thing this server does is the demo.
func demo(bin string) wish.Middleware {
	return func(ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			if len(s.Command()) > 0 || s.Subsystem() != "" {
				wish.Println(s, "This server only runs the Halo demo: ssh without a command.")
				_ = s.Exit(1)
				return
			}
			pty, _, _ := s.Pty() // activeterm guarantees one

			home, err := os.MkdirTemp("", "halo-demo-")
			if err != nil {
				wish.Errorln(s, "could not start the demo")
				log.Error("tempdir", "err", err)
				_ = s.Exit(1)
				return
			}
			defer os.RemoveAll(home)

			cmd := exec.CommandContext(s.Context(), bin, "--demo")
			cmd.Dir = home
			cmd.Env = []string{
				"PATH=/usr/local/bin:/usr/bin:/bin",
				"HOME=" + home,
				"XDG_CONFIG_HOME=" + home + "/.config",
				"XDG_STATE_HOME=" + home + "/.local/state",
				"TERM=" + pty.Term,
				"COLORTERM=truecolor",
				"LANG=C.UTF-8",
			}
			cmd.WaitDelay = 2 * time.Second
			if err := pty.Start(cmd, ssh.WithJobControl()); err != nil {
				wish.Errorln(s, "could not start the demo")
				log.Error("start", "err", err)
				_ = s.Exit(1)
				return
			}
			err = cmd.Wait()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) && s.Context().Err() == nil {
				code = exit.ExitCode()
			}
			if host, _, e := net.SplitHostPort(s.RemoteAddr().String()); e == nil {
				log.Debug("session done", "remote", host, "code", code)
			}
			wish.Println(s, fmt.Sprintf("\nThanks for trying Halo. Install it: %s", "https://github.com/three-agent-problem/halo"))
			_ = s.Exit(code)
		}
	}
}
