package clients

import (
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/models"
)

// clientListCacheTTL covers the admin server list refresh. A repeat open or
// that refresh reuses the last read. Edits drop the cache immediately.
//
// clientListCacheTTL 盖住后台服务器列表的自动刷新。再次打开或这次刷新沿用
// 上一次的结果。编辑会立刻丢掉缓存。
const clientListCacheTTL = 20 * time.Second

type clientBasicInfoCache struct {
	mu       sync.Mutex
	storedAt time.Time
	clients  []models.Client
	valid    bool
}

func (c *clientBasicInfoCache) get(now time.Time, ttl time.Duration) ([]models.Client, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid || now.Sub(c.storedAt) >= ttl {
		return nil, false
	}
	return cloneClientList(c.clients), true
}

func (c *clientBasicInfoCache) set(now time.Time, list []models.Client) {
	c.mu.Lock()
	c.storedAt = now
	c.clients = cloneClientList(list)
	c.valid = true
	c.mu.Unlock()
}

func (c *clientBasicInfoCache) invalidate() {
	c.mu.Lock()
	c.valid = false
	c.clients = nil
	c.mu.Unlock()
}

var basicInfoCache clientBasicInfoCache

func invalidateClientListCache() {
	basicInfoCache.invalidate()
}

func cloneClientList(in []models.Client) []models.Client {
	out := make([]models.Client, len(in))
	for index := range in {
		out[index] = in[index]
		if in[index].TrafficResetDay != nil {
			day := *in[index].TrafficResetDay
			out[index].TrafficResetDay = &day
		}
		if in[index].ExpiredAt != nil {
			stamp := *in[index].ExpiredAt
			out[index].ExpiredAt = &stamp
		}
		if in[index].PreviousTokenExpiresAt != nil {
			stamp := *in[index].PreviousTokenExpiresAt
			out[index].PreviousTokenExpiresAt = &stamp
		}
	}
	return out
}
