package speedlimiter

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/option"

	"golang.org/x/time/rate"
)

// UserLimiter holds a pair of rate limiters for a single user.
type UserLimiter struct {
	Upload   *rate.Limiter
	Download *rate.Limiter
}

// speedConfig holds resolved upload/download Mbps for a user.
type speedConfig struct {
	UploadMbps   int
	DownloadMbps int
}

// scheduleEntry is a parsed schedule rule.
type scheduleEntry struct {
	startHour, startMin int
	endHour, endMin     int
	uploadMbps          int
	downloadMbps        int
	groups              map[string]bool // empty means all groups/users
}

// LimiterManager manages per-user rate limiters with group and schedule support.
type LimiterManager struct {
	// config
	defaultConfig *speedConfig
	groups        map[string]*speedConfig            // group name -> speed
	userGroups    map[string]string                   // user name -> group name
	userOverrides map[string]*speedConfig             // user name -> per-user override (may be partial)
	userRawConfig map[string]*option.SpeedLimiterUser // raw config for partial override detection
	schedules     []scheduleEntry

	// runtime
	mu       sync.RWMutex
	limiters map[string]*UserLimiter // user name -> active limiter pair

	// schedule state
	inSchedule    bool
	activeSchedul int // index of active schedule, -1 if none
}

// NewLimiterManager creates a LimiterManager from config options.
func NewLimiterManager(options option.SpeedLimiterServiceOptions) (*LimiterManager, error) {
	m := &LimiterManager{
		groups:        make(map[string]*speedConfig),
		userGroups:    make(map[string]string),
		userOverrides: make(map[string]*speedConfig),
		userRawConfig: make(map[string]*option.SpeedLimiterUser),
		limiters:      make(map[string]*UserLimiter),
		activeSchedul: -1,
	}

	if options.Default != nil {
		m.defaultConfig = &speedConfig{
			UploadMbps:   options.Default.UploadMbps,
			DownloadMbps: options.Default.DownloadMbps,
		}
	}

	for _, g := range options.Groups {
		if g.Name == "" {
			return nil, fmt.Errorf("speed-limiter group missing name")
		}
		m.groups[g.Name] = &speedConfig{
			UploadMbps:   g.UploadMbps,
			DownloadMbps: g.DownloadMbps,
		}
	}

	for i := range options.Users {
		u := &options.Users[i]
		if u.Name == "" {
			return nil, fmt.Errorf("speed-limiter user missing name")
		}
		if u.Group != "" {
			if _, ok := m.groups[u.Group]; !ok {
				return nil, fmt.Errorf("speed-limiter user %q references unknown group %q", u.Name, u.Group)
			}
			m.userGroups[u.Name] = u.Group
		}
		if u.UploadMbps > 0 || u.DownloadMbps > 0 {
			m.userOverrides[u.Name] = &speedConfig{
				UploadMbps:   u.UploadMbps,
				DownloadMbps: u.DownloadMbps,
			}
		}
		m.userRawConfig[u.Name] = u
	}

	for _, s := range options.Schedules {
		entry, err := parseSchedule(s)
		if err != nil {
			return nil, err
		}
		m.schedules = append(m.schedules, entry)
	}

	return m, nil
}

// GetOrCreateLimiter returns the limiter pair for a user, creating it if needed.
// Returns nil if no rate limit applies to this user.
func (m *LimiterManager) GetOrCreateLimiter(userName string) *UserLimiter {
	// Fast path: existing limiter
	m.mu.RLock()
	if ul, ok := m.limiters[userName]; ok {
		m.mu.RUnlock()
		return ul
	}
	m.mu.RUnlock()

	// Resolve config
	cfg := m.resolveConfig(userName)
	if cfg == nil {
		return nil
	}

	ul := &UserLimiter{
		Upload:   NewLimiter(cfg.UploadMbps),
		Download: NewLimiter(cfg.DownloadMbps),
	}

	// Check if both are nil (0 Mbps in both directions)
	if ul.Upload == nil && ul.Download == nil {
		return nil
	}

	m.mu.Lock()
	// Double-check after acquiring write lock
	if existing, ok := m.limiters[userName]; ok {
		m.mu.Unlock()
		return existing
	}
	m.limiters[userName] = ul
	m.mu.Unlock()
	return ul
}

// resolveConfig determines the effective speed config for a user.
// Priority: per-user override > group > default.
// Per-user override fields with value 0 fall through to group/default.
func (m *LimiterManager) resolveConfig(userName string) *speedConfig {
	var base *speedConfig

	// Start with default
	if m.defaultConfig != nil {
		base = &speedConfig{
			UploadMbps:   m.defaultConfig.UploadMbps,
			DownloadMbps: m.defaultConfig.DownloadMbps,
		}
	}

	// Apply group config
	if groupName, ok := m.userGroups[userName]; ok {
		if groupCfg, ok := m.groups[groupName]; ok {
			if base == nil {
				base = &speedConfig{}
			}
			if groupCfg.UploadMbps > 0 {
				base.UploadMbps = groupCfg.UploadMbps
			}
			if groupCfg.DownloadMbps > 0 {
				base.DownloadMbps = groupCfg.DownloadMbps
			}
		}
	}

	// Apply per-user override (non-zero fields only)
	if override, ok := m.userOverrides[userName]; ok {
		if base == nil {
			base = &speedConfig{}
		}
		if override.UploadMbps > 0 {
			base.UploadMbps = override.UploadMbps
		}
		if override.DownloadMbps > 0 {
			base.DownloadMbps = override.DownloadMbps
		}
	}

	// Check if user is known at all (in users list, or has a default)
	if base == nil {
		return nil
	}

	return base
}

// CheckSchedules evaluates current time against schedules and updates limiter rates.
// Should be called periodically (e.g., every minute).
func (m *LimiterManager) CheckSchedules(now time.Time) {
	if len(m.schedules) == 0 {
		return
	}

	hour, min := now.Hour(), now.Minute()
	activeIdx := -1

	// Find the last matching schedule (later schedules have higher priority)
	for i := range m.schedules {
		if m.schedules[i].matchesTime(hour, min) {
			activeIdx = i
		}
	}

	if activeIdx == m.activeSchedul {
		return // no change
	}

	m.activeSchedul = activeIdx

	m.mu.RLock()
	defer m.mu.RUnlock()

	if activeIdx >= 0 {
		// Apply schedule rates
		sched := &m.schedules[activeIdx]
		for userName, ul := range m.limiters {
			m.applyScheduleToUser(sched, userName, ul)
		}
	} else {
		// Restore default rates
		for userName, ul := range m.limiters {
			m.restoreDefaultRates(userName, ul)
		}
	}
}

// applyScheduleToUser applies schedule rates to a user's limiter,
// respecting per-user override priority.
func (m *LimiterManager) applyScheduleToUser(sched *scheduleEntry, userName string, ul *UserLimiter) {
	// Check if schedule targets specific groups
	if len(sched.groups) > 0 {
		groupName, inGroup := m.userGroups[userName]
		if !inGroup || !sched.groups[groupName] {
			return // user not in targeted groups
		}
	}

	// Per-user override always takes priority over schedule
	raw := m.userRawConfig[userName]

	// Apply upload schedule rate only if user has no per-user upload override
	if sched.uploadMbps > 0 && (raw == nil || raw.UploadMbps == 0) {
		if ul.Upload != nil {
			SetLimiterRate(ul.Upload, sched.uploadMbps)
		}
	}

	// Apply download schedule rate only if user has no per-user download override
	if sched.downloadMbps > 0 && (raw == nil || raw.DownloadMbps == 0) {
		if ul.Download != nil {
			SetLimiterRate(ul.Download, sched.downloadMbps)
		}
	}
}

// restoreDefaultRates restores a user's limiter to their base config rates.
func (m *LimiterManager) restoreDefaultRates(userName string, ul *UserLimiter) {
	cfg := m.resolveConfig(userName)
	if cfg == nil {
		return
	}
	if ul.Upload != nil && cfg.UploadMbps > 0 {
		SetLimiterRate(ul.Upload, cfg.UploadMbps)
	}
	if ul.Download != nil && cfg.DownloadMbps > 0 {
		SetLimiterRate(ul.Download, cfg.DownloadMbps)
	}
}

// StartScheduleLoop starts a goroutine that checks schedules every minute.
func (m *LimiterManager) StartScheduleLoop(ctx context.Context) {
	if len(m.schedules) == 0 {
		return
	}
	// Initial check
	m.CheckSchedules(time.Now())
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				m.CheckSchedules(t)
			}
		}
	}()
}

// parseSchedule parses a SpeedLimiterSchedule into a scheduleEntry.
func parseSchedule(s option.SpeedLimiterSchedule) (scheduleEntry, error) {
	entry := scheduleEntry{
		uploadMbps:   s.UploadMbps,
		downloadMbps: s.DownloadMbps,
	}

	if len(s.Groups) > 0 {
		entry.groups = make(map[string]bool)
		for _, g := range s.Groups {
			entry.groups[g] = true
		}
	}

	parts := strings.SplitN(s.TimeRange, "-", 2)
	if len(parts) != 2 {
		return entry, fmt.Errorf("invalid time_range format %q, expected HH:MM-HH:MM", s.TimeRange)
	}

	var err error
	entry.startHour, entry.startMin, err = parseTime(parts[0])
	if err != nil {
		return entry, fmt.Errorf("invalid time_range start %q: %w", parts[0], err)
	}
	entry.endHour, entry.endMin, err = parseTime(parts[1])
	if err != nil {
		return entry, fmt.Errorf("invalid time_range end %q: %w", parts[1], err)
	}

	return entry, nil
}

func parseTime(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM format")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour %q", parts[0])
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil || min < 0 || min > 59 {
		return 0, 0, fmt.Errorf("invalid minute %q", parts[1])
	}
	return hour, min, nil
}

// matchesTime checks if the given hour:min falls within this schedule's time range.
// Supports cross-midnight ranges (e.g., 23:00-06:00).
func (e *scheduleEntry) matchesTime(hour, min int) bool {
	now := hour*60 + min
	start := e.startHour*60 + e.startMin
	end := e.endHour*60 + e.endMin

	if start <= end {
		// Normal range: e.g., 08:00-18:00
		return now >= start && now < end
	}
	// Cross-midnight range: e.g., 23:00-06:00
	return now >= start || now < end
}
