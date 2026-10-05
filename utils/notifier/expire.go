package notifier

import (
	"fmt"
	"time"

	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/models"
	messageevent "github.com/nuomiiiii/lite/database/models/messageEvent"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/expiry"
	"github.com/nuomiiiii/lite/utils/messageSender"
	"github.com/nuomiiiii/lite/utils/renewal"
)

func CheckExpireScheduledWork() {
	CheckExpire()
}

func CheckAutoRenewalScheduledWork() {
	clients_all, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return
	}
	for _, client := range clients_all {
		renewal.CheckAndAutoRenewal(client)
	}
}

func CheckExpire() {
	cfg, err := config.GetMany(map[string]any{
		config.ExpireNotificationEnabledKey:  false,
		config.ExpireNotificationLeadDaysKey: 7,
	})
	if err != nil {
		return
	}

	clients_all, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return
	}

	checkTime := time.Now().UTC()

	// 过期提醒检查（仅当启用过期通知时）
	if cfg[config.ExpireNotificationEnabledKey].(bool) {
		notificationLeadDays := int(cfg[config.ExpireNotificationLeadDaysKey].(float64)) // Json unmarshal 会将数字解析为 float64

		type clientToExpireInfo struct {
			Name     string
			DaysLeft int
			Until    string
		}

		var clientLeadToExpire []clientToExpireInfo

		for _, client := range clients_all {
			if !expiry.IsFinite(client.ExpiredAt) {
				continue
			}
			clientExpireTime := client.ExpiredAt.UTC()

			if !clientExpireTime.After(checkTime) {
				continue
			}

			notificationThreshold := checkTime.AddDate(0, 0, notificationLeadDays)

			if clientExpireTime.After(notificationThreshold) {
				continue
			}

			daysLeft := expiry.RemainingDaysCeil(clientExpireTime, checkTime)
			clientLeadToExpire = append(clientLeadToExpire, clientToExpireInfo{
				Name:     client.Name,
				DaysLeft: daysLeft,
				Until:    expiry.FormatLocalDisplay(clientExpireTime, client.ExpiryTimezone),
			})
		}

		if len(clientLeadToExpire) > 0 {
			message := ""
			for _, clientInfo := range clientLeadToExpire {
				message += fmt.Sprintf("• %s (%dd) %s\n", clientInfo.Name, clientInfo.DaysLeft, clientInfo.Until)
			}
			messageSender.SendEvent(models.EventMessage{
				Kind:    messageSender.KindExpire,
				Event:   messageevent.Expire,
				Time:    time.Now().UTC(),
				Message: message,
				Emoji:   "⏳",
			})
		}
	}

	for _, client := range clients_all {
		renewal.CheckAndAutoRenewal(client)
	}
}
