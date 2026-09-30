package notificationdefaults

import (
	"fmt"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OfflineNotificationDefaultConfig struct {
	Enabled     bool `json:"enabled"`
	GracePeriod int  `json:"grace_period"`
}

type PingLossNotificationDefaultConfig struct {
	Enabled         bool    `json:"enabled"`
	WindowSeconds   int     `json:"window_seconds"`
	LossThreshold   float64 `json:"loss_threshold"`
	MinimumSamples  int     `json:"minimum_samples"`
	CooldownSeconds int     `json:"cooldown_seconds"`
}

type LatencyAnomalyNotificationDefaultConfig struct {
	SchemaVersion          int     `json:"schema_version"`
	Enabled                bool    `json:"enabled"`
	WindowSeconds          int     `json:"window_seconds"`
	MinimumSamples         int     `json:"minimum_samples"`
	CooldownSeconds        int     `json:"cooldown_seconds"`
	LowerDeviationPercent  float64 `json:"lower_deviation_percent"`
	UpperDeviationPercent  float64 `json:"upper_deviation_percent"`
	BaselineWindowSeconds  int     `json:"baseline_window_seconds"`
	BaselineMinimumSamples int     `json:"baseline_minimum_samples"`
}

type TrafficReportDefaultConfig struct {
	Enabled        bool `json:"enabled"`
	Daily          bool `json:"daily"`
	Weekly         bool `json:"weekly"`
	Monthly        bool `json:"monthly"`
	IncludeTraffic bool `json:"include_traffic"`
	IncludeBilling bool `json:"include_billing"`
}

var defaultOfflineNotificationConfig = OfflineNotificationDefaultConfig{
	Enabled:     false,
	GracePeriod: 180,
}

var defaultPingLossNotificationConfig = PingLossNotificationDefaultConfig{
	Enabled:         false,
	WindowSeconds:   60,
	LossThreshold:   5,
	MinimumSamples:  1,
	CooldownSeconds: 300,
}

var defaultLatencyAnomalyNotificationConfig = LatencyAnomalyNotificationDefaultConfig{
	SchemaVersion:          2,
	Enabled:                false,
	WindowSeconds:          300,
	MinimumSamples:         30,
	CooldownSeconds:        1800,
	LowerDeviationPercent:  25,
	UpperDeviationPercent:  25,
	BaselineWindowSeconds:  86400,
	BaselineMinimumSamples: 30,
}

var defaultTrafficReportConfig = TrafficReportDefaultConfig{
	Enabled:        false,
	Daily:          true,
	Weekly:         false,
	Monthly:        false,
	IncludeTraffic: true,
	IncludeBilling: false,
}

var reconcileTrafficReportRetention = func() error { return nil }

func RegisterTrafficReportRetentionReconciler(reconcile func() error) {
	if reconcile != nil {
		reconcileTrafficReportRetention = reconcile
	}
}

func GetOfflineNotificationDefaultConfig() (OfflineNotificationDefaultConfig, error) {
	return config.GetAs[OfflineNotificationDefaultConfig](config.OfflineNotificationDefaultKey, defaultOfflineNotificationConfig)
}

func SetOfflineNotificationDefaultConfig(value OfflineNotificationDefaultConfig) error {
	if value.GracePeriod <= 0 {
		return fmt.Errorf("grace period must be a positive integer")
	}
	return config.Set(config.OfflineNotificationDefaultKey, value)
}

func GetPingLossNotificationDefaultConfig() (PingLossNotificationDefaultConfig, error) {
	return config.GetAs[PingLossNotificationDefaultConfig](config.PingLossNotificationDefaultKey, defaultPingLossNotificationConfig)
}

func SetPingLossNotificationDefaultConfig(value PingLossNotificationDefaultConfig) error {
	if err := validatePingLossNotificationDefaultConfig(value); err != nil {
		return err
	}
	return config.Set(config.PingLossNotificationDefaultKey, value)
}

func GetLatencyAnomalyNotificationDefaultConfig() (LatencyAnomalyNotificationDefaultConfig, error) {
	value, err := config.GetAs[LatencyAnomalyNotificationDefaultConfig](config.LatencyAnomalyNotificationDefaultKey, defaultLatencyAnomalyNotificationConfig)
	if err != nil {
		return LatencyAnomalyNotificationDefaultConfig{}, err
	}
	upgraded, changed := normalizeLatencyAnomalyDefault(value)
	if !changed {
		return upgraded, nil
	}
	if err := config.Set(config.LatencyAnomalyNotificationDefaultKey, upgraded); err != nil {
		return LatencyAnomalyNotificationDefaultConfig{}, err
	}
	return upgraded, nil
}

// normalizeLatencyAnomalyDefault moves an untouched previous factory default
// (3 successful samples, 20% deviation) onto the current default.
func normalizeLatencyAnomalyDefault(value LatencyAnomalyNotificationDefaultConfig) (LatencyAnomalyNotificationDefaultConfig, bool) {
	if value.SchemaVersion >= 2 {
		return value, false
	}
	if value.MinimumSamples != 3 || value.LowerDeviationPercent != 20 || value.UpperDeviationPercent != 20 {
		return value, false
	}
	value.SchemaVersion = defaultLatencyAnomalyNotificationConfig.SchemaVersion
	value.MinimumSamples = defaultLatencyAnomalyNotificationConfig.MinimumSamples
	value.LowerDeviationPercent = defaultLatencyAnomalyNotificationConfig.LowerDeviationPercent
	value.UpperDeviationPercent = defaultLatencyAnomalyNotificationConfig.UpperDeviationPercent
	return value, true
}

func SetLatencyAnomalyNotificationDefaultConfig(value LatencyAnomalyNotificationDefaultConfig) error {
	if err := validateLatencyAnomalyNotificationDefaultConfig(value); err != nil {
		return err
	}
	if value.SchemaVersion == 0 {
		value.SchemaVersion = defaultLatencyAnomalyNotificationConfig.SchemaVersion
	}
	return config.Set(config.LatencyAnomalyNotificationDefaultKey, value)
}

func GetTrafficReportDefaultConfig() (TrafficReportDefaultConfig, error) {
	return config.GetAs[TrafficReportDefaultConfig](config.TrafficReportDefaultKey, defaultTrafficReportConfig)
}

func SetTrafficReportDefaultConfig(value TrafficReportDefaultConfig) error {
	if err := validateTrafficReportDefaultConfig(value); err != nil {
		return err
	}
	return config.Set(config.TrafficReportDefaultKey, value)
}

func validatePingLossNotificationDefaultConfig(value PingLossNotificationDefaultConfig) error {
	if value.WindowSeconds < 60 || value.WindowSeconds > 24*60*60 {
		return fmt.Errorf("window must be between 60 and 86400 seconds")
	}
	if value.LossThreshold <= 0 || value.LossThreshold > 100 {
		return fmt.Errorf("loss threshold must be greater than 0 and at most 100")
	}
	if value.MinimumSamples < 1 || value.MinimumSamples > 100000 {
		return fmt.Errorf("minimum samples must be between 1 and 100000")
	}
	if value.CooldownSeconds < 60 || value.CooldownSeconds > 7*24*60*60 {
		return fmt.Errorf("cooldown must be between 60 and 604800 seconds")
	}
	return nil
}

func validateLatencyAnomalyNotificationDefaultConfig(value LatencyAnomalyNotificationDefaultConfig) error {
	if value.WindowSeconds < 60 || value.WindowSeconds > 24*60*60 {
		return fmt.Errorf("latency window must be between 60 and 86400 seconds")
	}
	if value.MinimumSamples < 1 || value.MinimumSamples > 100000 {
		return fmt.Errorf("latency minimum samples must be between 1 and 100000")
	}
	if value.CooldownSeconds < 60 || value.CooldownSeconds > 7*24*60*60 {
		return fmt.Errorf("latency cooldown must be between 60 and 604800 seconds")
	}
	if value.LowerDeviationPercent <= 0 || value.LowerDeviationPercent >= 100 {
		return fmt.Errorf("lower deviation percent must be greater than 0 and less than 100")
	}
	if value.UpperDeviationPercent <= 0 || value.UpperDeviationPercent > 1000 {
		return fmt.Errorf("upper deviation percent must be greater than 0 and at most 1000")
	}
	if value.BaselineWindowSeconds < value.WindowSeconds || value.BaselineWindowSeconds > 30*24*60*60 {
		return fmt.Errorf("baseline window must be between the latency window and 30 days")
	}
	if value.BaselineMinimumSamples < 1 || value.BaselineMinimumSamples > 1000000 {
		return fmt.Errorf("baseline minimum samples must be between 1 and 1000000")
	}
	return nil
}

func validateTrafficReportDefaultConfig(value TrafficReportDefaultConfig) error {
	if value.Enabled && !value.Daily && !value.Weekly && !value.Monthly {
		return fmt.Errorf("at least one cadence must be selected when enabling traffic reports")
	}
	if value.Enabled && !value.IncludeTraffic && !value.IncludeBilling {
		return fmt.Errorf("at least one report content type must be selected when enabling traffic reports")
	}
	return nil
}

func ApplyDefaultsToNewClient(clientUUID string) error {
	trafficReportApplied, err := applyDefaultsToNewClient(dbcore.GetDBInstance(), clientUUID)
	if err != nil {
		return err
	}
	if trafficReportApplied {
		return reconcileTrafficReportRetention()
	}
	return nil
}

func applyDefaultsToNewClient(db *gorm.DB, clientUUID string) (bool, error) {
	if clientUUID == "" {
		return false, nil
	}
	offlineConfig, err := GetOfflineNotificationDefaultConfig()
	if err != nil {
		return false, fmt.Errorf("load offline notification default: %w", err)
	}
	pingLossConfig, err := GetPingLossNotificationDefaultConfig()
	if err != nil {
		return false, fmt.Errorf("load ping loss notification default: %w", err)
	}
	latencyConfig, err := GetLatencyAnomalyNotificationDefaultConfig()
	if err != nil {
		return false, fmt.Errorf("load latency anomaly notification default: %w", err)
	}
	trafficReportConfig, err := GetTrafficReportDefaultConfig()
	if err != nil {
		return false, fmt.Errorf("load traffic report default: %w", err)
	}
	trafficReportApplied := false

	err = db.Transaction(func(tx *gorm.DB) error {
		if offlineConfig.Enabled {
			offline := models.OfflineNotification{
				Client:      clientUUID,
				Enable:      true,
				GracePeriod: offlineConfig.GracePeriod,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "client"}},
				DoNothing: true,
			}).Select("client", "enable", "grace_period").Create(&offline).Error; err != nil {
				return fmt.Errorf("apply offline notification default: %w", err)
			}
		}

		if pingLossConfig.Enabled || latencyConfig.Enabled {
			var pingTasks []models.PingTask
			if err := tx.Where("clients LIKE ?", `%"`+clientUUID+`"%`).Find(&pingTasks).Error; err != nil {
				return fmt.Errorf("find ping tasks for notification defaults: %w", err)
			}
			for _, task := range pingTasks {
				if !task.AppliesToClient(clientUUID) {
					continue
				}
				if err := applyPingHealthDefaultToClients(tx, pingLossConfig, latencyConfig, task.Id, []string{clientUUID}); err != nil {
					return err
				}
			}
		}

		if trafficReportConfig.Enabled {
			candidate := models.TrafficReportNotification{
				Client:         clientUUID,
				Enable:         true,
				Daily:          trafficReportConfig.Daily,
				Weekly:         trafficReportConfig.Weekly,
				Monthly:        trafficReportConfig.Monthly,
				IncludeTraffic: trafficReportConfig.IncludeTraffic,
				IncludeBilling: trafficReportConfig.IncludeBilling,
			}
			result := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "client"}},
				DoNothing: true,
			}).Select(
				"client", "enable", "daily", "weekly", "monthly", "include_traffic", "include_billing",
			).Create(&candidate)
			if result.Error != nil {
				return fmt.Errorf("apply traffic report default: %w", result.Error)
			}
			trafficReportApplied = result.RowsAffected > 0
		}
		return nil
	})
	return trafficReportApplied, err
}

// ApplyLoadedPingHealthDefaultsToTaskClients 使用已经读好的丢包/延迟默认配置写入新的任务/服务器组合。
// 调用方必须在事务外完成配置读取，避免 SQLite 单连接卡死。
func ApplyLoadedPingHealthDefaultsToTaskClients(
	db *gorm.DB,
	lossCfg PingLossNotificationDefaultConfig,
	latencyCfg LatencyAnomalyNotificationDefaultConfig,
	taskID uint,
	clients []string,
) error {
	return applyPingHealthDefaultToClients(db, lossCfg, latencyCfg, taskID, clients)
}

// ApplyLoadedPingLossDefaultsToTaskClients keeps the previous helper for callers
// that only loaded the packet-loss default. Latency defaults stay off.
func ApplyLoadedPingLossDefaultsToTaskClients(db *gorm.DB, cfg PingLossNotificationDefaultConfig, taskID uint, clients []string) error {
	return applyPingHealthDefaultToClients(db, cfg, defaultLatencyAnomalyNotificationConfig, taskID, clients)
}

func applyPingHealthDefaultToClients(
	tx *gorm.DB,
	lossCfg PingLossNotificationDefaultConfig,
	latencyCfg LatencyAnomalyNotificationDefaultConfig,
	taskID uint,
	clients []string,
) error {
	if tx == nil || taskID == 0 || (!lossCfg.Enabled && !latencyCfg.Enabled) {
		return nil
	}
	seen := make(map[string]struct{}, len(clients))
	for _, clientUUID := range clients {
		if clientUUID == "" {
			continue
		}
		if _, ok := seen[clientUUID]; ok {
			continue
		}
		seen[clientUUID] = struct{}{}
		candidate := models.PingLossNotification{
			Client:                        clientUUID,
			TaskId:                        taskID,
			Enable:                        true,
			LossEnabled:                   lossCfg.Enabled,
			WindowSeconds:                 lossCfg.WindowSeconds,
			LossThreshold:                 lossCfg.LossThreshold,
			MinimumSamples:                lossCfg.MinimumSamples,
			CooldownSeconds:               lossCfg.CooldownSeconds,
			LatencyEnabled:                latencyCfg.Enabled,
			AdaptiveBaselineEnabled:       latencyCfg.Enabled,
			LatencyWindowSeconds:          latencyCfg.WindowSeconds,
			LatencyMinimumSamples:         latencyCfg.MinimumSamples,
			LatencyCooldownSeconds:        latencyCfg.CooldownSeconds,
			AdaptiveLowerDeviationPercent: latencyCfg.LowerDeviationPercent,
			AdaptiveUpperDeviationPercent: latencyCfg.UpperDeviationPercent,
			BaselineWindowSeconds:         latencyCfg.BaselineWindowSeconds,
			BaselineMinimumSamples:        latencyCfg.BaselineMinimumSamples,
			LatencyAlertState:             models.LatencyAlertNormal,
			AdaptiveBaselineStatus:        models.AdaptiveBaselineWarming,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client"}, {Name: "task_id"}},
			DoNothing: true,
		}).Select(
			"client", "task_id", "enable", "loss_enabled", "window_seconds", "loss_threshold",
			"minimum_samples", "cooldown_seconds", "latency_enabled", "adaptive_baseline_enabled",
			"latency_window_seconds", "latency_minimum_samples", "latency_cooldown_seconds",
			"adaptive_lower_deviation_percent", "adaptive_upper_deviation_percent",
			"baseline_window_seconds", "baseline_minimum_samples", "latency_alert_state",
			"adaptive_baseline_status",
		).Create(&candidate).Error; err != nil {
			return fmt.Errorf("apply ping health notification default: %w", err)
		}
	}
	return nil
}
