// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/sdui"
	"github.com/wittywilla/wittymodule/service"
)

// DataEnv is where the server says the module's own folder is.
const DataEnv = "WITTYMODULE_DATA"

// Dual is one process answering as either of the two things a server starts the module's program
// as:
//
//   - a helper with one open disc, as it always was (Helper), started for each disc played or
//     ranked, and never given a network;
//   - the module's service (module API 5): the server's first call is then service.MethodStart,
//     with where its host API is. It listens on this machine only, with a secret of its own, for
//     the routes the server passes on, and answers the SDUI and lifecycle calls. It reads no disc.
//
// wittymodule/service.Main runs a service only, and answers every other call with
// "unknown-method", so it can't also be the helper a server asks about one disc: this is that part
// of it, with the same guard (a WittyModule gap: a service with helper calls of its own).
type Dual struct {
	Config
	Helper wittymodule.Handler

	mu      sync.Mutex
	started *service.Started
	host    *host.Client
	app     *App
}

// Handle is the helper protocol's handler, for wittymodule.Serve.
func (d *Dual) Handle(method string, params json.RawMessage) (any, []byte, error) {
	switch {
	case method == service.MethodStart:
		return d.start(params)
	case strings.HasPrefix(method, "lifecycle."):
		var a wittymodule.LifecycleArgs
		if len(params) > 0 && string(params) != "null" {
			if err := json.Unmarshal(params, &a); err != nil {
				return nil, nil, err
			}
		}
		return nil, nil, d.lifecycle(method, a)
	}
	answers := sdui.Answers{Items: func(req sdui.ItemsRequest) (sdui.ItemsAnswer, error) {
		a, err := d.App()
		if err != nil {
			return sdui.ItemsAnswer{}, err
		}
		return a.Items(req)
	}}
	if res, blob, err, ok := answers.Handle(method, params); ok {
		return res, blob, err
	}
	return d.Helper(method, params)
}

// App is the service's routes and sources, with its database opened the first time it's needed.
func (d *Dual) App() (*App, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.app != nil {
		return d.app, nil
	}
	st, err := Open(os.Getenv(DataEnv))
	if err != nil {
		return nil, err
	}
	d.app = &App{Config: d.Config, Host: hostLater{d}, Labels: st, Log: slog.Default()}
	return d.app, nil
}

// lifecycle answers the server's lifecycle calls: the database is made on first use, and taken
// away with the module only when the administrator didn't keep its data.
func (d *Dual) lifecycle(call string, a wittymodule.LifecycleArgs) error {
	if call != wittymodule.CallUninstall || a.KeepData {
		return nil
	}
	d.mu.Lock()
	if d.app != nil && d.app.Labels != nil {
		_ = d.app.Labels.Close()
		d.app = nil
	}
	d.mu.Unlock()
	if dir := os.Getenv(DataEnv); dir != "" {
		for _, f := range []string{File, File + "-wal", File + "-shm"} {
			if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (d *Dual) hostClient() *host.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.host
}

// start says where the service is, listening the first time. The server asks again now and
// then, with nothing, to see the process is there: what it said at the start stands.
func (d *Dual) start(params json.RawMessage) (any, []byte, error) {
	var p service.StartParams
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, nil, err
		}
	}
	d.mu.Lock()
	if p.Host != "" {
		d.host = host.NewAt(p.Host, p.HostToken, p.Offers)
	}
	started := d.started
	d.mu.Unlock()
	if started == nil {
		st, err := d.listen()
		if err != nil {
			return nil, nil, err
		}
		d.mu.Lock()
		d.started = st
		d.mu.Unlock()
		started = st
	}
	return started, nil, nil
}

// listen serves the routes on this machine only, behind the start's secret.
func (d *Dual) listen() (*service.Started, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(secret)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: Guard(token, d.Mux()), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(l) }()
	return &service.Started{URL: "http://" + l.Addr().String(), Token: token}, nil
}

// Mux is the service's handler: the module's routes under /http, as the server passes them.
func (d *Dual) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/http/", http.StripPrefix("/http", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := d.App()
		if err != nil {
			writeErr(w, 500, "The module's database couldn't be opened.")
			return
		}
		own := http.NewServeMux()
		a.Routes(own)
		own.ServeHTTP(w, r)
	})))
	return mux
}

// hostLater is the host API as the server last said where it is.
type hostLater struct{ d *Dual }

func (h hostLater) Do(ctx context.Context, on host.On, method, path string, body, out any) error {
	c := h.d.hostClient()
	if c == nil {
		return host.ErrNoHost
	}
	return c.Do(ctx, on, method, path, body, out)
}

// Guard lets in only whoever was told the start's secret, which is the server: a port on this
// machine can be reached by anything else running on it.
func Guard(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(service.TokenHeader)), want) != 1 {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
