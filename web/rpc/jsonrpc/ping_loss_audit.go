package jsonrpc

import (
	"context"
	"strconv"
	"strings"

	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
)

type pingLossAuditSubject struct {
	id   uint
	name string
	task string
}

func pingLossSaveAudit(items []pingLossAuditSubject) (level, key string, params map[string]string) {
	if len(items) == 0 {
		return "", "", nil
	}
	if len(items) == 1 {
		key = "audit.ping_loss_edit"
		if items[0].id == 0 {
			key = "audit.ping_loss_add"
		}
		return "info", key, map[string]string{"name": items[0].name, "task": items[0].task}
	}
	created := 0
	for _, item := range items {
		if item.id == 0 {
			created++
		}
	}
	key = "audit.ping_loss_edit_batch"
	if created == len(items) {
		key = "audit.ping_loss_add_batch"
	}
	return "info", key, map[string]string{"count": strconv.Itoa(len(items))}
}

func pingLossDeleteAudit(items []pingLossAuditSubject) (level, key string, params map[string]string) {
	if len(items) == 0 {
		return "", "", nil
	}
	if len(items) == 1 {
		return "warn", "audit.ping_loss_delete", map[string]string{"name": items[0].name, "task": items[0].task}
	}
	return "warn", "audit.ping_loss_delete_batch", map[string]string{"count": strconv.Itoa(len(items))}
}

func recordPingLossSaves(ctx context.Context, items []*models.PingLossNotification) {
	subjects := make([]pingLossAuditSubject, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		subjects = append(subjects, pingLossAuditSubject{
			id:   item.Id,
			name: clients.DisplayName(item.Client),
			task: pingTaskAuditName(item.TaskId),
		})
	}
	level, key, params := pingLossSaveAudit(subjects)
	writePingLossAudit(ctx, level, key, params)
}

func recordPingLossDefault(ctx context.Context, key string) {
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, "info", key, nil)
}

func writePingLossAudit(ctx context.Context, level, key string, params map[string]string) {
	if key == "" {
		return
	}
	actor, ip := auditActor(ctx)
	auditlog.Event(ip, actor, level, key, params)
}

func loadPingLossAuditSubjects(ids []uint) []pingLossAuditSubject {
	if len(ids) == 0 {
		return nil
	}
	var rows []models.PingLossNotification
	err := dbcore.GetDBInstance().Select("id", "client", "task_id").Where("id IN ?", ids).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		if len(ids) == 1 {
			label := "#" + strconv.FormatUint(uint64(ids[0]), 10)
			return []pingLossAuditSubject{{id: ids[0], name: label, task: label}}
		}
		subjects := make([]pingLossAuditSubject, len(ids))
		for i, id := range ids {
			subjects[i].id = id
		}
		return subjects
	}
	subjects := make([]pingLossAuditSubject, 0, len(rows))
	for _, row := range rows {
		subjects = append(subjects, pingLossAuditSubject{
			id:   row.Id,
			name: clients.DisplayName(row.Client),
			task: pingTaskAuditName(row.TaskId),
		})
	}
	return subjects
}

func pingTaskAuditName(id uint) string {
	if id == 0 {
		return ""
	}
	var task models.PingTask
	err := dbcore.GetDBInstance().Select("name").Where("id = ?", id).Take(&task).Error
	name := strings.TrimSpace(task.Name)
	if err != nil || name == "" {
		return "#" + strconv.FormatUint(uint64(id), 10)
	}
	return name
}
