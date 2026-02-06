package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"sandbox/pkg/api"

	utilsexec "k8s.io/client-go/util/exec"
)

const (
	execStatusRunning   = "running"
	execStatusCanceling = "canceling"
	execStatusCompleted = "completed"
	execStatusFailed    = "failed"
	execStatusCanceled  = "canceled"
	execStatusTimedOut  = "timed_out"
)

type execRegistry struct {
	mu        sync.Mutex
	bySandbox map[string]map[string]*execRecord
	retention time.Duration
}

type execRecord struct {
	sandboxID       string
	execID          string
	status          string
	timeoutSeconds  *int
	startedAt       time.Time
	finishedAt      *time.Time
	exitCode        *int
	errMsg          string
	cancel          context.CancelFunc
	cancelRequested bool
}

func newExecRegistry(retention time.Duration) *execRegistry {
	if retention <= 0 {
		retention = 30 * time.Minute
	}
	return &execRegistry{
		bySandbox: map[string]map[string]*execRecord{},
		retention: retention,
	}
}

func (r *execRegistry) start(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reapExpired(time.Now())
		}
	}
}

func (r *execRegistry) createRunning(sandboxID, execID string, timeoutSeconds *int, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byExec := r.bySandbox[sandboxID]
	if byExec == nil {
		byExec = map[string]*execRecord{}
		r.bySandbox[sandboxID] = byExec
	}
	timeoutCopy := intPtrCopy(timeoutSeconds)
	byExec[execID] = &execRecord{
		sandboxID:      sandboxID,
		execID:         execID,
		status:         execStatusRunning,
		timeoutSeconds: timeoutCopy,
		startedAt:      time.Now().UTC(),
		cancel:         cancel,
	}
}

func (r *execRegistry) get(sandboxID, execID string) (api.ExecStatusResponse, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.getLocked(sandboxID, execID)
	if rec == nil {
		return api.ExecStatusResponse{}, false
	}
	return rec.toAPI(), true
}

func (r *execRegistry) requestCancel(sandboxID, execID string) (api.ExecStatusResponse, bool, bool) {
	r.mu.Lock()
	rec := r.getLocked(sandboxID, execID)
	if rec == nil {
		r.mu.Unlock()
		return api.ExecStatusResponse{}, false, false
	}
	if isTerminalExecStatus(rec.status) {
		snapshot := rec.toAPI()
		r.mu.Unlock()
		return snapshot, true, false
	}
	cancel := rec.cancel
	if cancel == nil {
		snapshot := rec.toAPI()
		r.mu.Unlock()
		return snapshot, true, false
	}
	rec.cancelRequested = true
	if rec.status == execStatusRunning {
		rec.status = execStatusCanceling
	}
	snapshot := rec.toAPI()
	r.mu.Unlock()
	cancel()
	return snapshot, true, true
}

func (r *execRegistry) finish(sandboxID, execID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.getLocked(sandboxID, execID)
	if rec == nil {
		return
	}
	now := time.Now().UTC()
	rec.finishedAt = &now
	rec.cancel = nil

	if err == nil {
		rec.status = execStatusCompleted
		rec.exitCode = intPtr(0)
		rec.errMsg = ""
		return
	}

	if errors.Is(err, context.DeadlineExceeded) {
		rec.status = execStatusTimedOut
		rec.exitCode = intPtr(124)
		rec.errMsg = err.Error()
		return
	}
	if errors.Is(err, context.Canceled) && rec.cancelRequested {
		rec.status = execStatusCanceled
		rec.errMsg = ""
		return
	}

	if code, ok := exitCodeFromErr(err); ok {
		rec.exitCode = intPtr(code)
		if rec.cancelRequested {
			rec.status = execStatusCanceled
			rec.errMsg = ""
			return
		}
		if code == 0 {
			rec.status = execStatusCompleted
			rec.errMsg = ""
			return
		}
	}
	rec.status = execStatusFailed
	rec.errMsg = err.Error()
}

func (r *execRegistry) reapExpired(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for sandboxID, byExec := range r.bySandbox {
		for execID, rec := range byExec {
			if rec == nil || !isTerminalExecStatus(rec.status) || rec.finishedAt == nil {
				continue
			}
			if now.Sub(*rec.finishedAt) > r.retention {
				delete(byExec, execID)
			}
		}
		if len(byExec) == 0 {
			delete(r.bySandbox, sandboxID)
		}
	}
}

func (r *execRegistry) getLocked(sandboxID, execID string) *execRecord {
	byExec := r.bySandbox[sandboxID]
	if byExec == nil {
		return nil
	}
	return byExec[execID]
}

func (r *execRecord) toAPI() api.ExecStatusResponse {
	resp := api.ExecStatusResponse{
		SandboxID:      r.sandboxID,
		ExecID:         r.execID,
		Status:         r.status,
		TimeoutSeconds: intPtrCopy(r.timeoutSeconds),
		Error:          r.errMsg,
	}
	if !r.startedAt.IsZero() {
		resp.StartedAt = r.startedAt.UTC().Format(time.RFC3339Nano)
	}
	if r.finishedAt != nil {
		resp.FinishedAt = r.finishedAt.UTC().Format(time.RFC3339Nano)
	}
	if r.exitCode != nil {
		resp.ExitCode = intPtr(*r.exitCode)
	}
	return resp
}

func isTerminalExecStatus(status string) bool {
	return status == execStatusCompleted || status == execStatusFailed || status == execStatusCanceled || status == execStatusTimedOut
}

func exitCodeFromErr(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var codeErr utilsexec.CodeExitError
	if errors.As(err, &codeErr) {
		return codeErr.ExitStatus(), true
	}
	return 0, false
}

func intPtr(v int) *int {
	return &v
}

func intPtrCopy(v *int) *int {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}
