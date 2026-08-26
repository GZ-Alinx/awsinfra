package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/sensitive"
)

type Command struct {
	Name string
	Args []string
	Dir  string
	Env  []string
	// Stdin carries sensitive manifests without putting their contents in the
	// process argument list or deployment log.
	Stdin io.Reader
}

type Executor struct{}

func (Executor) Run(ctx context.Context, command Command, output io.Writer) error {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...) // #nosec G204 -- deployment commands use administrator-configured binaries and never invoke a shell.
	cmd.Dir = command.Dir
	cmd.Env = mergeEnvironment(os.Environ(), command.Env)
	recentOutput := &boundedTailWriter{limit: 64 * 1024}
	commandOutput := io.MultiWriter(output, recentOutput)
	cmd.Stdout = commandOutput
	cmd.Stderr = commandOutput
	cmd.Stdin = command.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", command.Name, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			excerpt := commandFailureExcerpt(recentOutput.String())
			if excerpt != "" {
				return fmt.Errorf("%s exited with an error: %w\n%s", command.Name, err, excerpt)
			}
			return fmt.Errorf("%s exited with an error: %w", command.Name, err)
		}
		return nil
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-done
			}
		}
		return ctx.Err()
	}
}

// boundedTailWriter retains only recent command output. Terraform and Helm can
// print very large plans, while the actionable provider error is normally at
// the end. The full stream still goes to the redacted deployment log.
type boundedTailWriter struct {
	limit int
	data  []byte
}

func (w *boundedTailWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.limit <= 0 {
		return original, nil
	}
	if len(p) >= w.limit {
		w.data = append(w.data[:0], p[len(p)-w.limit:]...)
		return original, nil
	}
	w.data = append(w.data, p...)
	if overflow := len(w.data) - w.limit; overflow > 0 {
		copy(w.data, w.data[overflow:])
		w.data = w.data[:w.limit]
	}
	return original, nil
}

func (w *boundedTailWriter) String() string { return string(w.data) }

func commandFailureExcerpt(value string) string {
	value = sensitive.RedactText(strings.ReplaceAll(value, "\r\n", "\n"))
	lines := strings.Split(value, "\n")
	start := len(lines) - 48
	if start < 0 {
		start = 0
	}
	// Prefer the first explicit error inside the recent window so two related
	// provider errors (for example Consul and etcd conflicts) stay together.
	for index := start; index < len(lines); index++ {
		lower := strings.ToLower(strings.TrimSpace(lines[index]))
		if strings.HasPrefix(lower, "error:") || strings.Contains(lower, " error: ") {
			start = index
			break
		}
	}
	excerpt := strings.TrimSpace(strings.Join(lines[start:], "\n"))
	if len(excerpt) > 6000 {
		excerpt = excerpt[len(excerpt)-6000:]
	}
	return excerpt
}

// mergeEnvironment gives explicit command entries deterministic precedence
// over inherited values. This is especially important for project-scoped AWS
// credentials: exec environments may otherwise contain duplicate keys whose
// winner differs between consumers.
func mergeEnvironment(base, overrides []string) []string {
	order := make([]string, 0, len(base)+len(overrides))
	values := make(map[string]string, len(base)+len(overrides))
	seen := make(map[string]bool, len(base)+len(overrides))
	for _, item := range append(append([]string{}, base...), overrides...) {
		key, _, ok := strings.Cut(item, "=")
		if key == "" {
			continue
		}
		if !seen[key] {
			seen[key] = true
			order = append(order, key)
		}
		if !ok {
			delete(values, key)
			continue
		}
		values[key] = item
	}
	result := make([]string, 0, len(values))
	for _, key := range order {
		if item, exists := values[key]; exists {
			result = append(result, item)
		}
	}
	return result
}

func CheckTools(names ...string) error {
	missing := make([]string, 0)
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required tools not found: %s", strings.Join(missing, ", "))
	}
	return nil
}

type RedactingWriter struct {
	mu      sync.Mutex
	w       io.Writer
	pending []byte
}

func NewRedactingWriter(w io.Writer) *RedactingWriter {
	return &RedactingWriter{w: w}
}

func (w *RedactingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	originalLen := len(p)
	w.pending = append(w.pending, p...)
	lastNewline := strings.LastIndexByte(string(w.pending), '\n')
	if lastNewline >= 0 {
		if _, err := io.WriteString(w.w, sensitive.RedactText(string(w.pending[:lastNewline+1]))); err != nil {
			return originalLen, err
		}
		w.pending = append(w.pending[:0], w.pending[lastNewline+1:]...)
	}
	if len(w.pending) > 1<<20 {
		_, err := io.WriteString(w.w, sensitive.RedactText(string(w.pending)))
		w.pending = w.pending[:0]
		return originalLen, err
	}
	return originalLen, nil
}

func (w *RedactingWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		return nil
	}
	_, err := io.WriteString(w.w, sensitive.RedactText(string(w.pending)))
	w.pending = w.pending[:0]
	return err
}

func IsCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
