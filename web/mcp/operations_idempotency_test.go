package mcp

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestValidateMCPUploadChunkUsesOneMiBCap(t *testing.T) {
	if err := validateMCPUploadChunk(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 1<<20))); err != nil {
		t.Fatal(err)
	}
	err := validateMCPUploadChunk(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 1<<20+1)))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized chunk err = %v", err)
	}
	if err := validateMCPUploadChunk("not base64"); err == nil {
		t.Fatal("expected invalid base64 to be rejected")
	}
}

func TestCreateOperationReusesMatchingIdempotencyKey(t *testing.T) {
	db := openMCPTestDB(t)
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })

	lease := models.MCPLease{ID: "ls_idem", MaxConcurrency: 4, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	first, reused, err := createOperation(lease, "node", "exec", "key-1", "digest-a", lease.ExpiresAt)
	if err != nil || reused {
		t.Fatalf("first create: reused=%v err=%v", reused, err)
	}
	second, reused, err := createOperation(lease, "node", "exec", "key-1", "digest-a", lease.ExpiresAt)
	if err != nil || !reused || second.ID != first.ID {
		t.Fatalf("reuse: id=%s want=%s reused=%v err=%v", second.ID, first.ID, reused, err)
	}
	if _, _, err := createOperation(lease, "node", "exec", "key-1", "digest-b", lease.ExpiresAt); err != errIdempotencyConflict {
		t.Fatalf("conflict err = %v, want idempotency conflict", err)
	}
	empty1, _, err := createOperation(lease, "node", "exec", "", "d1", lease.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	empty2, reused, err := createOperation(lease, "node", "exec", "", "d2", lease.ExpiresAt)
	if err != nil || reused || empty1.ID == empty2.ID {
		t.Fatalf("empty keys should not collide: %s %s reused=%v err=%v", empty1.ID, empty2.ID, reused, err)
	}
}

func TestRememberIdempotentOperationSkipsConcurrencySlot(t *testing.T) {
	db := openMCPTestDB(t)
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })

	lease := models.MCPLease{ID: "ls_term_slot", MaxConcurrency: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if _, _, err := createOperation(lease, "node", "exec", "exec-1", "digest-exec", lease.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createOperation(lease, "node", "exec", "exec-2", "digest-exec-2", lease.ExpiresAt); err != ErrConcurrency {
		t.Fatalf("second exec err = %v, want concurrency", err)
	}
	if _, _, err := rememberIdempotentOperation(lease, "node", "terminal.write", "write-1", "digest-write", lease.ExpiresAt); err != nil {
		t.Fatalf("terminal write should not consume a slot: %v", err)
	}
}

func TestFileChunkIdempotencyKeyIncludesOffset(t *testing.T) {
	first := fileChunkIdempotencyKey("file.upload.chunk", "up1", 0, 0)
	second := fileChunkIdempotencyKey("file.upload.chunk", "up1", 3, 1)
	if first == second {
		t.Fatal("identical chunk bytes at different offsets must not share an idempotency key")
	}
	sameOffset := fileChunkIdempotencyKey("file.upload.chunk", "up1", 0, 0)
	if first != sameOffset {
		t.Fatal("retries of the same chunk position must reuse the position key")
	}
}

func TestFileChunkSameContentDifferentOffsetCreatesTwoOps(t *testing.T) {
	db := openMCPTestDB(t)
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })

	lease := models.MCPLease{ID: "ls_chunk_dup", MaxConcurrency: 8, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	first, _, err := createOperation(lease, "node", "file.upload.chunk", fileChunkIdempotencyKey("file.upload.chunk", "up1", 0, 0), "digest-abc", lease.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	second, reused, err := createOperation(lease, "node", "file.upload.chunk", fileChunkIdempotencyKey("file.upload.chunk", "up1", 3, 1), "digest-abc", lease.ExpiresAt)
	if err != nil || reused || second.ID == first.ID {
		t.Fatalf("second chunk: id=%s first=%s reused=%v err=%v", second.ID, first.ID, reused, err)
	}
	if _, _, err := createOperation(lease, "node", "file.upload.chunk", fileChunkIdempotencyKey("file.upload.chunk", "up1", 0, 0), "digest-xyz", lease.ExpiresAt); err != errIdempotencyConflict {
		t.Fatalf("same offset different bytes err = %v, want conflict", err)
	}
}

func TestCreateOperationReusesTerminalOpenKey(t *testing.T) {
	db := openMCPTestDB(t)
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })

	lease := models.MCPLease{ID: "ls_term_open", MaxConcurrency: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	first, reused, err := createOperation(lease, "node", "terminal.open", "open-1", "digest-open", lease.ExpiresAt)
	if err != nil || reused {
		t.Fatalf("first open: reused=%v err=%v", reused, err)
	}
	second, reused, err := createOperation(lease, "node", "terminal.open", "open-1", "digest-open", lease.ExpiresAt)
	if err != nil || !reused || second.ID != first.ID {
		t.Fatalf("retry open: id=%s want=%s reused=%v err=%v", second.ID, first.ID, reused, err)
	}
	if _, _, err := createOperation(lease, "node", "terminal.open", "open-1", "digest-other", lease.ExpiresAt); err != errIdempotencyConflict {
		t.Fatalf("conflict err = %v, want idempotency conflict", err)
	}
}

func openMCPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:mcp-idem-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.MCPOperation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS ux_mcp_operations_lease_idem
		ON mcp_operations(lease_id, idempotency_key)
		WHERE idempotency_key IS NOT NULL AND idempotency_key != ''
	`).Error; err != nil {
		t.Fatalf("unique index: %v", err)
	}
	return db
}
