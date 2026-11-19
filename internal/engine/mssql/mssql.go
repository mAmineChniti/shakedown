// Package mssql implements engine.Engine for SQL Server by provisioning a
// throwaway container via the Docker CLI, or by connecting to a
// user-supplied scratch DSN.
package mssql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"github.com/mAmineChniti/shakedown/internal/engine"
)

const (
	defaultVersion  = "2022-latest"
	containerPrefix = "shakedown-"
	saPassword      = "Shakedown!1" // throwaway container credential, never logged
)

// Engine is a SQL Server-backed engine.Engine.
type Engine struct {
	opts        engine.Options
	container   string
	dsn         string
	externalDSN bool
}

// New provisions (or validates) the scratch database.
// Version maps to mcr.microsoft.com/mssql/server:<version>.
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
	image := "mcr.microsoft.com/mssql/server:" + version

	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker not found in PATH: %w", err)
	}

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	e.container = containerPrefix + hex.EncodeToString(suffix)

	args := []string{
		"run", "-d", "--name", e.container,
		"-e", "ACCEPT_EULA=Y",
		"-e", "MSSQL_SA_PASSWORD=" + saPassword,
		"-p", "127.0.0.1::1433",
		image,
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run %s: %w\n%s", image, err, out)
	}

	portOut, err := exec.CommandContext(ctx, "docker", "port", e.container, "1433/tcp").Output()
	if err != nil {
		_ = e.Teardown(ctx)
		return nil, fmt.Errorf("docker port: %w", err)
	}
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		addr = addr[i+1:]
	}
	e.dsn = fmt.Sprintf("sqlserver://sa:%s@127.0.0.1:%s?database=master&encrypt=disable", saPassword, addr)

	if err := e.waitReady(ctx); err != nil {
		_ = e.Teardown(ctx)
		return nil, err
	}
	return e, nil
}

// waitReady polls until SQL Server accepts connections. First boot runs
// setup and can take a couple of minutes.
func (e *Engine) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(300 * time.Second)
	var lastErr error
	for {
		db, err := e.Connect(ctx)
		if err == nil {
			if err := db.PingContext(ctx); err == nil {
				_ = db.Close()
				return nil
			} else {
				lastErr = err
				_ = db.Close()
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sqlserver did not become ready: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Connect implements engine.Engine.
func (e *Engine) Connect(_ context.Context) (*sql.DB, error) {
	return sql.Open("sqlserver", e.dsn)
}

// Dialect implements engine.Engine.
func (e *Engine) Dialect() engine.Dialect { return engine.MSSQL }

// Teardown implements engine.Engine.
func (e *Engine) Teardown(ctx context.Context) error {
	if e.externalDSN {
		return nil
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

func validVersion(v string) error {
	for _, r := range v {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.:-", r) {
			return fmt.Errorf("invalid MSSQL version %q", v)
		}
	}
	return nil
}
