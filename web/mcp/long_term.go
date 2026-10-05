package mcp

import (
	"errors"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

// Long-term authorization is chosen on the approval page. Access tokens remain
// short lived; account-security and explicit revocation continue to invalidate
// these leases. This is not an administrator API key.

// exists is false only when the account is confirmed missing. Other errors
// must not revoke a live grant.
var longTermOwnerLookup = func(uuid string) (exists bool, err error) {
	if strings.TrimSpace(uuid) == "" {
		return false, nil
	}
	_, err = accounts.GetUserByUUID(uuid)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return false, err
}

func longTermExpiresAt() time.Time {
	return time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func authorizationPolicy(requested *bool, now time.Time, minutes int) (bool, time.Time, error) {
	longTerm := false // Omitted/null requests retain the temporary policy.
	if requested != nil {
		longTerm = *requested
	}
	if longTerm {
		expires := longTermExpiresAt()
		if !expires.After(now) {
			return false, time.Time{}, ErrLeaseInactive
		}
		return true, expires, nil
	}
	return false, leaseExpiresAt(now, minutes), nil
}

func normalizeLongTermFlags(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&models.MCPLease{}) || !db.Migrator().HasColumn(&models.MCPLease{}, "long_term") {
		return nil
	}
	return db.Exec("UPDATE mcp_leases SET long_term = ? WHERE long_term IS NULL", false).Error
}

func visibleAdminLeases(db *gorm.DB, now time.Time) ([]models.MCPLease, error) {
	var leases []models.MCPLease
	err := db.Where("created_at >= ? OR (status = ? AND revoked_at IS NULL AND expires_at > ?)", adminHistoryCutoff(now), statusActive, now).
		Order("created_at DESC").Find(&leases).Error
	return leases, err
}
