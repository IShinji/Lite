package messageSender

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	logger "github.com/nuomiiiii/lite/utils/log"

	"github.com/nuomiiiii/lite/database"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils/messageSender/factory"
	"gorm.io/gorm"
)

var (
	providers                = map[string]factory.IMessageSender{}
	mu                       = sync.Mutex{}
	once                     = sync.Once{}
	loadNotificationSettings = config.GetMany
	writeSendAudit           = auditlog.Event
	channelsForKind          = storedChannels
)

func CurrentProvider() factory.IMessageSender {
	mu.Lock()
	defer mu.Unlock()
	if provider, ok := providers["email"]; ok {
		return provider
	}
	for _, provider := range providers {
		return provider
	}
	return nil
}

func providerByName(name string) factory.IMessageSender {
	mu.Lock()
	defer mu.Unlock()
	return providers[name]
}

// Shutdown 销毁已加载的消息发送 provider。供关闭流程调用。
func Shutdown() error {
	flushDigestBatches()
	mu.Lock()
	defer mu.Unlock()
	var errs []error
	for name, provider := range providers {
		if provider != nil {
			if err := provider.Destroy(); err != nil {
				errs = append(errs, err)
			}
		}
		delete(providers, name)
	}
	return errors.Join(errs...)
}

func Initialize() {
	go func() {
		once.Do(func() {
			all := factory.GetAllMessageSenders()
			for _, provider := range all {
				if _, err := database.GetMessageSenderConfigByName(provider.GetName()); err == nil {
					continue
				}
				// 如果数据库中没有该提供者的配置，则保存默认配置
				config := provider.GetConfiguration()
				configBytes, err := json.Marshal(config)
				if err != nil {
					logger.Errorf("message-sender", "Failed to marshal config for provider %s: %v", provider.GetName(), err)
					return
				}
				if err := database.SaveMessageSenderConfig(&models.MessageSenderProvider{
					Name:     provider.GetName(),
					Addition: string(configBytes),
				}); err != nil {
					logger.Errorf("message-sender", "Failed to save default config for provider %s: %v", provider.GetName(), err)
					return
				}
			}
		})
	}()
	if err := EnsureRoutes(); err != nil {
		logger.Errorf("message-sender", "Failed to prepare notification routes: %v", err)
	}
	if err := SyncProviders(); err != nil {
		logger.Errorf("message-sender", "Failed to load notification channels: %v", err)
	}
}

func SendEvent(event models.EventMessage) error {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	} else {
		event.Time = event.Time.UTC()
	}
	cfg, template, err := notificationDeliverySettings()
	if err != nil {
		return err
	}
	enabled, _ := cfg[config.NotificationEnabledKey].(bool)
	if !enabled {
		return nil
	}
	if strings.TrimSpace(event.Kind) == "" {
		return fmt.Errorf("notification kind is required")
	}
	channels, err := channelsForKind(event.Kind)
	if err != nil {
		return err
	}
	if len(channels) == 0 {
		return nil
	}
	if digestOn, window := digestFrom(cfg); digestOn && digestibleKind(event.Kind) {
		// Return before the window closes. A caller that sends servers one
		// after another, such as the load alert loop, must join the same batch.
		enqueueDigest(event, window)
		return nil
	}
	return deliverPrepared(event, channels, template)
}

func deliverEvent(event models.EventMessage) error {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	} else {
		event.Time = event.Time.UTC()
	}
	cfg, template, err := notificationDeliverySettings()
	if err != nil {
		return err
	}
	enabled, _ := cfg[config.NotificationEnabledKey].(bool)
	if !enabled {
		return nil
	}
	if strings.TrimSpace(event.Kind) == "" {
		return fmt.Errorf("notification kind is required")
	}
	channels, err := channelsForKind(event.Kind)
	if err != nil {
		return err
	}
	if len(channels) == 0 {
		return nil
	}
	return deliverPrepared(event, channels, template)
}

func deliverPrepared(event models.EventMessage, channels []string, template string) error {
	var failures []error
	sent := 0
	for _, name := range channels {
		if err := deliverTo(name, event, template); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
			writeSendAudit("", "", "error", "audit.event_fail", map[string]string{
				"event": event.Event,
				"error": name + ": " + err.Error(),
			})
			logger.Errorf("message-sender", "Failed to send %s via %s: %v", event.Event, name, err)
			continue
		}
		sent++
		writeSendAudit("", "", "info", "audit.event_ok", map[string]string{
			"event":   event.Event,
			"channel": name,
		})
	}
	if sent == 0 {
		return errors.Join(failures...)
	}
	return nil
}

// SendTestEventTo delivers a manual test to one channel.
// The master notification switch and the route table do not apply.
func SendTestEventTo(name string, event models.EventMessage) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "none" || name == "empty" {
		return fmt.Errorf("provider is required")
	}
	if _, ok := factory.GetConstructor(name); !ok {
		return fmt.Errorf("message sender provider not found: %s", name)
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	} else {
		event.Time = event.Time.UTC()
	}
	_, template, err := notificationDeliverySettings()
	if err != nil {
		template = "{{emoji}}{{emoji}}{{emoji}}\nEvent: {{event}}\nClients: {{client}}\nMessage: {{message}}\nTime: {{time}}"
	}
	if err := deliverTo(name, event, template); err != nil {
		writeSendAudit("", "", "error", "audit.event_fail", map[string]string{
			"event": event.Event,
			"error": name + ": " + err.Error(),
		})
		return err
	}
	writeSendAudit("", "", "info", "audit.event_ok", map[string]string{
		"event":   event.Event,
		"channel": name,
	})
	return nil
}

func notificationDeliverySettings() (map[string]any, string, error) {
	cfg, err := loadNotificationSettings(map[string]any{
		config.NotificationEnabledKey:       false,
		config.NotificationTemplateKey:      "{{emoji}}{{emoji}}{{emoji}}\nEvent: {{event}}\nClients: {{client}}\nMessage: {{message}}\nTime: {{time}}",
		config.NotificationDigestEnabledKey: false,
		config.NotificationDigestSecondsKey: DefaultDigestSeconds,
	})
	if err != nil {
		return nil, "", err
	}
	template, _ := cfg[config.NotificationTemplateKey].(string)
	return cfg, template, nil
}

// SyncProviders loads every channel selected by the route table and drops the rest.
func SyncProviders() error {
	routes, err := config.GetAs[map[string][]string](config.NotificationRoutesKey)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	needed := map[string]struct{}{}
	for _, channels := range routes {
		for _, name := range channels {
			if name == "" {
				continue
			}
			needed[name] = struct{}{}
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for name, provider := range providers {
		if _, ok := needed[name]; ok {
			continue
		}
		if provider != nil {
			_ = provider.Destroy()
		}
		delete(providers, name)
	}
	var errs []error
	for name := range needed {
		cfg, lookupErr := database.GetMessageSenderConfigByName(name)
		if lookupErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, lookupErr))
			continue
		}
		if err := loadProviderFromAdditionLocked(name, cfg.Addition); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func deliverTo(name string, event models.EventMessage, template string) error {
	provider := providerByName(name)
	if provider == nil {
		cfg, err := database.GetMessageSenderConfigByName(name)
		if err != nil {
			return err
		}
		if err := LoadProvider(name, cfg.Addition); err != nil {
			return err
		}
		provider = providerByName(name)
	}
	if provider == nil {
		return fmt.Errorf("message sender %s is not loaded", name)
	}
	var err error
	if eventSender, ok := provider.(factory.IEventMessageSender); ok {
		for range 3 {
			err = eventSender.SendEvent(event)
			if deliverySucceeded(err) {
				return nil
			}
		}
		return err
	}
	message := parseTemplate(template, event)
	for range 3 {
		err = provider.SendTextMessage(message, event.Event)
		if deliverySucceeded(err) {
			return nil
		}
	}
	return err
}

func deliverySucceeded(err error) bool {
	return err == nil || err.Error() == "short response: \x00\x00\x00\x1a\x00\x00\x00"
}

func parseTemplate(messageTemplate string, event models.EventMessage) string {
	// Aggregate client names. If Name is empty, fall back to UUID.
	clientNames := make([]string, 0, len(event.Clients))
	for _, c := range event.Clients {
		name := c.Name
		if strings.TrimSpace(name) == "" {
			// fallback to UUID when name is not set
			name = c.UUID
		}
		clientNames = append(clientNames, name)
	}
	joinedClients := strings.Join(clientNames, ", ")

	replaceMap := map[string]string{
		"{{event}}":   event.Event,
		"{{client}}":  joinedClients,
		"{{time}}":    event.Time.UTC().Format(time.RFC3339Nano),
		"{{message}}": event.Message,
		"{{emoji}}":   event.Emoji,
	}
	result := messageTemplate
	for placeholder, value := range replaceMap {
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}
