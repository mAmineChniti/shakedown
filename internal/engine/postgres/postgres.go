// Package postgres implements engine.Engine by provisioning a throwaway
// Postgres container via the Docker CLI, or by connecting to a user-supplied
// scratch DSN.
package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/mAmineChniti/shakedown/internal/engine"
)

const (
	defaultVersion  = "16"
	containerPrefix = "shakedown-"
	dbName          = "shakedown"
	password        = "shakedown" // throwaway container credential, never logged
)

// Engine is a Postgres-backed engine.Engine.
type Engine struct {
	opts        engine.Options
	container   string
	dsn         string
	externalDSN bool
}

// New provisions (or validates) the scratch database. Call Teardown when
// finished; it is safe to call on a partially constructed Engine.
func New(ctx context.Context, opts engine.Options) (*Engine, error) {
	e := &Engine{opts: opts}

	if opts.DSN != "" {
		if err := engine.ValidateDSNHost(opts.DSN, opts.AllowRemoteScratch); err != nil {
			return nil, err
		}
		e.externalDSN = true
		e.dsn = opts.DSN
		return e, nil
	}

	version := opts.Version
	if version == "" {
		version = defaultVersion
	}
	if err := validVersion(version); err != nil {
		return nil, err
	}

	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker not found in PATH: %w", err)
	}

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	e.container = containerPrefix + hex.EncodeToString(suffix)
	image := "postgres:" + version

	// -p 127.0.0.1::5432 asks Docker for a random host port on loopback only.
	args := []string{
		"run", "-d", "--name", e.container,
		"-e", "POSTGRES_PASSWORD=" + password,
		"-e", "POSTGRES_DB=" + dbName,
		"-p", "127.0.0.1::5432",
		image,
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run %s: %w\n%s", image, err, out)
	}

	portOut, err := exec.CommandContext(ctx, "docker", "port", e.container, "5432/tcp").Output()
	if err != nil {
		_ = e.Teardown(ctx)
		return nil, fmt.Errorf("docker port: %w", err)
	}
	// Output looks like "127.0.0.1:49153" (possibly with an IPv6 line too).
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		addr = addr[i+1:]
	}
	e.dsn = fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%s/%s?sslmode=disable", password, addr, dbName)

	if err := e.waitReady(ctx); err != nil {
		_ = e.Teardown(ctx)
		return nil, err
	}
	return e, nil
}

func validVersion(v string) error {
	if v == "latest" {
		return nil
	}
	for _, r := range v {
		if (r < '0' || r > '9') && r != '.' {
			return fmt.Errorf("invalid PG version %q", v)
		}
	}
	return nil
}

// waitReady polls until Postgres accepts connections or ctx expires.
func (e *Engine) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		db, err := e.Connect(ctx)
		if err == nil {
			if err := db.PingContext(ctx); err == nil {
				_ = db.Close()
				return nil
			}
			_ = db.Close()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres did not become ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Connect implements engine.Engine using the pgx stdlib driver.
func (e *Engine) Connect(ctx context.Context) (*sql.DB, error) {
	return sql.Open("pgx", e.dsn)
}

// Dialect implements engine.Engine.
func (e *Engine) Dialect() engine.Dialect { return engine.Postgres }

// Teardown implements engine.Engine.
func (e *Engine) Teardown(ctx context.Context) error {
	if e.externalDSN {
		return nil // never destroy a user-supplied database
	}
	if e.container == "" {
		return nil
	}
	name := e.container
	e.container = ""
	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker rm -f %s: %w\n%s", name, err, out)
	}
	return nil
}

// ensure pgx stdlib driver is linked.
var _ = stdlib.GetDefaultDriver()
