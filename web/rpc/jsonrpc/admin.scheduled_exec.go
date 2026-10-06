package jsonrpc

import (
	"context"
	"errors"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/pkg/execschedule"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/pkg/timeutil"
	"github.com/nuomiiiii/lite/utils"
	"github.com/nuomiiiii/lite/web/api/remote"
	"github.com/nuomiiiii/lite/web/remotectl"
	"github.com/nuomiiiii/lite/web/scheduledexec"
	"gorm.io/gorm"
)

var (
	loadScheduledExec        = tasks.GetScheduledExec
	saveScheduledExecEnabled = tasks.SetScheduledExecEnabled
	remoteManagementEnabled  = remote.RemoteManagementEnabled
	runScheduledNow          = scheduledexec.RunNow
	beforeScheduledRunNow    func()
)

func init() {
	reg("listScheduledExec", adminListScheduledExec, "List scheduled remote commands")
	reg("createScheduledExec", adminCreateScheduledExec, "Create a scheduled remote command")
	reg("updateScheduledExec", adminUpdateScheduledExec, "Update a scheduled remote command")
	reg("deleteScheduledExec", adminDeleteScheduledExec, "Delete a scheduled remote command")
	reg("setScheduledExecEnabled", adminSetScheduledExecEnabled, "Enable or disable a scheduled remote command")
	reg("runScheduledExec", adminRunScheduledExec, "Run a scheduled remote command now")
	reg("listScheduledExecRuns", adminListScheduledExecRuns, "List runs of a scheduled remote command")
	reg("reorderScheduledExec", adminReorderScheduledExec, "Reorder scheduled remote commands")
}

type scheduledExecBody struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Command         string   `json:"command"`
	Clients         []string `json:"clients"`
	Enabled         bool     `json:"enabled"`
	Kind            string   `json:"kind"`
	IntervalMinutes int      `json:"interval_minutes"`
	TimeOfDay       string   `json:"time_of_day"`
	Weekday         int      `json:"weekday"`
	Weekdays        []int    `json:"weekdays"`
	MonthDay        int      `json:"month_day"`
	Grant           string   `json:"grant"`
	PageID          string   `json:"page_id"`
}

type scheduledExecDTO struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	Command         string               `json:"command"`
	Clients         []string             `json:"clients"`
	Enabled         bool                 `json:"enabled"`
	DisabledReason  string               `json:"disabled_reason,omitempty"`
	Kind            string               `json:"kind"`
	IntervalMinutes int                  `json:"interval_minutes"`
	TimeOfDay       string               `json:"time_of_day"`
	Weekday         int                  `json:"weekday"`
	Weekdays        []int                `json:"weekdays,omitempty"`
	MonthDay        int                  `json:"month_day,omitempty"`
	LastRunAt       *time.Time           `json:"last_run_at,omitempty"`
	NextRunAt       *time.Time           `json:"next_run_at,omitempty"`
	LastRun         *scheduledExecRunDTO `json:"last_run,omitempty"`
}

type scheduledExecRunDTO struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id,omitempty"`
	Status    string    `json:"status"`
	Note      string    `json:"note,omitempty"`
	Sent      int       `json:"sent"`
	Queued    int       `json:"queued"`
	Offline   int       `json:"offline"`
	Failed    int       `json:"failed"`
	StartedAt time.Time `json:"started_at"`
}

func adminListScheduledExec(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	rows, err := tasks.ListScheduledExecs()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to list scheduled tasks: "+err.Error(), nil)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	latest, err := tasks.LatestScheduledExecRuns(ids)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to list scheduled tasks: "+err.Error(), nil)
	}
	out := make([]scheduledExecDTO, 0, len(rows))
	for _, row := range rows {
		var last *models.ScheduledExecRun
		if run, ok := latest[row.ID]; ok {
			last = &run
		}
		out = append(out, scheduledExecDTOFrom(row, last))
	}
	return map[string]any{"schedules": out}, nil
}

func adminReorderScheduledExec(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	var params struct {
		IDs []string `json:"ids"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid or missing request body: "+err.Error(), nil)
	}
	if err := scheduledexec.WithMutation(func() error {
		return tasks.ReorderScheduledExecs(params.IDs)
	}); err != nil {
		if errors.Is(err, tasks.ErrScheduledExecOrder) {
			return nil, rpc.MakeError(rpc.InvalidParams, tasks.ErrScheduledExecOrder.Error(), nil)
		}
		return nil, rpc.MakeError(rpc.InternalError, "Failed to reorder scheduled tasks: "+err.Error(), nil)
	}
	return map[string]any{"ok": true}, nil
}

func adminCreateScheduledExec(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	meta, rpcErr := scheduledExecActor(ctx)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var params scheduledExecBody
	req.BindParams(&params)
	row, rpcErr := buildScheduledExec("", meta.Principal.UserUUID, params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	count, err := tasks.CountScheduledExecs()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to create scheduled task: "+err.Error(), nil)
	}
	if count >= execschedule.MaxSchedules {
		return nil, rpc.MakeError(rpc.InvalidParams, "too many scheduled tasks", nil)
	}
	row.ID = utils.GenerateRandomString(16)
	if row.ID == "" {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to create scheduled task", nil)
	}
	epoch := accounts.UserSecurityEpoch(meta.Principal.UserUUID)
	var expires time.Time
	var grantEpoch uint64
	if rpcErr = commitScheduledExec(meta.Principal.UserUUID, epoch, func() (string, *rpc.JsonRpcError) {
		count, err = tasks.CountScheduledExecs()
		if err != nil {
			return "", rpc.MakeError(rpc.InternalError, "Failed to create scheduled task: "+err.Error(), nil)
		}
		if count >= execschedule.MaxSchedules {
			return "", rpc.MakeError(rpc.InvalidParams, "too many scheduled tasks", nil)
		}
		expires, grantEpoch, rpcErr = consumeScheduledExecGrant(meta, params.Grant, params.PageID)
		if rpcErr != nil {
			return "", rpcErr
		}
		if err = tasks.CreateScheduledExec(row); err != nil {
			return "", rpc.MakeError(rpc.InternalError, "Failed to create scheduled task: "+err.Error(), nil)
		}
		return row.ID, nil
	}); rpcErr != nil {
		return nil, rpcErr
	}
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "info", "audit.scheduled_exec_save", map[string]string{"name": row.Name})
	return grantPayload(meta.Principal.UserUUID, meta.SessionToken, params.PageID, expires, grantEpoch, map[string]any{
		"schedule": scheduledExecDTOFrom(*row, nil),
	}), nil
}

func adminUpdateScheduledExec(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	meta, rpcErr := scheduledExecActor(ctx)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var params scheduledExecBody
	req.BindParams(&params)
	if params.ID == "" {
		return nil, rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
	}
	current, err := loadScheduledExec(params.ID)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
	}
	if current == nil {
		return nil, rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
	}
	row, rpcErr := buildScheduledExec(current.ID, meta.Principal.UserUUID, params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	epoch := accounts.UserSecurityEpoch(meta.Principal.UserUUID)
	var expires time.Time
	var grantEpoch uint64
	if rpcErr = commitScheduledExec(meta.Principal.UserUUID, epoch, func() (string, *rpc.JsonRpcError) {
		fresh, err := loadScheduledExec(params.ID)
		if err != nil {
			return "", rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
		}
		if fresh == nil {
			return "", rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
		}
		row.CreatedAt = fresh.CreatedAt
		row.LastRunAt = fresh.LastRunAt
		row.SortOrder = fresh.SortOrder
		expires, grantEpoch, rpcErr = consumeScheduledExecGrant(meta, params.Grant, params.PageID)
		if rpcErr != nil {
			return "", rpcErr
		}
		if err = tasks.SaveScheduledExec(row); err != nil {
			return "", rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
		}
		if !row.Enabled {
			scheduledexec.NoteStopped(row.ID)
		}
		return row.ID, nil
	}); rpcErr != nil {
		return nil, rpcErr
	}
	if !row.Enabled {
		if err = scheduledexec.CancelPendingDelivery([]string{row.ID}); err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "定时任务已停用，但未能取消未投递的命令: "+err.Error(), nil)
		}
	}
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "info", "audit.scheduled_exec_save", map[string]string{"name": row.Name})
	return grantPayload(meta.Principal.UserUUID, meta.SessionToken, params.PageID, expires, grantEpoch, map[string]any{
		"schedule": scheduledExecDTOFrom(*row, nil),
	}), nil
}

func adminDeleteScheduledExec(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	var params struct {
		ID string `json:"id"`
	}
	req.BindParams(&params)
	row, err := loadScheduledExec(params.ID)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to delete scheduled task: "+err.Error(), nil)
	}
	if row == nil {
		return nil, rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
	}
	var taskIDs []string
	err = scheduledexec.WithMutation(func() error {
		fresh, err := loadScheduledExec(params.ID)
		if err != nil {
			return err
		}
		if fresh == nil {
			return gorm.ErrRecordNotFound
		}
		taskIDs, err = tasks.DeleteScheduledExec(params.ID)
		if err != nil {
			return err
		}
		scheduledexec.NoteStopped(params.ID)
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
	}
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to delete scheduled task: "+err.Error(), nil)
	}
	scheduledexec.DropQueuedExec(taskIDs)
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "warn", "audit.scheduled_exec_delete", map[string]string{"name": row.Name})
	return map[string]any{"id": params.ID}, nil
}

func adminSetScheduledExecEnabled(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
		Grant   string `json:"grant"`
		PageID  string `json:"page_id"`
	}
	req.BindParams(&params)
	if !params.Enabled {
		if err := denyAPIKey(ctx); err != nil {
			return nil, err
		}
		row, err := loadScheduledExec(params.ID)
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
		}
		if row == nil {
			return nil, rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
		}
		if err := markScheduledExecStopped(params.ID); err != nil {
			return nil, scheduledExecStoreError(err)
		}
		if err := scheduledexec.CancelPendingDelivery([]string{params.ID}); err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "定时任务已停用，但未能取消未投递的命令: "+err.Error(), nil)
		}
		actor, ip := auditActor(ctx)
		auditlog.Event(ip, actor, "warn", "audit.scheduled_exec_stop", map[string]string{"name": row.Name})
		updated, err := loadScheduledExec(params.ID)
		if err != nil || updated == nil {
			return map[string]any{"id": params.ID, "enabled": false}, nil
		}
		return map[string]any{"schedule": scheduledExecDTOFrom(*updated, nil)}, nil
	}

	meta, rpcErr := scheduledExecActor(ctx)
	if rpcErr != nil {
		return nil, rpcErr
	}
	row, err := loadScheduledExec(params.ID)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
	}
	if row == nil {
		return nil, rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
	}
	next, nextErr := execschedule.Next(storedScheduleSpec(row), time.Now(), timeutil.BeijingLocation)
	if nextErr != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, nextErr.Error(), nil)
	}
	epoch := accounts.UserSecurityEpoch(meta.Principal.UserUUID)
	var expires time.Time
	var grantEpoch uint64
	if rpcErr = commitScheduledExec(meta.Principal.UserUUID, epoch, func() (string, *rpc.JsonRpcError) {
		fresh, err := loadScheduledExec(params.ID)
		if err != nil {
			return "", rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
		}
		if fresh == nil {
			return "", rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
		}
		expires, grantEpoch, rpcErr = consumeScheduledExecGrant(meta, params.Grant, params.PageID)
		if rpcErr != nil {
			return "", rpcErr
		}
		if err = saveScheduledExecEnabled(params.ID, true, "", meta.Principal.UserUUID, &next); err != nil {
			return "", scheduledExecStoreError(err)
		}
		return params.ID, nil
	}); rpcErr != nil {
		return nil, rpcErr
	}
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "info", "audit.scheduled_exec_save", map[string]string{"name": row.Name})
	updated, err := loadScheduledExec(params.ID)
	if err != nil || updated == nil {
		return grantPayload(meta.Principal.UserUUID, meta.SessionToken, params.PageID, expires, grantEpoch, map[string]any{"id": params.ID, "enabled": true}), nil
	}
	return grantPayload(meta.Principal.UserUUID, meta.SessionToken, params.PageID, expires, grantEpoch, map[string]any{
		"schedule": scheduledExecDTOFrom(*updated, nil),
	}), nil
}

func adminRunScheduledExec(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	meta, rpcErr := scheduledExecActor(ctx)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var params struct {
		ID     string `json:"id"`
		Grant  string `json:"grant"`
		PageID string `json:"page_id"`
	}
	req.BindParams(&params)
	row, err := loadScheduledExec(params.ID)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to run scheduled task: "+err.Error(), nil)
	}
	if row == nil {
		return nil, rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
	}
	epoch := accounts.UserSecurityEpoch(meta.Principal.UserUUID)
	var expires time.Time
	var grantEpoch uint64
	var captured uint64
	var command string
	var clientIDs []string
	if rpcErr = commitScheduledExec(meta.Principal.UserUUID, epoch, func() (string, *rpc.JsonRpcError) {
		fresh, err := loadScheduledExec(params.ID)
		if err != nil {
			return "", rpc.MakeError(rpc.InternalError, "Failed to run scheduled task: "+err.Error(), nil)
		}
		if fresh == nil {
			return "", rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
		}
		expires, grantEpoch, rpcErr = consumeScheduledExecGrant(meta, params.Grant, params.PageID)
		if rpcErr != nil {
			return "", rpcErr
		}
		command = fresh.Command
		clientIDs = append([]string(nil), fresh.Clients...)
		captured = scheduledexec.PeekStopGen(params.ID)
		return "", nil
	}); rpcErr != nil {
		return nil, rpcErr
	}
	if beforeScheduledRunNow != nil {
		beforeScheduledRunNow()
	}
	actor, ip := auditActor(ctx)
	owner := meta.Principal.UserUUID
	run, err := runScheduledNow(params.ID, command, clientIDs, actor, ip, captured, func() bool {
		if !scheduledexec.RunStillCurrent(params.ID, captured) {
			return false
		}
		if accounts.UserSecurityEpoch(owner) != grantEpoch || !remoteManagementEnabled() {
			return false
		}
		fresh, err := loadScheduledExec(params.ID)
		return err == nil && fresh != nil
	})
	if err != nil {
		if err.Error() == "scheduled task not found" {
			return nil, rpc.MakeError(rpc.NotFound, err.Error(), nil)
		}
		if err.Error() == "站点未启用远程管理" {
			return nil, rpc.MakeError(rpc.PermissionDenied, err.Error(), nil)
		}
		return nil, rpc.MakeError(rpc.InternalError, "Failed to run scheduled task: "+err.Error(), nil)
	}
	if run == nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to run scheduled task", nil)
	}
	return grantPayload(meta.Principal.UserUUID, meta.SessionToken, params.PageID, expires, grantEpoch, map[string]any{
		"run": scheduledExecRunDTOFrom(*run),
	}), nil
}

func adminListScheduledExecRuns(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	var params struct {
		ID string `json:"id"`
	}
	req.BindParams(&params)
	row, err := loadScheduledExec(params.ID)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to list scheduled task runs: "+err.Error(), nil)
	}
	if row == nil {
		return nil, rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
	}
	runs, err := tasks.ListScheduledExecRuns(params.ID, execschedule.RunHistory)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to list scheduled task runs: "+err.Error(), nil)
	}
	out := make([]scheduledExecRunDTO, 0, len(runs))
	for _, run := range runs {
		out = append(out, scheduledExecRunDTOFrom(run))
	}
	return map[string]any{"runs": out}, nil
}

func scheduledExecActor(ctx context.Context) (*rpc.ContextMeta, *rpc.JsonRpcError) {
	if err := denyAPIKey(ctx); err != nil {
		return nil, err
	}
	meta := rpc.MetaFromContext(ctx)
	if meta == nil || meta.Principal == nil || meta.SessionToken == "" {
		return nil, rpc.MakeError(rpc.PermissionDenied, "Remote execution requires an administrator session", nil)
	}
	if !remoteManagementEnabled() {
		return nil, rpc.MakeError(rpc.PermissionDenied, "站点未启用远程管理", nil)
	}
	return meta, nil
}

func commitScheduledExec(userUUID string, epoch uint64, write func() (string, *rpc.JsonRpcError)) *rpc.JsonRpcError {
	var rpcErr *rpc.JsonRpcError
	var stopped string
	err := scheduledexec.WithMutation(func() error {
		if rpcErr = scheduledExecClosed(userUUID, epoch); rpcErr != nil {
			return scheduledexec.ErrAbort
		}
		id, writeErr := write()
		if writeErr != nil {
			rpcErr = writeErr
			return scheduledexec.ErrAbort
		}
		if accounts.UserSecurityEpoch(userUUID) != epoch {
			if id != "" {
				if err := saveScheduledExecEnabled(id, false, models.ScheduledExecReasonSecurity, "", nil); err != nil {
					rpcErr = rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
					return scheduledexec.ErrAbort
				}
				scheduledexec.NoteStopped(id)
				stopped = id
			}
			rpcErr = rpc.MakeError(rpc.PermissionDenied, "账户安全状态已变化", nil)
			return scheduledexec.ErrAbort
		}
		if !remoteManagementEnabled() {
			if id != "" {
				if err := saveScheduledExecEnabled(id, false, models.ScheduledExecReasonRemoteOff, "", nil); err != nil {
					rpcErr = rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
					return scheduledexec.ErrAbort
				}
				scheduledexec.NoteStopped(id)
				stopped = id
			}
			rpcErr = rpc.MakeError(rpc.PermissionDenied, "站点未启用远程管理", nil)
			return scheduledexec.ErrAbort
		}
		return nil
	})
	if stopped != "" {
		if cancelErr := scheduledexec.CancelPendingDelivery([]string{stopped}); cancelErr != nil {
			return rpc.MakeError(rpc.InternalError, "定时任务已停用，但未能取消未投递的命令: "+cancelErr.Error(), nil)
		}
	}
	if err != nil && !errors.Is(err, scheduledexec.ErrAbort) && rpcErr == nil {
		return rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
	}
	return rpcErr
}

func scheduledExecClosed(userUUID string, epoch uint64) *rpc.JsonRpcError {
	if accounts.UserSecurityEpoch(userUUID) != epoch {
		return rpc.MakeError(rpc.PermissionDenied, "账户安全状态已变化", nil)
	}
	if !remoteManagementEnabled() {
		return rpc.MakeError(rpc.PermissionDenied, "站点未启用远程管理", nil)
	}
	return nil
}

func markScheduledExecStopped(id string) error {
	return scheduledexec.WithMutation(func() error {
		if err := saveScheduledExecEnabled(id, false, "", "", nil); err != nil {
			return err
		}
		scheduledexec.NoteStopped(id)
		return nil
	})
}

func consumeScheduledExecGrant(meta *rpc.ContextMeta, grant, pageID string) (time.Time, uint64, *rpc.JsonRpcError) {
	expires, epoch, err := remotectl.TakeExecGrant(grant, meta.Principal.UserUUID, meta.SessionToken, pageID)
	if err != nil {
		return time.Time{}, 0, rpc.MakeError(rpc.PermissionDenied, err.Error(), nil)
	}
	return expires, epoch, nil
}

func grantPayload(user, session, pageID string, expires time.Time, epoch uint64, payload map[string]any) map[string]any {
	if next, nextExpires, err := remotectl.RotateExecGrant(user, session, pageID, expires, epoch); err == nil {
		payload["next_grant"] = next
		payload["expires_at"] = nextExpires.UTC()
	}
	return payload
}

func buildScheduledExec(id, owner string, params scheduledExecBody) (*models.ScheduledExec, *rpc.JsonRpcError) {
	name, err := execschedule.ValidateName(params.Name)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if err := execschedule.ValidateCommand(params.Command); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	spec, err := execschedule.Normalize(execschedule.Spec{
		Kind:            params.Kind,
		IntervalMinutes: params.IntervalMinutes,
		TimeOfDay:       params.TimeOfDay,
		Weekday:         params.Weekday,
		Weekdays:        params.Weekdays,
		MonthDay:        params.MonthDay,
	})
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	clientIDs, rpcErr := requireScheduledExecClients(params.Clients)
	if rpcErr != nil {
		return nil, rpcErr
	}
	next, err := execschedule.Next(spec, time.Now(), timeutil.BeijingLocation)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	now := time.Now().UTC()
	reason := ""
	if !params.Enabled {
		reason = ""
	}
	row := &models.ScheduledExec{
		ID:              id,
		Name:            name,
		Command:         params.Command,
		Clients:         clientIDs,
		OwnerUserUUID:   owner,
		Enabled:         params.Enabled,
		DisabledReason:  reason,
		Kind:            spec.Kind,
		IntervalMinutes: spec.IntervalMinutes,
		TimeOfDay:       spec.TimeOfDay,
		Weekday:         spec.Weekday,
		Weekdays:        execschedule.FormatWeekdays(spec.Weekdays),
		MonthDay:        spec.MonthDay,
		NextRunAt:       &next,
		UpdatedAt:       now,
	}
	if id == "" {
		row.CreatedAt = now
	}
	return row, nil
}

func requireScheduledExecClients(values []string) ([]string, *rpc.JsonRpcError) {
	ids := uniqueUUIDs(values)
	if len(ids) == 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "clients is required", nil)
	}
	if len(ids) > execschedule.MaxClients {
		return nil, rpc.MakeError(rpc.InvalidParams, "too many scheduled task clients", nil)
	}
	known, err := clients.GetClientsByUUIDs(ids)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to load clients: "+err.Error(), nil)
	}
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			return nil, rpc.MakeError(rpc.InvalidParams, "unknown scheduled task client", nil)
		}
	}
	return ids, nil
}

func scheduledExecStoreError(err error) *rpc.JsonRpcError {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return rpc.MakeError(rpc.NotFound, "scheduled task not found", nil)
	}
	return rpc.MakeError(rpc.InternalError, "Failed to update scheduled task: "+err.Error(), nil)
}

func storedScheduleSpec(row *models.ScheduledExec) execschedule.Spec {
	if row == nil {
		return execschedule.Spec{}
	}
	return execschedule.Spec{
		Kind:            row.Kind,
		IntervalMinutes: row.IntervalMinutes,
		TimeOfDay:       row.TimeOfDay,
		Weekday:         row.Weekday,
		Weekdays:        execschedule.ParseWeekdays(row.Weekdays),
		MonthDay:        row.MonthDay,
	}
}

func storedWeekdays(row models.ScheduledExec) []int {
	days := execschedule.ParseWeekdays(row.Weekdays)
	if len(days) == 0 && row.Kind == execschedule.KindWeekly {
		return []int{row.Weekday}
	}
	return days
}

func scheduledExecDTOFrom(row models.ScheduledExec, last *models.ScheduledExecRun) scheduledExecDTO {
	clients := []string(row.Clients)
	if clients == nil {
		clients = []string{}
	}
	dto := scheduledExecDTO{
		ID:              row.ID,
		Name:            row.Name,
		Command:         row.Command,
		Clients:         clients,
		Enabled:         row.Enabled,
		DisabledReason:  row.DisabledReason,
		Kind:            row.Kind,
		IntervalMinutes: row.IntervalMinutes,
		TimeOfDay:       row.TimeOfDay,
		Weekday:         row.Weekday,
		Weekdays:        storedWeekdays(row),
		MonthDay:        row.MonthDay,
		LastRunAt:       row.LastRunAt,
		NextRunAt:       row.NextRunAt,
	}
	if last != nil {
		run := scheduledExecRunDTOFrom(*last)
		dto.LastRun = &run
	}
	return dto
}

func scheduledExecRunDTOFrom(run models.ScheduledExecRun) scheduledExecRunDTO {
	return scheduledExecRunDTO{
		ID:        run.ID,
		TaskID:    run.TaskID,
		Status:    run.Status,
		Note:      run.Note,
		Sent:      run.Sent,
		Queued:    run.Queued,
		Offline:   run.Offline,
		Failed:    run.Failed,
		StartedAt: run.StartedAt,
	}
}
