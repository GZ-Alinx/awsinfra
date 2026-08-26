package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ops-deploy-platform/internal/sensitive"
)

type Action string

const (
	ActionValidate      Action = "validate"
	ActionPlan          Action = "plan"
	ActionDeploy        Action = "deploy"
	ActionPlatform      Action = "platform"
	ActionAccess        Action = "access"
	ActionTLS           Action = "tls"
	ActionStorageExpand Action = "storage_expand"
	ActionStorageShrink Action = "storage_shrink"
	ActionDestroy       Action = "destroy"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
	StatusIgnored   Status = "ignored"
)

// CompletionAction describes an operation that must run only after the task
// runner has completed successfully. It is persisted with the job so retries
// and manager restarts cannot silently drop a requested lifecycle operation.
type CompletionAction string

const (
	CompletionDeleteEnvironment CompletionAction = "delete_environment"
)

type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
)

type Step struct {
	Name       string     `json:"name"`
	Status     StepStatus `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type Job struct {
	ID               string           `json:"id"`
	Project          string           `json:"project"`
	Environment      string           `json:"environment"`
	TargetName       string           `json:"target_name,omitempty"`
	RequestedBy      string           `json:"requested_by"`
	Action           Action           `json:"action"`
	Status           Status           `json:"status"`
	CreatedAt        time.Time        `json:"created_at"`
	StartedAt        *time.Time       `json:"started_at,omitempty"`
	FinishedAt       *time.Time       `json:"finished_at,omitempty"`
	Error            string           `json:"error,omitempty"`
	FailureHint      string           `json:"failure_hint,omitempty"`
	Diagnosis        *Diagnosis       `json:"diagnosis,omitempty"`
	IgnoredAt        *time.Time       `json:"ignored_at,omitempty"`
	IgnoredBy        string           `json:"ignored_by,omitempty"`
	IgnoreReason     string           `json:"ignore_reason,omitempty"`
	LogSize          int64            `json:"log_size"`
	Progress         int              `json:"progress"`
	TotalSteps       int              `json:"total_steps"`
	SuccessSteps     int              `json:"success_steps"`
	FailedSteps      int              `json:"failed_steps"`
	CurrentStep      string           `json:"current_step,omitempty"`
	Steps            []Step           `json:"steps"`
	CompletionAction CompletionAction `json:"completion_action,omitempty"`
	// Parameters contains allowlisted, non-secret operation input. Deployment
	// and storage task handlers must derive Kubernetes object names from the
	// environment document instead of accepting arbitrary names from clients.
	Parameters map[string]string `json:"parameters,omitempty"`
}

// Diagnosis is the operator-facing explanation of a failed task. Error keeps
// the technical evidence; Diagnosis answers what failed, what was affected,
// and what must happen before retrying.
type Diagnosis struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	Stage      string `json:"stage"`
	Cause      string `json:"cause"`
	Impact     string `json:"impact"`
	Suggestion string `json:"suggestion"`
	Retry      string `json:"retry"`
}

type progressUpdate struct {
	Kind  string
	Names []string
	Name  string
	Err   error
}

type progressContextKey struct{}

func withProgress(ctx context.Context, reporter func(progressUpdate)) context.Context {
	return context.WithValue(ctx, progressContextKey{}, reporter)
}

// SetSteps declares the stable deployment plan before commands start so the UI
// can show a meaningful percentage from the first command onward.
func SetSteps(ctx context.Context, names []string) {
	if reporter, ok := ctx.Value(progressContextKey{}).(func(progressUpdate)); ok {
		reporter(progressUpdate{Kind: "plan", Names: append([]string(nil), names...)})
	}
}

func StepStarted(ctx context.Context, name string) {
	if reporter, ok := ctx.Value(progressContextKey{}).(func(progressUpdate)); ok {
		reporter(progressUpdate{Kind: "start", Name: name})
	}
}

func StepFinished(ctx context.Context, name string, err error) {
	if reporter, ok := ctx.Value(progressContextKey{}).(func(progressUpdate)); ok {
		reporter(progressUpdate{Kind: "finish", Name: name, Err: err})
	}
}

type TaskRunner interface {
	Run(ctx context.Context, environment string, action Action, jobID string, output io.Writer) error
}

type Store interface {
	LoadJobs(context.Context) ([]Job, error)
	SaveJob(context.Context, *Job) error
	DeleteJob(context.Context, string) error
}

type RealtimeStore interface {
	CacheJob(context.Context, *Job) error
	DeleteCachedJob(context.Context, string, string, string) error
}

type Manager struct {
	dir               string
	historyLimit      int
	timeout           time.Duration
	runner            TaskRunner
	store             Store
	realtime          RealtimeStore
	queue             chan string
	completionHandler func(context.Context, Job) error

	mu      sync.RWMutex
	jobs    map[string]*Job
	cancels map[string]context.CancelFunc
}

func NewManager(dir string, maxParallel, historyLimit int, timeout time.Duration, taskRunner TaskRunner) (*Manager, error) {
	return NewManagerWithStores(dir, maxParallel, historyLimit, timeout, taskRunner, nil, nil)
}

func NewManagerWithStores(dir string, maxParallel, historyLimit int, timeout time.Duration, taskRunner TaskRunner, store Store, realtime RealtimeStore) (*Manager, error) {
	if maxParallel < 1 {
		maxParallel = 1
	}
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o700); err != nil {
		return nil, err
	}
	manager := &Manager{
		dir:          filepath.Join(dir, "jobs"),
		historyLimit: historyLimit,
		timeout:      timeout,
		runner:       taskRunner,
		store:        store,
		realtime:     realtime,
		queue:        make(chan string, historyLimit+maxParallel+10),
		jobs:         make(map[string]*Job),
		cancels:      make(map[string]context.CancelFunc),
	}
	if err := manager.checkStorageDirectories([]string{manager.dir}); err != nil {
		return nil, err
	}
	if err := manager.load(); err != nil {
		return nil, err
	}
	for i := 0; i < maxParallel; i++ {
		go manager.worker()
	}
	return manager, nil
}

func (m *Manager) Submit(project, environment, targetName, requestedBy string, action Action) (*Job, error) {
	return m.SubmitWithCompletion(project, environment, targetName, requestedBy, action, "")
}

// SubmitWithParameters queues a task with immutable, non-secret input. It is
// used by narrowly scoped operations such as managed PVC resizing; ordinary
// deployments continue to use Submit.
func (m *Manager) SubmitWithParameters(project, environment, targetName, requestedBy string, action Action, parameters map[string]string) (*Job, error) {
	job, err := m.submit(project, environment, targetName, requestedBy, action, "", parameters)
	return job, err
}

// SubmitWithCompletion queues a deployment task and records the lifecycle
// action that must run after the runner succeeds.
func (m *Manager) SubmitWithCompletion(project, environment, targetName, requestedBy string, action Action, completion CompletionAction) (*Job, error) {
	return m.submit(project, environment, targetName, requestedBy, action, completion, nil)
}

func (m *Manager) submit(project, environment, targetName, requestedBy string, action Action, completion CompletionAction, parameters map[string]string) (*Job, error) {
	if !validAction(action) {
		return nil, ErrInvalidAction
	}
	if completion != "" && completion != CompletionDeleteEnvironment {
		return nil, ErrInvalidCompletionAction
	}
	if completion == CompletionDeleteEnvironment && action != ActionDestroy {
		return nil, ErrInvalidCompletionAction
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	job := &Job{
		ID:               id,
		Project:          project,
		Environment:      environment,
		TargetName:       targetName,
		RequestedBy:      requestedBy,
		Action:           action,
		Status:           StatusQueued,
		CreatedAt:        time.Now().UTC(),
		Steps:            make([]Step, 0),
		CompletionAction: completion,
		Parameters:       cloneStringMap(parameters),
	}
	// Validate the exact project/environment directory before accepting the
	// task. With a database-backed Store, metadata persistence can succeed even
	// when the PVC used for deployment logs is not writable; accepting the job
	// in that state previously produced a zero-step failure and an HTTP 500 when
	// the UI attempted to read its log.
	if err := m.prepareJobStorage(job); err != nil {
		return nil, err
	}
	m.mu.Lock()
	for _, existing := range m.jobs {
		if existing.Project == project && existing.Environment == environment && (existing.Status == StatusQueued || existing.Status == StatusRunning) {
			m.mu.Unlock()
			return nil, ErrEnvironmentBusy
		}
	}
	m.jobs[id] = job
	err = m.persistLocked(job)
	if err != nil {
		delete(m.jobs, id)
	}
	copy := cloneJob(job)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	m.queue <- id
	return &copy, nil
}

// SetCompletionHandler installs the application lifecycle callback. The
// handler is read under the manager lock so wiring remains race-safe after
// worker goroutines have started.
func (m *Manager) SetCompletionHandler(handler func(context.Context, Job) error) {
	m.mu.Lock()
	m.completionHandler = handler
	m.mu.Unlock()
}

func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.RLock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.RUnlock()
		return nil, false
	}
	copy := cloneJob(job)
	m.mu.RUnlock()
	if info, err := os.Stat(m.logPath(&copy)); err == nil {
		copy.LogSize = info.Size()
	}
	return &copy, true
}

func (m *Manager) List(project, environment string) []Job {
	m.mu.RLock()
	result := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		if project != "" && job.Project != project {
			continue
		}
		if environment != "" && job.Environment != environment {
			continue
		}
		copy := cloneJob(job)
		result = append(result, copy)
	}
	m.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if m.historyLimit > 0 && len(result) > m.historyLimit {
		result = result[:m.historyLimit]
	}
	for index := range result {
		if info, err := os.Stat(m.logPath(&result[index])); err == nil {
			result[index].LogSize = info.Size()
		}
	}
	return result
}

// Snapshot returns immutable task metadata without touching log files. It is
// intended for aggregate views such as project lifecycle badges, where
// repeatedly scanning every task and stat-ing every log for each environment
// would make page latency grow quadratically. The existing per-environment
// history limit is preserved so lifecycle derivation has the same semantics as
// calling List(project, environment) for each scope.
func (m *Manager) Snapshot() []Job {
	m.mu.RLock()
	result := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		result = append(result, cloneJob(job))
	}
	m.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if m.historyLimit > 0 {
		filtered := make([]Job, 0, min(len(result), m.historyLimit))
		counts := make(map[string]int)
		for _, job := range result {
			key := job.Project + "\x00" + job.Environment
			if counts[key] >= m.historyLimit {
				continue
			}
			counts[key]++
			filtered = append(filtered, job)
		}
		result = filtered
	}
	return result
}

func (m *Manager) HasActiveEnvironment(project, environment string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, job := range m.jobs {
		if job.Project == project && job.Environment == environment && (job.Status == StatusQueued || job.Status == StatusRunning) {
			return true
		}
	}
	return false
}

func (m *Manager) HasActiveProject(project string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, job := range m.jobs {
		if job.Project == project && (job.Status == StatusQueued || job.Status == StatusRunning) {
			return true
		}
	}
	return false
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return os.ErrNotExist
	}
	switch job.Status {
	case StatusQueued:
		now := time.Now().UTC()
		job.Status = StatusCanceled
		job.FinishedAt = &now
		return m.persistLocked(job)
	case StatusRunning:
		if cancel, ok := m.cancels[id]; ok {
			cancel()
		}
		return nil
	default:
		return ErrNotCancelable
	}
}

// Ignore acknowledges a finished non-destroy failure without pretending that
// the failed deployment step succeeded. The technical error and failed steps
// remain available for audit and a later retry.
func (m *Manager) Ignore(id, ignoredBy, reason string) (*Job, error) {
	ignoredBy = strings.TrimSpace(ignoredBy)
	reason = strings.TrimSpace(reason)
	reasonLength := utf8.RuneCountInString(reason)
	if ignoredBy == "" || reasonLength < 3 || reasonLength > 500 {
		return nil, ErrInvalidIgnoreReason
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	if job.Status != StatusFailed && job.Status != StatusCanceled {
		return nil, ErrNotIgnorable
	}
	if job.Action == ActionDestroy {
		return nil, ErrDestroyNotIgnorable
	}
	for _, existing := range m.jobs {
		if existing.Project == job.Project && existing.Environment == job.Environment && (existing.Status == StatusQueued || existing.Status == StatusRunning) {
			return nil, ErrEnvironmentBusy
		}
	}
	now := time.Now().UTC()
	job.Status = StatusIgnored
	job.IgnoredAt = &now
	job.IgnoredBy = ignoredBy
	job.IgnoreReason = reason
	if err := m.persistLocked(job); err != nil {
		return nil, err
	}
	if logFile, err := os.OpenFile(m.logPath(job), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		_, _ = fmt.Fprintf(logFile, "[%s] failure acknowledged by %s: %s\n", now.Format(time.RFC3339), safeSegment(ignoredBy, "operator"), sensitive.RedactText(reason))
		_ = logFile.Close()
	}
	copy := cloneJob(job)
	return &copy, nil
}

func cloneJob(job *Job) Job {
	copy := *job
	copy.Steps = append([]Step(nil), job.Steps...)
	copy.Parameters = cloneStringMap(job.Parameters)
	if job.Diagnosis != nil {
		diagnosis := *job.Diagnosis
		copy.Diagnosis = &diagnosis
	}
	return copy
}

func (m *Manager) ReadLog(id string, offset int64, limit int64) ([]byte, int64, bool, error) {
	job, ok := m.Get(id)
	if !ok {
		return nil, offset, false, os.ErrNotExist
	}
	file, err := os.Open(m.logPath(job))
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, offset, isTerminal(job.Status), nil
	}
	if err != nil {
		return nil, offset, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, offset, false, err
	}
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	if limit <= 0 || limit > 256*1024 {
		limit = 256 * 1024
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, false, err
	}
	buf := make([]byte, limit)
	n, err := file.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, offset, false, err
	}
	return buf[:n], offset + int64(n), isTerminal(job.Status), nil
}

func (m *Manager) Wait(ctx context.Context, id string) (*Job, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, ok := m.Get(id)
		if !ok {
			return nil, os.ErrNotExist
		}
		if isTerminal(job.Status) {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) worker() {
	for id := range m.queue {
		m.run(id)
	}
}

func (m *Manager) run(id string) {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok || job.Status != StatusQueued {
		m.mu.Unlock()
		return
	}
	started := time.Now().UTC()
	job.Status = StatusRunning
	job.StartedAt = &started
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	ctx = withProgress(ctx, func(update progressUpdate) { m.updateProgress(id, update) })
	m.cancels[id] = cancel
	_ = m.persistLocked(job)
	m.mu.Unlock()

	var err error
	if storageErr := m.prepareJobStorage(job); storageErr != nil {
		err = storageErr
	}
	var logFile *os.File
	if err == nil {
		logFile, err = os.OpenFile(m.logPath(job), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			err = wrapLogStorageError("open task log", err)
		}
		if err == nil {
			if chmodErr := logFile.Chmod(0o600); chmodErr != nil {
				err = wrapLogStorageError("secure task log", chmodErr)
			}
		}
	}
	if err == nil {
		_, _ = fmt.Fprintf(logFile, "[%s] project=%s environment=%s requested_by=%s job=%s action=%s target=%s started\n",
			started.Format(time.RFC3339), job.Project, job.Environment, job.RequestedBy, id, job.Action, job.TargetName)
		if detailed, ok := m.runner.(DetailedTaskRunner); ok {
			err = detailed.RunJob(ctx, cloneJob(job), logFile)
		} else {
			err = m.runner.Run(ctx, job.TargetName, job.Action, job.ID, logFile)
		}
	}
	ctxErr := ctx.Err()
	if err == nil && ctxErr == nil && job.CompletionAction != "" {
		m.mu.RLock()
		handler := m.completionHandler
		m.mu.RUnlock()
		if handler == nil {
			err = errors.New("任务已执行成功，但平台未配置后续环境清理处理器")
		} else if completionErr := handler(ctx, *job); completionErr != nil {
			err = fmt.Errorf("资源销毁已完成，但删除环境配置失败: %w", completionErr)
		} else if logFile != nil {
			_, _ = fmt.Fprintln(logFile, "资源销毁已完成，环境配置已自动删除。")
		}
		ctxErr = ctx.Err()
	}
	if logFile != nil {
		finishedMessage := "completed"
		if err != nil {
			finishedMessage = "failed: " + sensitive.RedactText(err.Error())
		}
		_, _ = fmt.Fprintf(logFile, "[%s] job %s %s\n", time.Now().UTC().Format(time.RFC3339), id, finishedMessage)
		_ = logFile.Close()
	}
	cancel()

	m.mu.Lock()
	finished := time.Now().UTC()
	job.FinishedAt = &finished
	delete(m.cancels, id)
	switch {
	case errors.Is(ctxErr, context.Canceled):
		job.Status = StatusCanceled
		job.Error = "canceled"
	case errors.Is(ctxErr, context.DeadlineExceeded):
		job.Status = StatusFailed
		job.Error = "job timeout exceeded"
	case err != nil:
		job.Status = StatusFailed
		job.Error = sensitive.RedactText(err.Error())
	default:
		job.Status = StatusSucceeded
		job.Error = ""
		job.FailureHint = ""
		job.Diagnosis = nil
		normalizeSucceededSteps(job, finished)
	}
	if job.Status == StatusFailed {
		m.diagnoseFailedJob(job)
		_ = m.appendDiagnosisLog(job)
	}
	_ = m.persistLocked(job)
	m.pruneLocked()
	m.mu.Unlock()
}

func failureHint(job *Job, message string) string {
	diagnosis := failureDiagnosis(job, message)
	return strings.TrimSpace(diagnosis.Cause + " " + diagnosis.Suggestion + " " + diagnosis.Retry)
}

func failureDiagnosis(job *Job, message string) Diagnosis {
	text := strings.ToLower(message)
	failedStep := ""
	for _, step := range job.Steps {
		if step.Status == StepFailed {
			failedStep = step.Name
			break
		}
	}
	failedStepText := strings.ToLower(failedStep)
	stage := displayStepName(failedStep)
	if stage == "" {
		stage = actionDisplayName(job.Action)
	}
	base := Diagnosis{Code: "unknown", Title: "任务未完成", Stage: stage}
	switch {
	case job.Action == ActionStorageExpand && (strings.Contains(text, "不允许在线扩容") || strings.Contains(text, "does not allow volume expansion")):
		base.Code, base.Title = "storage_class_expansion_disabled", "当前存储类不支持在线扩容"
		base.Cause = "目标 PVC 使用的 StorageClass 没有启用 allowVolumeExpansion，Kubernetes 因此拒绝在线增大容量。"
		base.Impact = "原 PVC、数据和工作负载均保持不变，没有执行缩容迁移或删除数据卷。"
		base.Suggestion = "改用支持 EBS CSI 在线扩容的 StorageClass，或由平台管理员为该 StorageClass 启用扩容能力。"
		base.Retry = "确认存储类允许扩容后，可直接重试同一任务。"
		return base
	case job.Action == ActionStorageShrink && (strings.Contains(text, "目标容量不足") || strings.Contains(text, "安全余量后需要")):
		base.Code, base.Title = "storage_shrink_target_too_small", "缩容目标无法容纳现有数据"
		base.Cause = "平台实测源 PVC 已用空间并加上安全余量后，超过或接近目标 PVC 容量，因此在切换前主动停止。"
		base.Impact = "原 PVC 没有切换或删除；目标子组件已按原副本数恢复，新建的小容量 PVC 会保留用于排查。"
		base.Suggestion = "提高缩容目标容量，或先按业务规则清理历史日志和数据，再刷新容量后重新提交。"
		base.Retry = "目标容量大于已用数据加安全余量后可重试；不需要删除原 PVC。"
		return base
	case job.Action == ActionStorageShrink && strings.Contains(text, "数据迁移失败"):
		base.Code, base.Title = "storage_migration_failed", "PVC 安全迁移未完成"
		base.Cause = "源 PVC 到新 PVC 的离线复制或复制后容量校验失败，平台没有执行最终存储切换。"
		base.Impact = "原 PVC 和原数据完整保留，目标子组件已尝试恢复；新 PVC 与迁移 Pod 会保留，便于查看日志。"
		base.Suggestion = "查看迁移 Pod 日志和事件，检查 EBS 挂载、节点可用区、磁盘空间及文件权限。"
		base.Retry = "修复挂载或容量问题后重试；平台会重新发现当前活动 PVC，不需要删除原数据卷。"
		return base
	case job.Action == ActionStorageShrink && strings.Contains(text, "已尝试回滚原 pvc"):
		base.Code, base.Title = "storage_switch_rollback", "新 PVC 验证失败，平台已回滚"
		base.Cause = "数据复制成功后，工作负载切换到新 PVC 未能在限定时间内恢复健康，平台已尝试重新绑定原 PVC。"
		base.Impact = "原 PVC 保留并作为回滚目标；新 PVC 也保留，不会自动删除任何业务数据。"
		base.Suggestion = "先检查工作负载事件、容器日志和原 PVC 是否已重新挂载，确认服务恢复后再分析新盘权限或数据一致性。"
		base.Retry = "只有在原工作负载恢复健康且新盘问题明确修复后再重试缩容。"
		return base
	case strings.Contains(text, "deployment_phase") && strings.Contains(text, "must be base"):
		base.Code, base.Title = "platform_access_phase_contract_mismatch", "平台接入阶段与 Terraform 版本不匹配"
		base.Cause = "平台已提交 access 接入配置动作，但当前 Terraform 模块的变量校验尚未接受 access，任务在资源计划和变更前被拒绝。"
		base.Impact = "Terraform 没有进入资源操作，本次任务不会创建、修改或删除组件、TLS、域名、告警或云资源。"
		base.Suggestion = "更新运维自动部署平台及其内置 Terraform 模块，确保 access 同时被变量校验和阶段二资源计算识别。"
		base.Retry = "平台发布包含 access 阶段契约修复的版本后，可直接使用“仅应用接入配置”重试，不需要清理 State、Helm Release 或 PVC。"
		return base
	case strings.Contains(text, "job log storage unavailable"),
		strings.Contains(text, "permission denied") && strings.Contains(text, "/data/jobs"):
		base.Code, base.Title = "job_log_storage_unwritable", "任务日志存储不可写"
		base.Cause = "平台运行卷中的任务日志目录无法创建、读取或写入，任务在执行 Terraform、Helm 或 kubectl 之前已停止。"
		base.Impact = "本次任务没有进入任何部署步骤，不会创建、修改或删除 AWS、EKS、组件、TLS 和域名资源。"
		base.Suggestion = "平台管理员需要检查运行 PVC 的挂载状态，并将 data/jobs 目录及其子目录修正为平台运行用户 10001:10001、目录 0700、日志 0600。"
		base.Retry = "运行卷健康检查恢复为正常后可直接重试；不需要清理 Terraform State、Helm Release 或 PVC。"
		return base
	case strings.Contains(text, "资源销毁已完成，但删除环境配置失败"):
		base.Code, base.Title = "environment_cleanup_after_destroy_failed", "资源已销毁，环境配置未删除"
		base.Cause = "销毁命令已成功，但平台在确认 Terraform 状态或删除环境记录时失败。"
		base.Impact = "云资源不会因此重建；环境配置保留，避免在状态未确认时丢失管理入口。"
		base.Suggestion = "查看日志中 Terraform 剩余资源数或数据库错误；先修复状态中心或平台数据库连接。"
		base.Retry = "修复后直接重试该任务；销毁流程幂等，只会继续清理剩余资源并再次尝试删除环境。"
		return base
	case strings.Contains(text, "统一 terraform 状态中心尚未配置"), strings.Contains(text, "统一 terraform 状态中心不可用") && strings.Contains(text, "尚未配置"):
		base.Code, base.Title = "terraform_state_center_not_configured", "统一 Terraform 状态中心尚未配置"
		base.Cause = "平台没有可用的统一 S3 state bucket 与独立状态凭据，因此按安全策略拒绝回退到本机或项目账号 state。"
		base.Impact = "任务在 Terraform 初始化前停止，没有创建、修改或销毁任何 AWS、EKS 和组件资源。"
		base.Suggestion = "请让平台管理员进入“平台管理 / Terraform 状态中心”，填写 S3 bucket、Region、路径前缀和状态中心凭据并完成连接验证。"
		base.Retry = "状态中心显示“已启用”后直接重试；首次运行会自动对账并迁移旧 state。"
		return base
	case strings.Contains(text, "failed to update dependency lock file"),
		strings.Contains(text, ".terraform.lock.hcl") && strings.Contains(text, "read-only file system"):
		base.Code, base.Title = "terraform_lockfile_readonly", "Terraform 依赖锁文件与只读运行容器冲突"
		base.Cause = "平台容器启用了只读根文件系统，但 Terraform init 尝试在镜像内更新 .terraform.lock.hcl。"
		base.Impact = "任务停在 Terraform 初始化阶段，组件、TLS Secret 和后续检查尚未执行；AWS 与 EKS 资源没有被本步骤修改。"
		base.Suggestion = "平台运行时应使用 -lockfile=readonly，并在构建镜像前统一更新和审核 Provider 锁文件。"
		base.Retry = "发布包含该修复的平台版本后可直接重试，不需要删除 Helm Release、PVC 或 Terraform State。"
		return base
	case strings.Contains(text, "prevent_destroy") && strings.Contains(text, "kubernetes_namespace"):
		base.Code, base.Title = "namespace_deletion_protected", "Namespace 永久删除保护已拦截操作"
		base.Cause = "当前配置或 Terraform 操作试图删除 Kubernetes Namespace。删除 Namespace 会级联删除其中全部工作负载、Service、Secret、Ingress 和 PVC，因此平台已强制停止。"
		base.Impact = "Namespace 及其中资源均未被平台删除；其他组件变更也不会继续执行。"
		base.Suggestion = "恢复该 Namespace 的配置，只关闭需要卸载的组件开关。平台会删除对应 Helm Release 与关联资源，但永久保留 Namespace。"
		base.Retry = "恢复 Namespace 配置并确认计划中没有 kubernetes_namespace 删除项后，再重试对应阶段。不要移除 prevent_destroy 或手工删除 Namespace。"
		return base
	case strings.Contains(text, "namespaces \"") && strings.Contains(text, "\" already exists"):
		base.Code, base.Title = "namespace_exists_outside_state", "已有 Namespace 尚未纳入当前环境 State"
		base.Cause = "目标 Namespace 已经存在于 EKS，但当前环境的 Terraform State 尚未记录它，因此旧版平台重复执行创建并被 Kubernetes 拒绝。"
		base.Impact = "已有 Namespace 及其中的工作负载、Service、Secret、Ingress 和 PVC 均未被删除；本次阶段 2 在安装后续组件前停止。"
		base.Suggestion = "新版平台会在阶段 2 开始前读取 EKS 与 Terraform State：归属为空或属于当前项目环境时自动复用并导入 State，归属冲突时停止并提示改用独立 Namespace。"
		base.Retry = "升级平台后可直接重试阶段 2；无需手工删除 Namespace、组件、PVC 或 Terraform State。日志会明确显示“已安全复用并跳过重复创建”。"
		return base
	case strings.Contains(text, "namespace") && strings.Contains(text, "归属于其他项目或环境"):
		base.Code, base.Title = "namespace_ownership_conflict", "Namespace 归属与当前项目环境冲突"
		base.Cause = "平台发现目标 Namespace 已由其他项目或环境标记，已拒绝自动导入，避免两个 Terraform State 同时管理同一 Namespace。"
		base.Impact = "冲突 Namespace 和其中全部资源保持不变；当前阶段尚未继续安装依赖它的组件。"
		base.Suggestion = "在组件参数中选择当前项目环境专属 Namespace，或先由负责人完成现有 Namespace 的归属和资源清点；不要直接删除或改标签规避校验。"
		base.Retry = "保存无冲突的 Namespace 后直接重试阶段 2。"
		return base
	case strings.Contains(text, "复用已有 namespace") && strings.Contains(text, "导入 terraform state 失败"):
		base.Code, base.Title = "namespace_state_adoption_failed", "已有 Namespace 自动纳管失败"
		base.Cause = "Namespace 归属校验已通过，但 Terraform import 未能写入当前环境的统一 State。"
		base.Impact = "Namespace 及其中资源没有被修改或删除；组件安装在 State 纳管成功前不会继续。"
		base.Suggestion = "检查统一 Terraform 状态中心、State 锁和当前 EKS 身份的 Namespace 读取权限；不要手工删除 Namespace。"
		base.Retry = "修复状态中心或锁后直接重试阶段 2，平台会再次安全纳管。"
		return base
	case strings.Contains(text, "required plugins are not installed") && strings.Contains(text, "does not match any of the checksums recorded"):
		base.Code, base.Title = "terraform_provider_platform_checksum_missing", "Terraform Provider 缺少 Linux 平台校验和"
		base.Cause = "依赖锁文件只记录了开发机平台的 Provider 校验和，Linux 部署容器下载的相同版本 Provider 因校验和未登记而被 Terraform 拒绝。"
		base.Impact = "任务在选择 Terraform workspace 前停止，本次重试尚未修改或销毁任何 AWS、EKS、Helm 与 PVC 资源；原 state 仍完整保留。"
		base.Suggestion = "在构建前对 infra 与 platform 执行 terraform providers lock -platform=darwin_arm64 -platform=linux_amd64，并随镜像发布更新后的锁文件。"
		base.Retry = "发布包含 Linux Provider 校验和的镜像后可直接重试，不需要删除 state 或云资源。"
		return base
	case strings.Contains(text, "prepare s3 remote state"), strings.Contains(text, "terraform state bucket"), strings.Contains(text, "state center"), strings.Contains(text, "状态中心 s3"):
		base.Code, base.Title = "terraform_remote_state_unavailable", "Terraform S3 远程状态存储不可用"
		base.Cause = "统一状态中心身份无法读取或写入 S3 state，或 S3 后端初始化失败。"
		base.Impact = "平台在 Terraform 资源操作前停止，本地旧 state 不会被删除，S3 中已有 state 也不会被覆盖。"
		base.Suggestion = "在 Terraform 状态中心重新验证 bucket、Region、版本化、加密、公共访问阻止和对象权限；项目凭据不负责统一 state。"
		base.Retry = "S3 权限或配置修复后直接重试；平台会继续自动迁移并校验 state lineage。"
		return base
	case strings.Contains(text, "states contain resources but their lineages differ"), strings.Contains(text, "automatic overwrite was blocked"):
		base.Code, base.Title = "terraform_state_lineage_conflict", "本地与 S3 Terraform State 冲突"
		base.Cause = "同一环境的本地 state 与 S3 state 都含资源，但 lineage 或 serial 无法证明它们来自同一条状态历史。"
		base.Impact = "平台已阻止自动覆盖，两个 state 都被保留，没有继续执行 apply 或 destroy。"
		base.Suggestion = "在 Terraform 状态中心对比 bucket、workspace、lineage、serial 和资源清单，确认权威副本后再人工迁移。"
		base.Retry = "完成 state 对账并确保 S3 是唯一权威副本后再重试。"
		return base
	case strings.Contains(text, "tls 证书") && (strings.Contains(text, "加密材料不可用") || strings.Contains(text, "certificate material is not configured")):
		base.Code, base.Title = "tls_material_missing", "TLS 证书材料尚未配置或无法解密"
		base.Cause = "环境配置选择了“直接粘贴证书”，但平台没有找到该证书对应的加密证书链和私钥，或加密材料已失效。"
		base.Impact = "TLS Secret 未创建或更新；平台不会使用空证书，已有组件和云资源会保留。"
		base.Suggestion = "进入部署配置的 TLS 证书页面，编辑对应证书并重新粘贴完整证书链和匹配的未加密私钥。"
		base.Retry = "页面显示“证书已加密保存”后，可直接重试当前 TLS 任务。"
		return base
	case strings.Contains(text, "tls 目标 namespace"):
		base.Code, base.Title = "tls_namespace_prepare_failed", "TLS 目标 Namespace 自动准备失败"
		base.Cause = "平台发现证书目标 Namespace 不存在，已在创建 Secret 前停止；自动纳入统一 Terraform State 或读取 Namespace 时未能完成。"
		base.Impact = "TLS Secret 尚未创建，其他组件、已有 Namespace 和云资源均未被本步骤修改；不会留下未纳入 State 的 Namespace。"
		base.Suggestion = "确认该 Namespace 已加入当前环境的 Namespaces 配置，并检查统一 State 中心及 EKS 身份的 Namespace get/create 权限。"
		base.Retry = "配置或权限修复后直接重试 TLS 任务；平台会先创建并记录 Namespace，再应用证书。"
		return base
	case strings.Contains(text, "应用 tls secret") || strings.Contains(failedStepText, "kubernetes tls secret"):
		base.Code, base.Title = "tls_secret_apply_failed", "Kubernetes TLS Secret 创建或更新失败"
		base.Cause = "证书材料已通过平台校验，但 kubectl 无法在目标 Namespace 创建或更新 kubernetes.io/tls Secret。"
		base.Impact = "该证书对应的 HTTPS 在 Secret 可用前不会正常工作；其他组件未被重装，证书正文和私钥也未写入部署日志。"
		base.Suggestion = "目标 Namespace 已通过前置检查；请检查当前 EKS 身份是否具有 Secret 的 get、create、patch 权限。"
		base.Retry = "修复 Kubernetes Secret RBAC 后可直接重试当前 TLS 任务。"
		return base
	case strings.Contains(text, "清理首次安装中断的 pvc 失败"), strings.Contains(text, "无法在首次安装重试前读取 pvc 清单"):
		base.Code, base.Title = "fresh_install_pvc_cleanup_failed", "首次安装留下的 PVC 未能安全清理"
		base.Cause = "平台已确认组件未进入 Terraform State 且处于 pending-install，但读取或删除该组件自己的新建 PVC 时失败。"
		base.Impact = "平台已停止继续安装，不会误用一块未确认清理完成的首次安装数据卷。"
		base.Suggestion = "检查 Namespace 中对应 data-<release>-<序号> PVC 的 finalizer、挂载 Pod 和 Kubernetes 删除权限。"
		base.Retry = "确认该首次安装 PVC 已删除或可正常删除后重试。"
		return base
	case strings.Contains(text, "cannot patch") && strings.Contains(text, "statefulset") && strings.Contains(text, "updates to statefulset spec") && strings.Contains(text, "are forbidden"):
		base.Code, base.Title = "statefulset_immutable_upgrade", "StatefulSet 升级触发了 Kubernetes 不可变字段保护"
		base.Cause = "Helm 升级尝试修改已有 StatefulSet 的 volumeClaimTemplates 等不可变存储模板，Kubernetes 拒绝了该 Patch。"
		base.Impact = "已有 Redis/ActiveMQ 工作负载和 PVC 未被删除；其他在错误前完成的组件可能已经成功更新。"
		if job.Action == ActionPlatform {
			base.Suggestion = "如果本次只修改域名、TLS、TCP 转发或告警，请使用“仅应用接入配置”，平台不会触碰该 StatefulSet；真正升级组件时再单独处理 Chart 迁移。不要删除 PVC。"
			base.Retry = "接入配置可立即隔离重试；组件升级需保持 volumeClaimTemplates 等不可变字段不变后再原地重试。"
		} else {
			base.Suggestion = "保持已有组件的 volumeClaimTemplates 不变，只将新存储元数据应用到需要的新建组件；不要删除 PVC。"
			base.Retry = "Chart 不再修改现有 StatefulSet 不可变字段后可直接原地重试。"
		}
		return base
	case strings.Contains(text, "cannot inspect helm releases before component retry") && strings.Contains(text, "unknown flag"):
		base.Code, base.Title = "platform_tool_compatibility", "平台自动对账命令与 Helm 版本不兼容"
		base.Cause = "平台在读取 Helm Release 清单时使用了当前 Helm 版本不支持的命令参数。"
		base.Impact = "任务在只读对账阶段已停止，Terraform 和 Kubernetes 资源均未被该次重试修改。"
		base.Suggestion = "使用 Helm 3/4 兼容的显式状态过滤参数后重试；不需要手工删除 Release 或 PVC。"
		base.Retry = "平台命令参数修复后可直接重试。"
		return base
	case strings.Contains(text, "creating mq broker") && strings.Contains(text, "does not support host instance type"):
		instanceType := "当前配置的实例规格"
		if match := regexp.MustCompile(`(?i)host instance type \[([^\]]+)\]`).FindStringSubmatch(message); len(match) > 1 {
			instanceType = match[1]
		}
		base.Code, base.Title = "amazon_mq_instance_unsupported", "Amazon MQ RabbitMQ 实例规格不可用"
		base.Cause = "AWS 拒绝使用 " + instanceType + " 创建新的 RabbitMQ Broker；该规格在当前引擎或 Region 已不再支持。"
		base.Impact = "RabbitMQ Broker 未创建；EKS、RDS、Redis 等此前成功创建的资源会保留在 Terraform State 中，不需要手工删除。"
		base.Suggestion = "在部署配置的 Amazon MQ RabbitMQ 中改用 mq.m7g.medium 或 AWS 当前返回的更高规格。"
		base.Retry = "保存有效规格后可原地重试，Terraform 会复用已经创建成功的资源并继续未完成步骤。"
		return base
	case strings.Contains(text, "parameter group is not applicable to engine"),
		strings.Contains(text, "invalidparametercombination") && strings.Contains(text, "parameter group") && strings.Contains(text, "engine"):
		base.Code, base.Title = "elasticache_parameter_group_mismatch", "ElastiCache 引擎与参数组不兼容"
		base.Cause = "ElastiCache 选择的 Redis OSS/Valkey 引擎与 AWS 参数组属于不同引擎家族，AWS 因此拒绝创建缓存集群。"
		base.Impact = "ElastiCache 未创建；已经完成的 VPC、EKS、节点组和其他云资源仍保留在 Terraform State 中，不需要删除或重建。"
		base.Suggestion = "让参数组随引擎和版本自动匹配：Redis OSS 7.x 使用 default.redis7.cluster.on，Valkey 8.x 使用 default.valkey8.cluster.on。"
		base.Retry = "修正参数组后可直接原地重试；Terraform 会跳过已成功资源并继续创建 ElastiCache。"
		return base
	case strings.Contains(text, "deletion protection") &&
		(strings.Contains(text, "cannot delete") || strings.Contains(text, "disable") || strings.Contains(text, "protected")):
		base.Code, base.Title = "cloud_resource_deletion_protected", "云服务删除保护仍处于开启状态"
		base.Cause = "AWS 拒绝删除受保护的 RDS、Aurora 或 DocumentDB 资源。删除保护不能在资源被关闭后与删除动作同时完成，必须先保留该服务并单独关闭保护。"
		base.Impact = "数据库实例、集群及其中数据均未被删除；Terraform State 继续保留资源管理关系。"
		base.Suggestion = "回到阶段1的云中间件与云数据库：先重新开启该服务，只关闭“删除保护”，保存并执行一次更新部署；确认保护已关闭后，再关闭服务并执行第二次更新部署。"
		base.Retry = "不要直接重复当前删除任务。先完成关闭删除保护的更新，再发起卸载；无需手工删除 State 或数据库。"
		return base
	case strings.Contains(text, "final snapshot") &&
		(strings.Contains(text, "already exists") || strings.Contains(text, "alreadyexist") || strings.Contains(text, "duplicate")):
		base.Code, base.Title = "cloud_final_snapshot_name_conflict", "数据库最终快照名称已存在"
		base.Cause = "删除数据库时配置的最终快照名称已被历史快照占用，AWS 为防止覆盖已有备份而拒绝继续。"
		base.Impact = "数据库及已有快照均未删除，Terraform State 仍保留完整管理关系。"
		base.Suggestion = "保留数据库启用状态并修改最终快照名称，或确认已有快照可作为最终备份后选择跳过最终快照；保存并完成一次阶段1更新后再关闭服务。"
		base.Retry = "处理快照名称或保留策略后可重新发起卸载；不要删除 Terraform State。"
		return base
	case strings.Contains(text, "repositoryalreadyexistsexception") && strings.Contains(text, "ecr"):
		base.Code, base.Title = "ecr_shared_repository_state_conflict", "ECR 项目共享仓库被环境重复创建"
		base.Cause = "目标 ECR 仓库已经存在于当前 AWS 账号和 Region，但旧版环境 Terraform State 仍把它当作该环境的独占新资源。ECR 不属于 VPC，也不应按 dev/test/uat/prod 重复创建。"
		base.Impact = "ECR 仓库和已有镜像未受影响；Terraform 在创建重复仓库时停止，前面已经成功的 VPC、EKS、数据库等资源会继续保留在 State 中。"
		base.Suggestion = "升级平台后，ECR 会改为项目级共享资源：部署前存在则复用、不存在才创建；旧环境 State 只解除 ECR 管理关系，不删除仓库或镜像。"
		base.Retry = "平台升级完成后直接重试阶段一；不需要手工删除、改名或导入 ECR 仓库。生产镜像使用 prod-<版本> Tag 与测试镜像区分。"
		return base
	case strings.Contains(text, "defaultuserrequired") && strings.Contains(text, "elasticache"):
		base.Code, base.Title = "elasticache_serverless_default_user_missing", "ElastiCache Serverless 缺少受认证的 default 用户"
		base.Cause = "Redis/Valkey Serverless User Group 强制要求至少包含一个 user_name=default 的成员；当前配置只有应用用户，因此 AWS 拒绝创建 User Group。"
		base.Impact = "Serverless Cache 与 User Group 尚未创建；已有 VPC、EKS、Aurora、密码 Secret 和 app 用户会保留在 Terraform State 中。"
		base.Suggestion = "平台应保留 app 用户，并额外创建一个使用同一随机密码的 default 用户加入 User Group；不要使用 AWS 内置的 nopass default 用户。"
		base.Retry = "发布修复后直接原地重试阶段一；无需删除 app 用户、Secret 或任何现有基础资源。"
		return base
	case strings.Contains(text, "kubernetes cluster unreachable") && (strings.Contains(text, "i/o timeout") || strings.Contains(text, "context deadline exceeded")):
		base.Code, base.Title = "eks_api_endpoint_unreachable", "平台无法连接目标 EKS API"
		base.Cause = "kubeconfig 已生成，但目标 EKS API 只返回 VPC 私网地址或公网白名单未包含平台固定出口，部署服务因此无法访问 Kubernetes API。"
		base.Impact = "AWS 基础资源 Apply 已完成并保存在统一 State；基础组件尚未安装，不会回滚或重建 EKS、数据库、Redis、NAT 与 ECR。"
		base.Suggestion = "跨 VPC/跨 Region 管理时开启 EKS 公网 API，并只放行部署平台的固定 NAT 出口 /32；同时保留私网 API 供集群内使用。不要把公网白名单长期设为 0.0.0.0/0。"
		base.Retry = "EKS Endpoint 更新为 ACTIVE 且平台出口已加入白名单后，直接重试阶段一；Terraform 会显示 0 删除并从基础组件对账继续。"
		return base
	case strings.Contains(text, "cannot re-use a name that is still in use"):
		releases := helmReleaseNames(message)
		releaseText := "同名 Helm Release"
		if len(releases) > 0 {
			releaseText = "Helm Release（" + strings.Join(releases, "、") + "）"
		}
		base.Code, base.Title = "helm_release_state_conflict", "Helm Release 与 Terraform State 不一致"
		base.Cause = "EKS 中已存在" + releaseText + "，但当前 Terraform State 没有它们的记录。通常是上次部署在 Helm 创建完成前后被中断。"
		base.Impact = "AWS 基础资源和 EKS 不会因此重建；本次任务停在基础服务接管阶段，相关组件可能已运行，也可能停在 pending/failed。"
		base.Suggestion = "先执行资源状态对账：健康的 Release 导入 Terraform State；pending/failed 残留只清理 Release 记录并保留 PVC、Secret 和数据。"
		base.Retry = "不要直接重试；完成状态对账后，从当前失败阶段重试。"
		return base
	case strings.Contains(text, "another operation (install/upgrade/rollback) is in progress"):
		base.Code, base.Title = "helm_release_operation_pending", "上次 Helm 操作中断，Release 仍处于 Pending"
		base.Cause = "上次组件安装、升级或回滚被中断，Helm 为该 Release 保留了 pending 操作锁。"
		base.Impact = "Terraform 已停止本次升级；已有 PVC、业务数据和健康工作负载不会被平台自动删除。"
		base.Suggestion = "平台会在下一次重试前读取 Helm History，并自动回滚到最近成功 revision 后继续增量升级。"
		base.Retry = "可以直接重试；不需要手工删除 Helm Secret、Release、PVC 或 Terraform State。"
		return base
	case strings.Contains(text, "namespaces \"") && strings.Contains(text, "\" not found"):
		base.Code, base.Title = "component_namespace_missing", "组件目标 Namespace 不存在"
		base.Cause = "一个或多个已启用组件指向了尚未创建的 Kubernetes Namespace，Helm 因此在创建 Release 前直接失败；这不是 Higress 负载均衡超时。"
		base.Impact = "对应组件未完成安装；同一任务中已经创建的 PVC、Helm 资源和其他健康组件会保留，平台不会删除 Namespace 或业务数据。"
		base.Suggestion = "平台会把所有已启用组件的目标 Namespace 自动纳入环境配置和 Terraform State，并永久保留这些 Namespace。"
		base.Retry = "升级到包含 Namespace 自动准备修复的平台后可直接重试阶段2；不需要手工创建 Namespace、删除 PVC 或修改 Terraform State。"
		return base
	case strings.Contains(text, "exec: -e: invalid option") || strings.Contains(text, "basename: invalid option -- 'e'"):
		base.Code, base.Title = "efk_elasticsearch_entrypoint_invalid", "Elasticsearch 启动入口参数错误"
		base.Cause = "EFK Chart 覆盖了 Elasticsearch 镜像的默认 CMD，却直接把 -E 参数作为第一个命令执行，容器因此立即退出。"
		base.Impact = "Elasticsearch 未就绪，Kibana 与 Fluentd 的日志链路也无法工作；已有 Elasticsearch PVC 和数据不会被删除。"
		base.Suggestion = "在 -E 参数前恢复官方镜像的 eswrapper 启动命令，并保留当前 StatefulSet 与 PVC。"
		base.Retry = "发布修复后的内置 EFK Chart 后直接重试阶段2；无需删除 EFK Namespace 或 PVC。"
		return base
	case strings.Contains(text, "could not find a temporary directory") && strings.Contains(text, "fluent"):
		base.Code, base.Title = "efk_fluentd_tmp_permissions", "Fluentd 临时目录权限不符合运行时要求"
		base.Cause = "Fluentd 使用的 Ruby 运行时拒绝没有 sticky-bit 的全局可写 /tmp，容器在初始化阶段退出。"
		base.Impact = "Fluentd 暂时无法采集节点日志；Elasticsearch PVC 和其他业务工作负载不受影响。"
		base.Suggestion = "由 EFK Chart 的初始化容器把独立 emptyDir /tmp 设置为 1777，再启动 Fluentd。"
		base.Retry = "发布修复后的 EFK Chart 后直接重试阶段2，不需要删除 DaemonSet Namespace 或日志数据盘。"
		return base
	case strings.Contains(text, "manager restarted while job was active"):
		base.Code, base.Title = "manager_restarted", "部署进程被管理服务重启中断"
		base.Cause = "任务执行期间管理服务重启，平台无法确认中断瞬间 Terraform、Helm 和 AWS 的最终状态。"
		base.Impact = "部分资源可能已经创建，但任务记录或 Terraform State 可能尚未更新。"
		base.Suggestion = "先检查当前 Terraform State、AWS 资源和 Helm Release，完成对账后再续传。"
		base.Retry = "需要先检测并修复状态，不建议盲目重试。"
		return base
	case strings.Contains(text, "error acquiring the state lock"), strings.Contains(text, "state lock") && strings.Contains(text, "lock info"):
		base.Code, base.Title = "terraform_state_locked", "Terraform State 被其他任务锁定"
		base.Cause = "当前环境的 Terraform State 存在活动锁，可能有另一个任务正在操作，或上次任务异常退出后留下锁记录。"
		base.Impact = "Terraform 尚未修改本次计划中的资源。"
		base.Suggestion = "先确认没有其他活动任务；只有确认为残留锁时才执行解锁。"
		base.Retry = "活动任务结束或残留锁安全清理后可重试。"
		return base
	case strings.Contains(text, "accessdenied"), strings.Contains(text, "unauthorized"), strings.Contains(text, "invalidclienttokenid"), strings.Contains(text, "signaturedoesnotmatch"):
		base.Code, base.Title = "aws_permission_denied", "AWS 身份或 IAM 权限不足"
		base.Cause = "AWS 拒绝了当前操作，项目选择的凭据无效、已过期，或缺少当前 API 权限。"
		base.Impact = "当前失败阶段未能完整执行，之前成功的步骤不会自动回滚。"
		base.Suggestion = "检查项目选择的 AWS 凭据、Session Token、Region 和失败 API 对应的 IAM Policy。"
		base.Retry = "修复凭据或 IAM 权限后可直接重试。"
		return base
	case strings.Contains(text, "session has expired"), strings.Contains(text, "please reauthenticate using 'aws login'"):
		base.Code, base.Title = "aws_session_expired", "AWS 登录会话已过期"
		base.Cause = "当前 AWS SSO/临时会话已过期，命令无法获取 EKS 或 AWS API 凭据。"
		base.Impact = "未能执行需要 AWS 身份的当前步骤。"
		base.Suggestion = "请在 AWS 凭据池为当前项目重新验证或选择一条属于该项目的有效凭据。"
		base.Retry = "AWS 会话恢复后可直接重试。"
		return base
	case strings.Contains(text, "loki.commonstorageconfig") && strings.Contains(text, "bucketnames.chunks"):
		base.Code, base.Title = "loki_storage_config_missing", "Loki 存储配置不完整"
		base.Cause = "Loki Helm Chart 已启用，但 Values 没有提供对象存储 Bucket，且没有切换为本地 filesystem 存储，模板因此无法生成。"
		base.Impact = "Loki 未安装；Jenkins、Prometheus、Higress 等已经成功的组件会保留，不需要卸载。"
		base.Suggestion = "测试/单机环境可使用 SingleBinary + filesystem + 持久卷；生产集群应配置 S3 Bucket、IRSA 和正式 Schema。"
		base.Retry = "补全 Loki Values 后直接重试阶段 2，Terraform 会跳过已成功组件并继续安装 Loki。"
		return base
	case strings.Contains(text, "invalid index") && strings.Contains(text, "master_user_secret"):
		base.Code, base.Title = "database_secret_output_mode_mismatch", "数据库凭据输出与管理模式不兼容"
		base.Cause = "Terraform 输出直接读取了空的 master_user_secret 列表；这可能是数据库使用自管密码，也可能是 AWS 托管 Secret 被外部删除或尚未返回。"
		base.Impact = "任务在生成 Plan 时停止，尚未执行 Apply，因此本次没有创建、修改或删除任何 AWS 资源。"
		base.Suggestion = "使用安全的 try 取值，并按凭据管理模式选择平台 Secrets Manager Secret 或 AWS 托管 Secret；若 AWS 托管 Secret 确实丢失，应由运维确认后执行密码轮换。"
		base.Retry = "发布包含输出兼容修复的平台版本后直接重试阶段一；不需要修改或删除数据库、State 和密码。"
		return base
	case strings.Contains(text, "conflicting configuration arguments") &&
		strings.Contains(text, "manage_master_user_password") &&
		(strings.Contains(text, "master_password") || strings.Contains(text, "with password")):
		base.Code, base.Title = "database_credential_arguments_conflict", "数据库密码管理模式冲突"
		base.Cause = "Terraform 同时向 AWS Provider 传入了自管密码和 AWS 托管密码开关。Provider 会将显式 false 也视为已配置，因此拒绝生成 Plan。"
		base.Impact = "任务停在 Terraform Plan，尚未执行 Apply；本次没有创建、修改或删除 RDS/Aurora 及其他 AWS 资源。"
		base.Suggestion = "自管凭据模式必须只传 master_password，并将 manage_master_user_password 置为 null（完全省略）；AWS 托管模式则只传 manage_master_user_password=true。"
		base.Retry = "发布包含凭据参数互斥修复的平台版本后直接重试阶段一；已保存的密码可继续使用，不需要删除 State 或云资源。"
		return base
	case strings.Contains(text, "autoscalerstatus: initializing") &&
		strings.Contains(text, "resource.k8s.io") && strings.Contains(text, "forbidden"):
		base.Code, base.Title = "cluster_autoscaler_dra_rbac_missing", "Cluster Autoscaler 权限不完整，节点组未自动扩容"
		base.Cause = "Cluster Autoscaler 缺少 Kubernetes Dynamic Resource Allocation 对象的只读权限，启动后一直停在 Initializing；运维节点容量耗尽时因此没有触发扩容。日志中的 untolerated taint 表示业务节点隔离正常生效，不是给运维节点误加了污点。"
		base.Impact = "需要 platform-ops 节点的监控、日志、网关控制面和中间件 Pod 会保持 Pending，多个 Helm Release 最终一起等待超时；业务节点和已有云资源不受影响。"
		base.Suggestion = "平台应为 Cluster Autoscaler 自动补齐 resource.k8s.io 下 deviceclasses、resourceclaims、resourceclaimtemplates、resourceslices 的 get/list/watch 权限，并确认状态从 Initializing 进入 Healthy 后再继续组件部署。"
		base.Retry = "Autoscaler 恢复并完成 platform-ops 扩容、Pending Pod 均已调度后，可直接重试阶段2；无需删除 Namespace、PVC、Helm Release 或 Terraform State。"
		return base
	case strings.Contains(text, `helm_release.catalog["higress"]`) &&
		(strings.Contains(text, "context deadline exceeded") || strings.Contains(text, "timed out waiting")) &&
		strings.Contains(strings.ToLower(text), "insufficient cpu") && strings.Contains(text, "higress-gateway"):
		base.Code, base.Title = "higress_gateway_cpu_unschedulable", "Higress 网关请求超过专用节点容量"
		base.Cause = "Higress Gateway 的 CPU request 大于 ingress-gateway 节点实际 Allocatable CPU。EKS 会为 kubelet 和系统组件预留资源，因此两核节点不能调度请求整整 2 CPU 的 Pod。"
		base.Impact = "Higress Release 保留为 failed，Gateway Pod 为 Pending；Prometheus、Loki、数据库及其他已成功组件和 PVC 不受影响。"
		base.Suggestion = "降低 Higress Gateway 请求 CPU，或升级 ingress-gateway 节点规格。平台会在下次部署前直接比较 Pod 请求与专用节点 Allocatable，避免再次等待完整 Helm 超时。"
		base.Retry = "保存可调度的资源参数后直接重试阶段2；无需删除 Namespace、PVC、LoadBalancer、Helm Secret 或 Terraform State。"
		return base
	case strings.Contains(text, `helm_release.catalog["higress"]`) &&
		(strings.Contains(text, "context deadline exceeded") || strings.Contains(text, "timed out waiting")):
		base.Code, base.Title = "higress_load_balancer_timeout", "Higress 网关负载均衡地址等待超时"
		base.Cause = "Higress 工作负载可能已经运行，但 Gateway Service 在 Helm 超时前没有取得 AWS LoadBalancer 地址。接入已有 EKS 且未托管 Add-on 时，通常是集群没有 AWS Load Balancer Controller，却配置了仅由该 Controller 处理的 external/IP Target 注解。"
		base.Impact = "Higress Release 会保留为 failed，网关 Pod 和 Service 可能已经创建；其他已成功组件与 PVC 不会被自动删除。"
		base.Suggestion = "先查看 higress-gateway Service 的 EXTERNAL-IP 与 Events。已有 EKS 且不管理 Add-on 时应使用 EKS Service Controller 的 NLB instance 模式；托管 Add-on 的集群才使用 external + IP Target。"
		base.Retry = "LoadBalancer 地址出现且 Target 健康后可原地重试阶段2；无需删除 Namespace、PVC 或 Terraform State。"
		return base
	case strings.Contains(text, "higress_nlb_preflight"):
		base.Code, base.Title = "higress_nlb_preflight_failed", "Higress NLB 部署前安全检查未通过"
		base.Cause = "平台在 Terraform/Helm 变更前发现 AWS Load Balancer Controller、VPC 归属、前端安全组用途或 80/443 入站规则不满足部署条件。具体不合格项已保留在底层错误中。"
		base.Impact = "任务在组件和 NLB 变更前停止；现有 EKS、网关、域名、TLS、其他项目资源和 Terraform State 均未被修改。"
		base.Suggestion = "按底层错误恢复 Load Balancer Controller，或重新选择与当前 EKS 同 VPC的专用入口安全组。不要选择 default、eks-cluster-sg-* 或平台守护安全组；自定义模式至少允许 TCP 80/443 之一。"
		base.Retry = "修正配置或 AWS 安全组规则后直接重试阶段2；无需删除 NLB、Namespace、Helm Release、PVC 或 Terraform State。"
		return base
	case strings.Contains(text, `helm_release.catalog["prometheus"]`) &&
		strings.Contains(text, "failed pre-install") && strings.Contains(text, "timed out"):
		base.Code, base.Title = "prometheus_preinstall_hook_unschedulable", "Prometheus 安装前置 Job 未能调度"
		base.Cause = "kube-prometheus-stack 的 admission webhook 证书 Job 在 Helm pre-install 阶段未完成。专用节点组开启 taint 隔离时，该 Hook 也必须带上 platform toleration 和 nodeSelector。"
		base.Impact = "Prometheus Release 标记为 failed，其他已安装成功的 Loki、MySQL、Nacos 等组件保持运行，PVC 和 Namespace 不会被删除。"
		base.Suggestion = "查看 monitoring Namespace 中 prometheus-kube-prometheus-admission-create Job 的 Pod Events；平台应同时为 Prometheus 主体和 admission Hook 注入 workload-class=platform 调度策略。"
		base.Retry = "发布 Hook 调度修复后可直接重试阶段2；Helm 会先删除上次失败的 Hook 并重建，无需删除已成功组件、PVC 或 Namespace。"
		return base
	case strings.Contains(text, "invalid ownership metadata") ||
		strings.Contains(text, "annotation validation error") && strings.Contains(text, "meta.helm.sh/release-namespace"):
		base.Code, base.Title = "helm_cluster_resource_ownership_conflict", "组件与集群已有 Helm 资源冲突"
		base.Cause = "目标 EKS 已有其他 Namespace 的 Helm Release 占用同名集群级资源；新组件不能接管旧 Release 的 ClusterRole、ClusterRoleBinding 或 CRD。"
		base.Impact = "冲突组件未完成安装；同一任务中其他组件可能已经运行。平台不会删除旧监控栈、PVC 或历史数据。"
		base.Suggestion = "接入已有 EKS 时启用命名空间隔离，并避免重复占用 hostNetwork 端口；平台会在重试前只清理当前环境中未进入 Terraform State 的 failed/pending Release。"
		base.Retry = "发布隔离配置后可直接重试阶段2；不要手工删除旧 Namespace 的 Helm Release 或集群级资源。"
		return base
	case strings.Contains(text, "clickvisual kafka") && strings.Contains(text, "no space left on device"):
		base.Code, base.Title = "clickvisual_kafka_storage_full", "ClickVisual Kafka 磁盘已满"
		base.Cause = "ClickVisual 的 Kafka 数据卷已无可写空间，Kafka 因 No space left on device 持续重启，Helm 无法等到组件就绪。"
		base.Impact = "ClickVisual 日志写入链路中断；已经运行的其他组件、Namespace 和 PVC 数据不会被删除。"
		base.Suggestion = "进入环境配置的“ClickVisual 磁盘与容量”，先在线增大 Kafka PVC；平台会显示实际 PVC 名称、当前容量和配置容量。"
		base.Retry = "PVC 容量生效且 Kafka Pod 恢复后，直接重试阶段2；不需要删除 Namespace、PVC 或 Terraform State。"
		return base
	case (strings.Contains(text, "helm_release.consul") || strings.Contains(text, "helm_release.etcd")) &&
		strings.Contains(text, "still modifying") &&
		(strings.Contains(text, "broken pipe") || strings.Contains(text, "context deadline exceeded") || strings.Contains(text, "timed out")):
		base.Code, base.Title = "stateful_platform_pvc_topology_conflict", "Consul/etcd 节点组迁移被 PVC 可用区阻塞"
		base.Cause = "Consul/etcd 已有 EBS PVC 绑定到特定可用区，但 StatefulSet 又被精确限定到了没有该可用区节点的 platform-ops 节点组。Pod 因此持续 Pending，Helm 只能等待到超时。"
		base.Impact = "Consul/etcd 的 PVC 和数据均保留；部分副本可能已迁移，其余副本停在 Pending，当前阶段不会继续。"
		base.Suggestion = "有历史 zonal PVC 的 StatefulSet 只应限定 workload-class=platform，让调度器选择 PVC 所在可用区的运维节点；无状态组件仍可精确绑定 platform-ops。不要删除 PVC。"
		base.Retry = "调度约束修正且所有 Consul/etcd 副本 Ready 后可原地重试；Terraform 会对账现有 Release 和 State，不需要重建组件。"
		return base
	case strings.Contains(text, "job timeout exceeded"),
		strings.Contains(text, "context deadline exceeded"),
		strings.Contains(text, "timed out waiting"),
		strings.Contains(text, "timeout while waiting"):
		base.Code, base.Title = "operation_timeout", "资源创建或组件就绪等待超时"
		base.Cause = "当前操作在限定时间内没有达到就绪状态。"
		base.Impact = "资源可能仍在 AWS 中创建，或 Pod 仍在 Pending/ContainerCreating，不代表所有已创建资源失效。"
		base.Suggestion = "检查 AWS 资源事件、EKS 节点容量、Pod Events、镜像拉取和 PVC 绑定状态。"
		base.Retry = "先确认资源实际状态；仍在创建时应等待，明确失败后再重试。"
		return base
	case strings.Contains(text, "aws_create_quota_insufficient"):
		base.Code, base.Title = "aws_create_quota_insufficient", "新建 EKS 的 AWS 区域剩余额度不足"
		base.Cause = "平台按“总配额减去当前已使用量”计算实际剩余额度；新建托管 EKS 要求 Standard On-Demand vCPU 至少剩余 96、EC2-VPC EIP 至少剩余 5。"
		base.Impact = "任务已在 Terraform Plan/Apply 之前停止，本次没有创建、修改或删除 AWS、EKS 及其他项目资源。"
		base.Suggestion = "根据任务日志中的总配额、已使用量、实际剩余量和缺口，在 AWS Service Quotas 提交对应区域的 EC2 vCPU 或 EIP 提额申请。"
		base.Retry = "提额审批并生效后可直接重试阶段1，不需要删除配置、Terraform State 或已有资源。"
		return base
	case strings.Contains(text, "aws_create_quota_check_unavailable"):
		base.Code, base.Title = "aws_create_quota_check_unavailable", "无法验证新建 EKS 的 AWS 剩余额度"
		base.Cause = "当前项目 AWS 凭据没有读取 Service Quotas 或 EC2 使用量所需的权限，平台无法安全确认剩余 vCPU/EIP。"
		base.Impact = "任务已在 Terraform 执行前停止，AWS 和其他项目资源均未发生变化。"
		base.Suggestion = "为该项目凭据补充 servicequotas:GetServiceQuota、ec2:DescribeInstances、ec2:DescribeInstanceTypes、ec2:DescribeAddresses 和 eks:DescribeCluster 只读权限。"
		base.Retry = "权限补齐后直接重试阶段1。"
		return base
	case strings.Contains(text, "no space left"), strings.Contains(text, "insufficient"):
		base.Code, base.Title = "capacity_insufficient", "资源容量或 AWS 配额不足"
		base.Cause = "目标区域的 AWS 配额，或 EKS 节点 CPU、内存、磁盘不足以满足请求。"
		base.Impact = "当前资源或 Pod 无法创建/调度。"
		base.Suggestion = "检查 Service Quotas、节点组容量、Pod requests/limits 和可用区实例供应。"
		base.Retry = "扩容节点或提高配额后可重试。"
		return base
	case strings.Contains(text, "imagepull"), strings.Contains(text, "errimagepull"), strings.Contains(text, "image pull"):
		base.Code, base.Title = "image_pull_failed", "容器镜像拉取失败"
		base.Cause = "EKS 节点无法下载组件镜像，可能是地址/版本错误、仓库权限、网络出口或 CPU 架构不匹配。"
		base.Impact = "对应 Pod 不会就绪，其他已健康资源不受影响。"
		base.Suggestion = "检查镜像名称与 Tag、imagePullSecret/ECR 权限、节点 NAT 出口和 amd64/arm64 架构。"
		base.Retry = "修复镜像或网络后可重试组件部署。"
		return base
	case job.Action == ActionDestroy && (strings.Contains(failedStepText, "destroy infra terraform") || strings.Contains(failedStepText, "销毁 aws 基础资源")):
		base.Code, base.Title = "destroy_network_dependency", "AWS 网络依赖尚未释放"
		base.Cause = "VPC/子网仍被 LoadBalancer、ENI 或 EKS 安全组引用，AWS 拒绝删除。"
		base.Impact = "销毁未完成，Terraform State 中仍保留剩余资源。"
		base.Suggestion = "查找并释放残留 LoadBalancer/ENI/安全组引用，不要手工删除 Terraform State。"
		base.Retry = "AWS 网络依赖释放后可重试并继续销毁。"
		return base
	case job.Action == ActionDestroy && (strings.Contains(failedStepText, "destroy platform terraform") || strings.Contains(failedStepText, "销毁 eks 平台组件")):
		base.Code, base.Title = "destroy_platform_dependency", "EKS 平台组件尚未完全删除"
		base.Cause = "Helm Release、Namespace finalizer、LoadBalancer 或 Kubernetes API 可达性阻止了组件销毁。"
		base.Impact = "EKS 内仍有平台组件或网络资源残留。"
		base.Suggestion = "检查 Helm 状态、Namespace finalizer、LoadBalancer Service 和 EKS API 连接。"
		base.Retry = "残留依赖处理后可继续销毁。"
		return base
	case strings.Contains(failedStepText, "component"), strings.Contains(failedStepText, "组件"), strings.Contains(failedStepText, "helm"):
		base.Code, base.Title = "helm_operation_failed", "Helm 组件安装或升级失败"
		base.Cause = "Helm Chart 参数、Kubernetes 资源或组件 rollout 未成功。"
		base.Impact = "失败组件不可用，已健康组件不会自动删除。"
		base.Suggestion = "查看首个 Helm/Chart/Values 错误，并检查 Namespace、StorageClass、PVC、镜像和 Pod Events。"
		base.Retry = "修复组件参数或集群依赖后可重试。"
		return base
	case strings.Contains(text, "terraform"), strings.Contains(strings.ToLower(failedStep), "terraform"):
		base.Code, base.Title = "terraform_operation_failed", "Terraform 资源操作失败"
		base.Cause = "Terraform Provider 在当前阶段返回错误，具体技术证据保留在下方原始错误和日志中。"
		base.Impact = "当前阶段未完成，前面已创建的资源通常会保留并由 Terraform State 继续管理。"
		base.Suggestion = "优先查看原始错误中第一个 Error，检查配置值、AWS 配额、资源依赖和 Terraform State。"
		base.Retry = "确认错误原因已修复后再重试，不建议连续盲目重试。"
		return base
	case strings.Contains(text, "helm"):
		base.Code, base.Title = "helm_operation_failed", "Helm 组件安装或升级失败"
		base.Cause = "Helm Chart 参数、Kubernetes 资源或组件 rollout 未成功。"
		base.Impact = "失败组件不可用，已健康组件不会自动删除。"
		base.Suggestion = "查看首个 Helm/Chart/Values 错误，并检查 Namespace、StorageClass、PVC、镜像和 Pod Events。"
		base.Retry = "修复组件参数或集群依赖后可重试。"
		return base
	case strings.Contains(text, "connection refused"), strings.Contains(text, "no such host"), strings.Contains(text, "i/o timeout"):
		base.Code, base.Title = "network_unreachable", "网络或服务地址不可达"
		base.Cause = "平台无法连接 DNS、AWS API、EKS API 或目标服务。"
		base.Impact = "当前 API 调用或集群操作未完成。"
		base.Suggestion = "检查本地网络、DNS、代理、AWS API 可达性和 EKS 公私网访问策略。"
		base.Retry = "网络恢复后可重试。"
		return base
	case failedStep != "":
		base.Cause = "任务在「" + stage + "」停止，平台暂时无法将底层错误精确分类。"
		base.Impact = "当前阶段未完成，之前成功的步骤会保留。"
		base.Suggestion = "查看下方技术错误和日志末尾，优先处理第一个 Error。"
		base.Retry = "确认配置或外部依赖已修复后再重试。"
		return base
	default:
		base.Cause = "任务没有正常完成，当前错误尚未匹配已知类型。"
		base.Impact = "当前阶段未完成。"
		base.Suggestion = "查看下方技术错误和日志末尾，优先处理第一个 Error。"
		base.Retry = "明确并修复原因后再重试。"
		return base
	}
}

func helmReleaseNames(message string) []string {
	matches := regexp.MustCompile(`(?m)\bwith\s+helm_release\.([A-Za-z0-9_-]+)`).FindAllStringSubmatch(message, -1)
	seen := make(map[string]bool, len(matches))
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		result = append(result, match[1])
	}
	return result
}

func actionDisplayName(action Action) string {
	switch action {
	case ActionValidate:
		return "配置校验"
	case ActionPlan:
		return "AWS 资源规划"
	case ActionDeploy:
		return "阶段1 · 基础资源与基础服务"
	case ActionPlatform:
		return "阶段2 · 组件与接入配置"
	case ActionAccess:
		return "阶段2 · 域名、TLS 与告警接入"
	case ActionTLS:
		return "阶段2 · TLS 证书配置"
	case ActionDestroy:
		return "环境销毁"
	default:
		return "部署任务"
	}
}

func displayStepName(value string) string {
	translations := map[string]string{
		"Initialize infra Terraform":                        "初始化 AWS 基础资源 Terraform",
		"Initialize platform Terraform":                     "初始化 EKS 平台组件 Terraform",
		"Prepare infra Terraform workspace":                 "选择 AWS 基础资源状态空间",
		"Prepare platform Terraform workspace":              "选择 EKS 平台组件状态空间",
		"Apply infra Terraform":                             "创建或更新 AWS 基础资源",
		"Apply phase 1 base services":                       "阶段1 · 安装 EKS 基础组件与基础服务",
		"Apply phase 2 components and access configuration": "阶段2 · 安装组件并应用接入配置",
		"Update isolated kubeconfig":                        "更新当前环境 EKS 访问配置",
		"Verify Kubernetes nodes":                           "验证 EKS 节点是否可用",
		"Verify platform Pods":                              "验证平台组件 Pod 是否健康",
		"Destroy platform Terraform":                        "销毁 EKS 平台组件资源",
		"Destroy infra Terraform":                           "销毁 AWS 基础资源",
	}
	if translated := translations[value]; translated != "" {
		return translated
	}
	return value
}

func (m *Manager) diagnoseFailedJob(job *Job) bool {
	previous := job.Diagnosis
	contextText := job.Error
	if tail, err := readFileTail(m.logPath(job), 64*1024); err == nil && strings.TrimSpace(tail) != "" {
		contextText += "\n" + tail
	}
	diagnosis := failureDiagnosis(job, sensitive.RedactText(contextText))
	job.Diagnosis = &diagnosis
	job.FailureHint = strings.TrimSpace(diagnosis.Cause + " " + diagnosis.Suggestion + " " + diagnosis.Retry)
	return previous == nil || *previous != diagnosis
}

func (m *Manager) appendDiagnosisLog(job *Job) error {
	if job.Diagnosis == nil {
		return nil
	}
	path := m.logPath(job)
	if tail, err := readFileTail(path, 64*1024); err == nil {
		const marker = "\n==> 部署失败诊断\n"
		if index := strings.LastIndex(tail, marker); index >= 0 {
			if info, statErr := os.Stat(path); statErr == nil {
				offset := info.Size() - int64(len(tail)) + int64(index)
				if offset >= 0 {
					_ = os.Truncate(path, offset)
				}
			}
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	diagnosis := job.Diagnosis
	_, err = fmt.Fprintf(file, "\n==> 部署失败诊断\n[问题类型] %s\n[失败阶段] %s\n[直接原因] %s\n[影响范围] %s\n[处理建议] %s\n[重试条件] %s\n",
		diagnosis.Title, diagnosis.Stage, diagnosis.Cause, diagnosis.Impact, diagnosis.Suggestion, diagnosis.Retry)
	return err
}

func readFileTail(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 64 * 1024
	}
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	payload, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func conciseStepError(message string) string {
	message = sensitive.RedactText(strings.ReplaceAll(message, "\r\n", "\n"))
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "cannot re-use a name that is still in use"):
		return "EKS 中已有同名 Helm Release，但 Terraform State 尚未接管"
	case strings.Contains(lower, "accessdenied"), strings.Contains(lower, "unauthorized"):
		return "AWS 身份无效或 IAM 权限不足"
	case strings.Contains(lower, "session has expired"):
		return "AWS 登录会话已过期"
	case strings.Contains(lower, "error acquiring the state lock"):
		return "Terraform State 已被其他任务锁定"
	}
	lines := strings.Split(message, "\n")
	for index, line := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "error:") {
			result := strings.TrimSpace(line)
			if index+1 < len(lines) && strings.TrimSpace(lines[index+1]) != "" {
				result += " " + strings.TrimSpace(lines[index+1])
			}
			if len(result) > 300 {
				result = result[:300] + "…"
			}
			return result
		}
	}
	first := strings.TrimSpace(strings.Split(message, "\n")[0])
	if len(first) > 300 {
		first = first[:300] + "…"
	}
	return first
}

// DeleteHistory removes only terminal jobs in the requested scope. Active
// jobs are deliberately preserved so cleanup cannot interrupt deployments.
func (m *Manager) DeleteHistory(project, environment string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	var result error
	for id, job := range m.jobs {
		if job.Project != project || (environment != "" && job.Environment != environment) || !isTerminal(job.Status) {
			continue
		}
		if m.store != nil {
			ctx, cancel := persistenceContext()
			err := m.store.DeleteJob(ctx, id)
			if err == nil && m.realtime != nil {
				err = m.realtime.DeleteCachedJob(ctx, id, job.Project, job.Environment)
			}
			cancel()
			if err != nil {
				result = errors.Join(result, err)
				continue
			}
		} else {
			_ = os.Remove(m.metadataPath(job))
		}
		_ = os.Remove(m.logPath(job))
		delete(m.jobs, id)
		removed++
	}
	return removed, result
}

func Retryable(status Status) bool {
	return status == StatusFailed || status == StatusCanceled || status == StatusIgnored
}

func (m *Manager) updateProgress(id string, update progressUpdate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	switch update.Kind {
	case "plan":
		job.Steps = make([]Step, 0, len(update.Names))
		for _, name := range update.Names {
			if strings.TrimSpace(name) != "" {
				job.Steps = append(job.Steps, Step{Name: name, Status: StepPending})
			}
		}
	case "start":
		index := stepIndex(job.Steps, update.Name)
		if index < 0 {
			job.Steps = append(job.Steps, Step{Name: update.Name, Status: StepPending})
			index = len(job.Steps) - 1
		}
		job.Steps[index].Status = StepRunning
		job.Steps[index].StartedAt = &now
		job.Steps[index].FinishedAt = nil
		job.Steps[index].Error = ""
		job.CurrentStep = update.Name
	case "finish":
		index := stepIndex(job.Steps, update.Name)
		if index < 0 {
			job.Steps = append(job.Steps, Step{Name: update.Name, Status: StepPending})
			index = len(job.Steps) - 1
		}
		if job.Steps[index].StartedAt == nil {
			job.Steps[index].StartedAt = &now
		}
		job.Steps[index].FinishedAt = &now
		if update.Err != nil {
			job.Steps[index].Status = StepFailed
			job.Steps[index].Error = conciseStepError(update.Err.Error())
		} else {
			job.Steps[index].Status = StepSucceeded
			job.Steps[index].Error = ""
		}
		if job.CurrentStep == update.Name {
			job.CurrentStep = ""
		}
	}
	recalculateProgress(job)
	_ = m.persistLocked(job)
}

func stepIndex(steps []Step, name string) int {
	for index := range steps {
		if steps[index].Name == name {
			return index
		}
	}
	return -1
}

func recalculateProgress(job *Job) {
	job.TotalSteps = len(job.Steps)
	job.SuccessSteps = 0
	job.FailedSteps = 0
	for _, step := range job.Steps {
		switch step.Status {
		case StepSucceeded:
			job.SuccessSteps++
		case StepFailed:
			job.FailedSteps++
		}
	}
	if job.TotalSteps == 0 {
		job.Progress = 0
		return
	}
	job.Progress = (job.SuccessSteps + job.FailedSteps) * 100 / job.TotalSteps
	if job.Status == StatusSucceeded {
		job.Progress = 100
	}
}

// normalizeSucceededSteps enforces the UI contract that a successful job has
// no red failed/pending steps. The runner's nil error is authoritative; a
// command that is intentionally treated as a safe no-op (for example an EKS
// cluster already absent during destroy retry) must not leave a stale failure.
func normalizeSucceededSteps(job *Job, finished time.Time) {
	for index := range job.Steps {
		if job.Steps[index].Status == StepSucceeded {
			continue
		}
		job.Steps[index].Status = StepSucceeded
		job.Steps[index].Error = ""
		if job.Steps[index].StartedAt == nil {
			job.Steps[index].StartedAt = &finished
		}
		if job.Steps[index].FinishedAt == nil {
			job.Steps[index].FinishedAt = &finished
		}
	}
	recalculateProgress(job)
	job.Progress = 100
}

func (m *Manager) load() error {
	if m.store != nil {
		ctx, cancel := persistenceContext()
		defer cancel()
		storedJobs, err := m.store.LoadJobs(ctx)
		if err != nil {
			return err
		}
		for _, stored := range storedJobs {
			job := stored
			if job.Status == StatusRunning || job.Status == StatusQueued {
				now := time.Now().UTC()
				job.Status = StatusFailed
				job.Error = "manager restarted while job was active"
				job.FinishedAt = &now
			}
			if job.Status == StatusFailed {
				if m.diagnoseFailedJob(&job) {
					_ = m.appendDiagnosisLog(&job)
				}
			}
			if job.Status == StatusSucceeded {
				finished := time.Now().UTC()
				if job.FinishedAt != nil {
					finished = *job.FinishedAt
				}
				normalizeSucceededSteps(&job, finished)
			}
			m.jobs[job.ID] = &job
			if err := m.persistLocked(&job); err != nil {
				return err
			}
		}
		return nil
	}
	root, err := os.OpenRoot(m.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	err = filepath.WalkDir(m.dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil
		}
		relative, err := filepath.Rel(m.dir, path)
		if err != nil {
			return err
		}
		b, err := root.ReadFile(relative)
		if err != nil {
			return err
		}
		var job Job
		if err := json.Unmarshal(b, &job); err != nil {
			return err
		}
		if job.Status == StatusRunning || job.Status == StatusQueued {
			now := time.Now().UTC()
			job.Status = StatusFailed
			job.Error = "manager restarted while job was active"
			job.FinishedAt = &now
		}
		if job.Status == StatusFailed {
			if m.diagnoseFailedJob(&job) {
				_ = m.appendDiagnosisLog(&job)
			}
		}
		if job.Status == StatusSucceeded {
			finished := time.Now().UTC()
			if job.FinishedAt != nil {
				finished = *job.FinishedAt
			}
			normalizeSucceededSteps(&job, finished)
		}
		copy := job
		m.jobs[job.ID] = &copy
		_ = m.persistLocked(&copy)
		return nil
	})
	return err
}

func (m *Manager) persistLocked(job *Job) error {
	if info, err := os.Stat(m.logPath(job)); err == nil {
		job.LogSize = info.Size()
	}
	if m.store != nil {
		ctx, cancel := persistenceContext()
		defer cancel()
		if err := m.store.SaveJob(ctx, job); err != nil {
			return err
		}
		if m.realtime != nil {
			_ = m.realtime.CacheJob(ctx, job)
		}
		return nil
	}
	b, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.jobDir(job), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.jobDir(job), ".job-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(b); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, m.metadataPath(job))
}

// CheckStorage validates that every project/environment log directory already
// known to the manager is traversable and writable by the running process. It
// is intentionally lightweight and is used by the readiness endpoint so a Pod
// with a broken PVC mount is removed from service before accepting retries.
func (m *Manager) CheckStorage() error {
	m.mu.RLock()
	directories := make([]string, 0, len(m.jobs)+1)
	directories = append(directories, m.dir)
	seen := map[string]struct{}{m.dir: {}}
	for _, job := range m.jobs {
		directory := m.jobDir(job)
		if _, exists := seen[directory]; exists {
			continue
		}
		seen[directory] = struct{}{}
		directories = append(directories, directory)
	}
	m.mu.RUnlock()
	return m.checkStorageDirectories(directories)
}

func (m *Manager) prepareJobStorage(job *Job) error {
	return m.checkStorageDirectories([]string{m.dir, m.jobDir(job)})
}

func (m *Manager) checkStorageDirectories(directories []string) error {
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return wrapLogStorageError("create task log directory", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- task logs are private to the platform UID.
			return wrapLogStorageError("secure task log directory", err)
		}
		probe, err := os.CreateTemp(directory, ".storage-probe-*")
		if err != nil {
			return wrapLogStorageError("write task log directory", err)
		}
		probeName := probe.Name()
		if chmodErr := probe.Chmod(0o600); chmodErr != nil {
			_ = probe.Close()
			_ = os.Remove(probeName)
			return wrapLogStorageError("secure task log probe", chmodErr)
		}
		if closeErr := probe.Close(); closeErr != nil {
			_ = os.Remove(probeName)
			return wrapLogStorageError("close task log probe", closeErr)
		}
		if removeErr := os.Remove(probeName); removeErr != nil {
			return wrapLogStorageError("remove task log probe", removeErr)
		}
	}
	return nil
}

func wrapLogStorageError(operation string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrLogStorageUnavailable, operation, err)
}

func (m *Manager) pruneLocked() {
	if m.historyLimit <= 0 || len(m.jobs) <= m.historyLimit {
		return
	}
	all := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		if isTerminal(job.Status) {
			all = append(all, job)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	for len(m.jobs) > m.historyLimit && len(all) > 0 {
		job := all[0]
		all = all[1:]
		delete(m.jobs, job.ID)
		if m.store != nil {
			ctx, cancel := persistenceContext()
			_ = m.store.DeleteJob(ctx, job.ID)
			if m.realtime != nil {
				_ = m.realtime.DeleteCachedJob(ctx, job.ID, job.Project, job.Environment)
			}
			cancel()
		} else {
			_ = os.Remove(m.metadataPath(job))
		}
		_ = os.Remove(m.logPath(job))
	}
}

var safeSegmentPattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeSegment(value, fallback string) string {
	value = strings.Trim(safeSegmentPattern.ReplaceAllString(value, "-"), "-.")
	if value == "" {
		return fallback
	}
	return value
}

func (m *Manager) jobDir(job *Job) string {
	return filepath.Join(m.dir, safeSegment(job.Project, "legacy"), safeSegment(job.Environment, "unknown"))
}

func (m *Manager) metadataPath(job *Job) string { return filepath.Join(m.jobDir(job), job.ID+".json") }
func (m *Manager) logPath(job *Job) string      { return filepath.Join(m.jobDir(job), job.ID+".log") }

func validAction(action Action) bool {
	switch action {
	case ActionValidate, ActionPlan, ActionDeploy, ActionPlatform, ActionAccess, ActionTLS, ActionStorageExpand, ActionStorageShrink, ActionDestroy:
		return true
	default:
		return false
	}
}

// DetailedTaskRunner receives immutable job metadata in addition to the
// legacy runner arguments. Existing test and integration runners only need to
// implement TaskRunner; deployments that handle operation parameters opt in.
type DetailedTaskRunner interface {
	RunJob(context.Context, Job, io.Writer) error
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func isTerminal(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCanceled || status == StatusIgnored
}

func newID() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(random), nil
}

func persistenceContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

var (
	ErrInvalidAction           = errors.New("invalid job action")
	ErrInvalidCompletionAction = errors.New("invalid job completion action")
	ErrNotCancelable           = errors.New("job is not cancelable")
	ErrNotIgnorable            = errors.New("job failure is not ignorable")
	ErrDestroyNotIgnorable     = errors.New("destroy failures cannot be ignored")
	ErrInvalidIgnoreReason     = errors.New("ignore reason must contain 3 to 500 characters")
	ErrEnvironmentBusy         = errors.New("environment already has an active job")
	ErrLogStorageUnavailable   = errors.New("job log storage unavailable")
)
