// Package mysql implements engine.Engine for MySQL and MariaDB by
// provisioning a throwaway container via the Docker CLI, or by connecting
// to a user-supplied scratch DSN.
package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/mAmineChniti/shakedown/internal/engine"
)

const (
	defaultVersion  = "8.4"
	containerPrefix = "shakedown-"
	dbName          = "shakedown"
	user            = "root"
	password        = "shakedown" // throwaway container credential, never logged
)

// Engine is a MySQL/MariaDB-backed engine.Engine.
type Engine struct {
	opts        engine.Options
	container   string
	dsn         string
	externalDSN bool
}

// New provisions (or validates) the scratch database.
// Version selects the image: "8", "8.4" -> mysql:X; "11", "11.4" -> mariadb:X;
// "mariadb:11.4" or "mysql:8.4" are also accepted verbatim.
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
	image := mysqlImage(version)

	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker not found in PATH: %w", err)
	}

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	e.container = containerPrefix + hex.EncodeToString(suffix)

	// MySQL needs a moment for initdb; the entrypoint creates MYSQL_DATABASE.
	args := []string{
		"run", "-d", "--name", e.container,
		"-e", "MYSQL_ROOT_PASSWORD=" + password,
		"-e", "MYSQL_DATABASE=" + dbName,
		// performance_schema is on by default in MySQL 8 / MariaDB 10.5+;
		// make sure it's explicit so lock sampling works.
		"-p", "127.0.0.1::3306",
		image,
		"--performance-schema=ON",
	}

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run %s: %w\n%s", image, err, out)
	}

	portOut, err := exec.CommandContext(ctx, "docker", "port", e.container, "3306/tcp").Output()
	if err != nil {
		_ = e.Teardown(ctx)
		return nil, fmt.Errorf("docker port: %w", err)
	}
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		addr = addr[i+1:]
	}
	e.dsn = fmt.Sprintf("%s:%s@tcp(127.0.0.1:%s)/%s?parseTime=true", user, password, addr, dbName)

	if err := e.waitReady(ctx); err != nil {
		_ = e.Teardown(ctx)
		return nil, err
	}
	return e, nil
}

// mysqlImage maps a version string to a Docker image.
func mysqlImage(version string) string {
	if strings.HasPrefix(version, "mariadb") {
		v := strings.TrimPrefix(version, "mariadb")
		v = strings.TrimPrefix(v, ":")
		if v == "" {
			v = "11"
		}
		return "mariadb:" + v
	}
	if strings.HasPrefix(version, "mysql") {
		v := strings.TrimPrefix(version, "mysql")
		v = strings.TrimPrefix(v, ":")
		if v == "" {
			v = defaultVersion
		}
		return "mysql:" + v
	}
	// Bare versions: "8", "8.4" -> mysql; "11", "10.x" -> mariadb.
	if strings.HasPrefix(version, "11") || strings.HasPrefix(version, "10.") {
		return "mariadb:" + version
	}
	return "mysql:" + version
}

// waitReady polls until MySQL accepts connections or ctx expires.
// MySQL's entrypoint runs init scripts before accepting connections, so
// the first attempts will fail with "connection refused".
func (e *Engine) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(120 * time.Second)
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
			return fmt.Errorf("mysql did not become ready: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Connect implements engine.Engine.
func (e *Engine) Connect(ctx context.Context) (*sql.DB, error) {
	return sql.Open("mysql", e.dsn)
}

// Dialect implements engine.Engine.
func (e *Engine) Dialect() engine.Dialect { return engine.MySQL }

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
			return fmt.Errorf("invalid MySQL version %q", v)
		}
	}
	return nil
}
