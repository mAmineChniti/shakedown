// Package oracle implements engine.Engine for Oracle Database via the
// pure-Go go-ora driver (no Instant Client needed, binary stays static).
// Provisioning uses the gvenzl/oracle-free Docker image.
package oracle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	_ "github.com/sijms/go-ora/v2"

	"github.com/mAmineChniti/shakedown/internal/engine"
)

const (
	defaultVersion  = "23-slim-faststart"
	containerPrefix = "shakedown-"
	password        = "Shakedown1" // throwaway container credential, never logged
	service         = "FREEPDB1"
)

// Engine is an Oracle-backed engine.Engine.
type Engine struct {
	opts        engine.Options
	container   string
	dsn         string
	externalDSN bool
}

// New provisions (or validates) the scratch database.
// Version maps to gvenzl/oracle-free:<version>.
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
	image := "gvenzl/oracle-free:" + version

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
		"-e", "ORACLE_PASSWORD=" + password,
		"-p", "127.0.0.1::1521",
		image,
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run %s: %w\n%s", image, err, out)
	}

	portOut, err := exec.CommandContext(ctx, "docker", "port", e.container, "1521/tcp").Output()
	if err != nil {
		_ = e.Teardown(ctx)
		return nil, fmt.Errorf("docker port: %w", err)
	}
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		addr = addr[i+1:]
	}
	e.dsn = fmt.Sprintf("oracle://system:%s@127.0.0.1:%s/%s", password, addr, service)

	if err := e.waitReady(ctx); err != nil {
		_ = e.Teardown(ctx)
		return nil, err
	}
	// Connect as a dedicated scratch user so user_tables only contains
	// rehearsal tables (SYSTEM sees Oracle-internal scheduler/mview
	// objects that can't be dropped inside a PDB).
	if err := e.setupUser(ctx); err != nil {
		_ = e.Teardown(ctx)
		return nil, err
	}
	e.dsn = fmt.Sprintf("oracle://%s:%s@127.0.0.1:%s/%s", scratchUser, scratchPass, addr, service)
	return e, nil
}

const (
	scratchUser = "shakedown"
	scratchPass = "shakedown1"
)

// setupUser creates the scratch schema user and grants it the roles a
// migration rehearsal needs.
func (e *Engine) setupUser(ctx context.Context) error {
	db, err := e.Connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		fmt.Sprintf("CREATE USER %s IDENTIFIED BY %s", scratchUser, scratchPass),
		fmt.Sprintf("GRANT CONNECT, RESOURCE, UNLIMITED TABLESPACE TO %s", scratchUser),
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			// User may already exist on a reused scratch DB — tolerate.
			if !strings.Contains(err.Error(), "ORA-01920") && !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("oracle setup: %w", err)
			}
		}
	}
	return nil
}

// waitReady polls until Oracle accepts connections. Database creation in
// the image can take several minutes.
func (e *Engine) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(420 * time.Second)
	var lastErr error
	for {
		db, err := e.Connect(ctx)
		if err == nil {
			if err := db.PingContext(ctx); err == nil {
				_ = db.Close()
				return nil
			}
			_ = db.Close()
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("oracle did not become ready: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Connect implements engine.Engine.
func (e *Engine) Connect(_ context.Context) (*sql.DB, error) {
	return sql.Open("oracle", e.dsn)
}

// Dialect implements engine.Engine.
func (e *Engine) Dialect() engine.Dialect { return engine.Oracle }

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
			return fmt.Errorf("invalid Oracle version %q", v)
		}
	}
	return nil
}
