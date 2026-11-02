package dws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func shellFixture(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixtureRunner(t *testing.T, script string) runner {
	t.Helper()
	return runner{binary: shellFixture(t, script), timeout: 3 * time.Second}
}

func TestRunnerEnvironmentIsolation(t *testing.T) {
	t.Setenv("DWS_TEST_SECRET", "must-not-reach-child")
	t.Setenv("DWS_KEYCHAIN_DIR", "/outside/keychain")
	t.Setenv("HTTP_PROXY", "secret-proxy")
	root := t.TempDir()
	r := fixtureRunner(t, `test -z "${DWS_TEST_SECRET+x}"
test -z "${HTTP_PROXY+x}"
test "$HOME" = "$PWD/home"
test "$DWS_CONFIG_DIR" = "$PWD/config"
test "$DWS_KEYCHAIN_DIR" = "$PWD/keychain"
test "$TMPDIR" = "$PWD/tmp"
test "$XDG_CONFIG_HOME" = "$PWD/xdg/config"
test "$XDG_CACHE_HOME" = "$PWD/xdg/cache"
test "$XDG_DATA_HOME" = "$PWD/xdg/data"
test "$PATH" = /usr/bin:/bin
test "$LANG" = C.UTF-8
printf '%s' "$1"`)
	out, err := r.execute(context.Background(), root, []string{"literal;$(false)"}, "")
	if err != nil || string(out) != "literal;$(false)" {
		t.Fatalf("execute = %q, %v", out, err)
	}
	for _, dir := range []string{"", "home", "config", "keychain", "tmp", "xdg", "xdg/config", "xdg/cache", "xdg/data"} {
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("directory %s is not private: %v, %v", dir, info, err)
		}
	}
	if env := workspaceEnv(root); len(env) != 9 {
		t.Fatalf("unexpected environment: %v", env)
	}
}

func TestWorkspaceEnvRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "keychain")); err != nil {
		t.Fatal(err)
	}
	if workspaceEnv(root) != nil {
		t.Fatal("accepted symlink directory")
	}
}

func TestRunnerErrorsHideOutput(t *testing.T) {
	r := fixtureRunner(t, "printf 'stdout-secret'; printf 'stderr-secret' >&2; exit 9")
	out, err := r.execute(context.Background(), t.TempDir(), nil, "")
	if err == nil || out != nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe failure: %q, %v", out, err)
	}
}

func TestRunnerCancellationKillsChildren(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			r := fixtureRunner(t, "sleep 30 &\nprintf '%s' \"$!\" > child.pid\nwait")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "timeout" {
				r.timeout = 100 * time.Millisecond
			}
			done := make(chan error, 1)
			go func() { _, err := r.execute(ctx, root, nil, ""); done <- err }()
			var pid int
			deadline := time.After(time.Second)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for pid == 0 {
				select {
				case <-ticker.C:
					data, err := os.ReadFile(filepath.Join(root, "child.pid"))
					if err == nil {
						pid, _ = strconv.Atoi(string(data))
					}
				case <-deadline:
					t.Fatal("fixture did not start")
				}
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				want := context.Canceled
				if mode == "timeout" {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not return promptly")
			}
			// A killed orphan may remain a zombie until PID 1 reaps it.
			for {
				data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if errors.Is(err, os.ErrNotExist) || (err == nil && strings.Contains(string(data), ") Z ")) {
					break
				}
				select {
				case <-ticker.C:
				case <-deadline:
					t.Fatalf("child %d survived cancellation", pid)
				}
			}
		})
	}
}

func TestRunnerOutputBounds(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, extra := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/%d", stream, extra), func(t *testing.T) {
				script := fmt.Sprintf("head -c %d /dev/zero", runnerOutputLimit+extra)
				if stream == "stderr" {
					script += " >&2"
				}
				r := fixtureRunner(t, script)
				out, err := r.execute(context.Background(), t.TempDir(), nil, "")
				if extra > 0 {
					if err == nil || out != nil {
						t.Fatalf("oversize output accepted: %d, %v", len(out), err)
					}
				} else if err != nil || (stream == "stdout" && len(out) != runnerOutputLimit) {
					t.Fatalf("boundary output rejected: %d, %v", len(out), err)
				}
			})
		}
	}
}

func TestRunnerDownloadBounds(t *testing.T) {
	tests := []struct {
		name, script, target string
		ok                   bool
	}{
		{"boundary", "truncate -s 8388608 result", "result", true},
		{"oversize", "truncate -s 8388609 result", "result", false},
		{"temp", "truncate -s 8388609 tmp/partial; sleep 30", "result", false},
		{"aggregate", "truncate -s 5000000 tmp/one; truncate -s 5000000 tmp/two; sleep 30", "result", false},
		{"symlink", "printf secret > tmp/real; ln -s tmp/real result", "result", false},
		{"directory", "mkdir result", "result", false},
		{"missing", "true", "result", false},
		{"escape", "true", "../result", false},
		{"nested-symlink", "ln -s tmp linked; printf secret > tmp/result", "linked/result", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fixtureRunner(t, tt.script)
			start := time.Now()
			_, err := r.execute(context.Background(), t.TempDir(), nil, tt.target)
			if (err == nil) != tt.ok {
				t.Fatalf("execute error = %v, want success %t", err, tt.ok)
			}
			if time.Since(start) > time.Second {
				t.Fatal("download monitor did not stop process promptly")
			}
		})
	}
}

func TestNewWorkspaceTmpfsPolicy(t *testing.T) {
	if _, err := newWorkspace("relative"); err == nil {
		t.Fatal("accepted relative base")
	}
	if _, err := newWorkspace(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("accepted missing base")
	}
	base := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	if _, err := newWorkspace(link); err == nil {
		t.Fatal("accepted symlink base")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(base, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Type != tmpfsMagic {
		if _, err := newWorkspace(base); err == nil {
			t.Fatal("accepted disk-backed base")
		}
	}
	if err := syscall.Statfs("/dev/shm", &stat); err != nil || stat.Type != tmpfsMagic {
		t.Skip("no test tmpfs available")
	}
	root, err := newWorkspace("/dev/shm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("invalid workspace: %v, %v", info, err)
	}
}
