package dws

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// deviceParser recognizes only the exact Chinese presentation in the pinned
// official device_flow.go:dfPrintDeviceAuthorization. Other stderr is discarded.
type deviceParser struct {
	buffer   []byte
	count    int
	code     string
	lifetime time.Duration
	label    bool
	uri      string
	emit     func(deviceChallenge) error
	cancel   context.CancelFunc
	failed   bool
}

func (p *deviceParser) Write(b []byte) (int, error) {
	n := len(b)
	p.count += n
	if p.count > runnerOutputLimit {
		p.failed = true
		p.cancel()
		return n, nil
	}
	p.buffer = append(p.buffer, b...)
	for {
		i := bytes.IndexByte(p.buffer, '\n')
		if i < 0 {
			break
		}
		line := string(p.buffer[:i])
		p.buffer = p.buffer[i+1:]
		if err := p.line(line); err != nil {
			p.failed = true
			p.cancel()
			return n, nil
		}
	}
	return n, nil
}
func officialDeviceURL(raw string) bool {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n\t ") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := u.Hostname()
	return host == "dingtalk.com" || strings.HasSuffix(host, ".dingtalk.com")
}
func (p *deviceParser) line(line string) error {
	if p.label {
		p.label = false
		if !officialDeviceURL(line) || p.code == "" || p.lifetime <= 0 {
			return errors.New("invalid challenge")
		}
		// Complete URL is printed first; a manual fallback must not overwrite it.
		if p.uri == "" {
			p.uri = line
			return p.emit(deviceChallenge{uri: line, code: p.code, lifetime: p.lifetime})
		}
		return nil
	}
	if strings.HasPrefix(line, "  授权码: ") {
		p.code = strings.TrimPrefix(line, "  授权码: ")
		p.uri = ""
		p.lifetime = 0
		if p.code == "" || len(p.code) > 256 || strings.ContainsAny(p.code, "\x00\r\n\x1b") {
			return errors.New("invalid challenge")
		}
		// Invalidate a previous attempt immediately, before the new URL arrives.
		return p.emit(deviceChallenge{code: p.code})
	}
	if strings.HasPrefix(line, "  授权码将在 ") && strings.HasSuffix(line, " 秒后过期。") {
		raw := strings.TrimSuffix(strings.TrimPrefix(line, "  授权码将在 "), " 秒后过期。")
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 || seconds > 86400 {
			return errors.New("invalid expiry")
		}
		p.lifetime = time.Duration(seconds) * time.Second
	}
	if line == "  授权链接（已填入授权码）：" || line == "  手动输入授权码的链接：" {
		p.label = true
	}
	return nil
}

type deviceRunner struct{ binary string }

func (r *deviceRunner) login(ctx context.Context, root string, emit func(deviceChallenge) error) ([]byte, error) {
	fail := errors.New("dws authorization failed")
	env := workspaceEnv(root)
	if env == nil {
		return nil, fail
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var exceeded atomic.Bool
	stdout := &runnerOutput{keep: true, exceeded: &exceeded, cancel: cancel}
	stderr := &deviceParser{emit: emit, cancel: cancel}
	cmd := exec.CommandContext(runCtx, r.binary, "auth", "login", "--device", "--no-browser", "-f", "json")
	cmd.Dir = root
	cmd.Env = append(env, "DWS_LANG=zh", "NO_COLOR=1", "DWS_NO_UPDATE_CHECK=1")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return nil, fail
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	err := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || stderr.failed || exceeded.Load() || stderr.uri == "" {
		clear(stdout.data.Bytes())
		return nil, fail
	}
	return stdout.data.Bytes(), nil
}
