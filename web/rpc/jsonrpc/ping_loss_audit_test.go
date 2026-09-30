package jsonrpc

import "testing"

func TestPingLossSaveAudit(t *testing.T) {
	level, key, params := pingLossSaveAudit(nil)
	if level != "" || key != "" || params != nil {
		t.Fatalf("empty save: %s %s %#v", level, key, params)
	}

	level, key, params = pingLossSaveAudit([]pingLossAuditSubject{{name: "web-1", task: "icmp"}})
	if level != "info" || key != "audit.ping_loss_add" || params["name"] != "web-1" || params["task"] != "icmp" {
		t.Fatalf("add: %s %s %#v", level, key, params)
	}

	level, key, params = pingLossSaveAudit([]pingLossAuditSubject{{id: 7, name: "web-1", task: "icmp"}})
	if level != "info" || key != "audit.ping_loss_edit" || params["name"] != "web-1" || params["task"] != "icmp" {
		t.Fatalf("edit: %s %s %#v", level, key, params)
	}

	level, key, params = pingLossSaveAudit([]pingLossAuditSubject{{}, {}})
	if level != "info" || key != "audit.ping_loss_add_batch" || params["count"] != "2" {
		t.Fatalf("add batch: %s %s %#v", level, key, params)
	}

	level, key, params = pingLossSaveAudit([]pingLossAuditSubject{{}, {id: 3}})
	if level != "info" || key != "audit.ping_loss_edit_batch" || params["count"] != "2" {
		t.Fatalf("edit batch: %s %s %#v", level, key, params)
	}
}

func TestPingLossDeleteAudit(t *testing.T) {
	level, key, params := pingLossDeleteAudit(nil)
	if level != "" || key != "" || params != nil {
		t.Fatalf("empty delete: %s %s %#v", level, key, params)
	}

	level, key, params = pingLossDeleteAudit([]pingLossAuditSubject{{name: "web-1", task: "icmp"}})
	if level != "warn" || key != "audit.ping_loss_delete" || params["name"] != "web-1" || params["task"] != "icmp" {
		t.Fatalf("delete: %s %s %#v", level, key, params)
	}

	level, key, params = pingLossDeleteAudit([]pingLossAuditSubject{{}, {}, {}})
	if level != "warn" || key != "audit.ping_loss_delete_batch" || params["count"] != "3" {
		t.Fatalf("delete batch: %s %s %#v", level, key, params)
	}
}
