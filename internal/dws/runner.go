package dws

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	runnerOutputLimit   = 1 << 20
	runnerDownloadLimit = 8 << 20
	tmpfsMagic          = 0x01021994
)

type runner struct {
	binary  string
	timeout time.Duration
}

// workspaceEnv intentionally contains no values inherited from the server.
// A nil environment signals that the private directories could not be prepared.
func workspaceEnv(root string) []string {
	if !filepath.IsAbs(root) || !privateDirectory(root) {
		return nil
	}
	paths := []struct{ key, path string }{
		{"HOME", "home"},
		{"DWS_CONFIG_DIR", "config"},
		{"DWS_KEYCHAIN_DIR", "keychain"},
		{"TMPDIR", "tmp"},
		{"XDG_CONFIG_HOME", "xdg/config"},
		{"XDG_CACHE_HOME", "xdg/cache"},
		{"XDG_DATA_HOME", "xdg/data"},
	}
	env := make([]string, 0, len(paths)+2)
	for _, p := range paths {
		dir := root
		for _, component := range strings.Split(p.path, "/") {
			dir = filepath.Join(dir, component)
			if !privateDirectory(dir) {
				return nil
			}
		}
		env = append(env, p.key+"="+dir)
	}
	return append(env, "PATH=/usr/bin:/bin", "LANG=C.UTF-8")
}

func privateDirectory(path string) bool {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && os.Chmod(path, 0700) == nil
}

// newWorkspace fails closed: credentials must never fall back to disk storage.
func newWorkspace(base string) (string, error) {
	fail := errors.New("dws workspace unavailable")
	if !filepath.IsAbs(base) {
		return "", fail
	}
	base = filepath.Clean(base)
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil || resolved != base {
		return "", fail
	}
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fail
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(base, &stat); err != nil || stat.Type != tmpfsMagic {
		return "", fail
	}
	root, err := os.MkdirTemp(base, "dws-")
	if err != nil {
		return "", fail
	}
	if err := os.Chmod(root, 0700); err != nil {
		_ = os.RemoveAll(root)
		return "", fail
	}
	return root, nil
}

type runnerOutput struct {
	data     bytes.Buffer
	count    int
	keep     bool
	exceeded *atomic.Bool
	cancel   context.CancelFunc
}

func (w *runnerOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := runnerOutputLimit - w.count
	if remaining > n {
		remaining = n
	}
	if w.keep && remaining > 0 {
		_, _ = w.data.Write(p[:remaining])
	}
	w.count += remaining
	if n > remaining {
		w.exceeded.Store(true)
		w.cancel()
	}
	return n, nil
}

func (r runner) execute(ctx context.Context, root string, args []string, download string) ([]byte, error) {
	fail := errors.New("dws command failed")
	if !filepath.IsAbs(r.binary) || r.timeout <= 0 {
		return nil, fail
	}
	env := workspaceEnv(root)
	if env == nil {
		return nil, fail
	}
	var target string
	if download != "" {
		target = download
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return nil, fail
		}
		if err := checkWorkspaceDownloads(root); err != nil {
			return nil, fail
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	var exceeded atomic.Bool
	stdout := &runnerOutput{keep: true, exceeded: &exceeded, cancel: cancel}
	stderr := &runnerOutput{exceeded: &exceeded, cancel: cancel}
	cmd := exec.CommandContext(runCtx, r.binary, args...)
	cmd.Dir, cmd.Env = root, env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return nil, fail
	}
	// Kill descendants even when the group leader exits before its children.
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			// Stop detached descendants before checking the final filesystem state.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if exceeded.Load() {
				return nil, fail
			}
			if runCtx.Err() != nil {
				return nil, runCtx.Err()
			}
			if err != nil {
				return nil, fail
			}
			if target != "" {
				if checkWorkspaceDownloads(root) != nil || !validDownload(root, target) {
					return nil, fail
				}
			}
			return stdout.data.Bytes(), nil
		case <-ticker.C:
			if target != "" && checkWorkspaceDownloads(root) != nil {
				exceeded.Store(true)
				cancel()
			}
		}
	}
}

// WalkDir never follows symlinks; reject them rather than allowing a download
// or its temporary files to escape the workspace. Count aggregate logical size
// so sparse files and multiple temporary fragments are bounded too.
func checkWorkspaceDownloads(root string) error {
	var total int64
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("dws download unavailable")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("dws download unavailable")
		}
		total += info.Size()
		if total > runnerDownloadLimit {
			return errors.New("dws download limit exceeded")
		}
		return nil
	})
}

func validDownload(root, target string) bool {
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || resolved != filepath.Clean(target) {
		return false
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	info, err := os.Lstat(target)
	return err == nil && info.Mode().IsRegular() && info.Size() <= runnerDownloadLimit
}
