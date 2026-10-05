package renewal

import (
	"errors"
	"fmt"
	"time"

	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/models"
	messageevent "github.com/nuomiiiii/lite/database/models/messageEvent"
	"github.com/nuomiiiii/lite/pkg/expiry"
	"github.com/nuomiiiii/lite/utils/messageSender"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
)

func CheckAndAutoRenewal(client models.Client) {
	if !client.AutoRenewal {
		return
	}
	if !agent_runtime.IsPresent(client.UUID) {
		return
	}
	if !expiry.IsFinite(client.ExpiredAt) {
		return
	}
	if client.BillingCycle <= 0 {
		return
	}

	now := time.Now().UTC()
	expiredAt := client.ExpiredAt.UTC()
	if !expiry.Due(now, expiredAt) {
		return
	}

	next, err := nextExpiry(client, now)
	if err != nil {
		auditlog.Event("", "", "renewal", "audit.renewal_fail", map[string]string{"name": client.Name, "error": err.Error()})
		return
	}

	err = clients.SaveClientWithSource(map[string]interface{}{
		"uuid":              client.UUID,
		"expired_at":        next,
		"_match_expired_at": expiredAt,
	}, "renewal")
	if errors.Is(err, clients.ErrExpiryAlreadyChanged) {
		return
	}
	if err != nil {
		auditlog.Event("", "", "renewal", "audit.renewal_fail", map[string]string{"name": client.Name, "error": err.Error()})
		return
	}

	until := expiry.FormatLocalDisplay(next, client.ExpiryTimezone)
	auditlog.Event("", "", "renewal", "audit.renewal_ok", map[string]string{
		"name":  client.Name,
		"until": until,
	})

	messageSender.SendEvent(models.EventMessage{
		Kind:    messageSender.KindRenew,
		Event:   messageevent.Renew,
		Clients: []models.Client{client},
		Time:    time.Now().UTC(),
		Emoji:   "🔄",
		Message: fmt.Sprintf("• %s until %s\n", client.Name, until),
	})
}

// nextExpiry returns the first expiry strictly after now. Overdue short
// cycles are caught up here so CheckAndAutoRenewal saves once.
func nextExpiry(client models.Client, now time.Time) (time.Time, error) {
	if client.ExpiredAt == nil {
		return time.Time{}, fmt.Errorf("expiry is not set")
	}
	return expiry.NextDue(*client.ExpiredAt, client.ExpiryTimezone, client.BillingCycle, now)
}
