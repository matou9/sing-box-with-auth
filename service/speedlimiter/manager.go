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

// UserSchedule is a runtime per-user schedule rule.
type UserSchedule struct {
	TimeRange    string
	UploadMbps   int
	DownloadMbps int
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
	groups        map[string]*speedConfig             // group name -> speed
	userGroups    map[string]string                   // user name -> group name
	userOverrides map[string]*speedConfig             // user name -> per-user override (may be partial)
	userRawConfig map[string]*option.SpeedLimiterUser // raw config for partial override detection
	schedules     []scheduleEntry
	userSchedules map[string][]scheduleEntry

	// runtime
	mu       sync.RWMutex
	limiters map[string]*UserLimiter // user name -> active limiter pair

	// schedule state
	lastCheckTime time.Time
	hasChecked    bool
	now           func() time.Time
}

// NewLimiterManager creates a LimiterManager from config options.
func NewLimiterManager(options option.SpeedLimiterServiceOptions) (*LimiterManager, error) {
	m := &LimiterManager{
		groups:        make(map[string]*speedConfig),
		userGroups:    make(map[string]string),
		userOverrides: make(map[string]*speedConfig),
		userRawConfig: make(map[string]*option.SpeedLimiterUser),
		userSchedules: make(map[string][]scheduleEntry),
		limiters:      make(map[string]*UserLimiter),
		now:           time.Now,
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

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if existing, ok := m.limiters[userName]; ok {
		return existing
	}

	cfg := m.currentSpeedLocked(userName)
	if cfg == nil {
		return nil
	}

	ul := &UserLimiter{
		Upload:   NewLimiter(cfg.UploadMbps),
		Download: NewLimiter(cfg.DownloadMbps),
	}
	if ul.Upload == nil && ul.Download == nil {
		return nil
	}

	m.limiters[userName] = ul
	return ul
}

func (m *LimiterManager) CurrentSpeed(userName string) (int, int, bool) {
	cfg := m.currentSpeed(userName)
	if cfg == nil {
		return 0, 0, false
	}
	return cfg.UploadMbps, cfg.DownloadMbps, true
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

func (m *LimiterManager) currentSpeed(userName string) *speedConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentSpeedLocked(userName)
}

func (m *LimiterManager) currentSpeedLocked(userName string) *speedConfig {
	if !m.hasChecked {
		return m.resolveConfig(userName)
	}
	return m.effectiveSpeedLocked(userName, m.lastCheckTime)
}

func (m *LimiterManager) effectiveSpeedLocked(userName string, now time.Time) *speedConfig {
	base := m.resolveConfig(userName)
	if base == nil {
		activeUserSchedule := m.activeUserScheduleLocked(userName, now)
		if activeUserSchedule == nil {
			return nil
		}
		base = &speedConfig{}
	}

	if activeGlobal := m.activeGlobalScheduleLocked(userName, now); activeGlobal != nil {
		if !m.hasFixedUserUploadOverride(userName) && activeGlobal.uploadMbps > 0 {
			base.UploadMbps = activeGlobal.uploadMbps
		}
		if !m.hasFixedUserDownloadOverride(userName) && activeGlobal.downloadMbps > 0 {
			base.DownloadMbps = activeGlobal.downloadMbps
		}
	}

	if activeUser := m.activeUserScheduleLocked(userName, now); activeUser != nil {
		if !m.hasFixedUserUploadOverride(userName) && activeUser.uploadMbps > 0 {
			base.UploadMbps = activeUser.uploadMbps
		}
		if !m.hasFixedUserDownloadOverride(userName) && activeUser.downloadMbps > 0 {
			base.DownloadMbps = activeUser.downloadMbps
		}
	}

	return base
}

func (m *LimiterManager) hasFixedUserUploadOverride(userName string) bool {
	raw := m.userRawConfig[userName]
	return raw != nil && raw.UploadMbps > 0
}

func (m *LimiterManager) hasFixedUserDownloadOverride(userName string) bool {
	raw := m.userRawConfig[userName]
	return raw != nil && raw.DownloadMbps > 0
}

func (m *LimiterManager) activeGlobalScheduleLocked(userName string, now time.Time) *scheduleEntry {
	hour, min := now.Hour(), now.Minute()
	var active *scheduleEntry
	for i := range m.schedules {
		schedule := &m.schedules[i]
		if !schedule.matchesTime(hour, min) {
			continue
		}
		if len(schedule.groups) > 0 {
			groupName, inGroup := m.userGroups[userName]
			if !inGroup || !schedule.groups[groupName] {
				continue
			}
		}
		active = schedule
	}
	return active
}

func (m *LimiterManager) activeUserScheduleLocked(userName string, now time.Time) *scheduleEntry {
	schedules := m.userSchedules[userName]
	if len(schedules) == 0 {
		return nil
	}
	hour, min := now.Hour(), now.Minute()
	var active *scheduleEntry
	for i := range schedules {
		schedule := &schedules[i]
		if schedule.matchesTime(hour, min) {
			active = schedule
		}
	}
	return active
}

func (m *LimiterManager) ReplaceUserSchedules(user string, schedules []UserSchedule) error {
	if user == "" {
		return fmt.Errorf("speed-limiter user missing name")
	}
	parsed := make([]scheduleEntry, 0, len(schedules))
	for _, schedule := range schedules {
		entry, err := parseUserSchedule(schedule)
		if err != nil {
			return err
		}
		parsed = append(parsed, entry)
	}

	m.mu.Lock()
	if len(parsed) == 0 {
		delete(m.userSchedules, user)
	} else {
		m.userSchedules[user] = parsed
	}
	m.applyRuntimeStateLocked(m.now())
	m.mu.Unlock()
	return nil
}

func (m *LimiterManager) RemoveUserSchedules(user string) error {
	if user == "" {
		return fmt.Errorf("speed-limiter user missing name")
	}
	m.mu.Lock()
	delete(m.userSchedules, user)
	m.applyRuntimeStateLocked(m.now())
	m.mu.Unlock()
	return nil
}

func (m *LimiterManager) GetUserSchedules(user string) ([]UserSchedule, bool) {
	m.mu.RLock()
	schedules, ok := m.userSchedules[user]
	if !ok || len(schedules) == 0 {
		m.mu.RUnlock()
		return nil, false
	}
	result := make([]UserSchedule, 0, len(schedules))
	for _, schedule := range schedules {
		result = append(result, schedule.toUserSchedule())
	}
	m.mu.RUnlock()
	return result, true
}

// CheckSchedules evaluates current time against schedules and updates limiter rates.
// Should be called periodically (e.g., every minute).
func (m *LimiterManager) CheckSchedules(now time.Time) {
	m.mu.Lock()
	m.applyRuntimeStateLocked(now)
	m.mu.Unlock()
}

// StartScheduleLoop starts a goroutine that checks schedules every minute.
func (m *LimiterManager) StartScheduleLoop(ctx context.Context) {
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

func (m *LimiterManager) updateLimiterRatesLocked(now time.Time) {
	for userName, limiter := range m.limiters {
		cfg := m.effectiveSpeedLocked(userName, now)
		if cfg == nil {
			delete(m.limiters, userName)
			continue
		}
		reconcileLimiterDirection(&limiter.Upload, cfg.UploadMbps)
		reconcileLimiterDirection(&limiter.Download, cfg.DownloadMbps)
		if limiter.Upload == nil && limiter.Download == nil {
			delete(m.limiters, userName)
		}
	}
}

func (m *LimiterManager) applyRuntimeStateLocked(now time.Time) {
	m.lastCheckTime = now
	m.hasChecked = true
	m.updateLimiterRatesLocked(now)
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

func parseUserSchedule(s UserSchedule) (scheduleEntry, error) {
	entry := scheduleEntry{
		uploadMbps:   s.UploadMbps,
		downloadMbps: s.DownloadMbps,
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

func (e scheduleEntry) toUserSchedule() UserSchedule {
	return UserSchedule{
		TimeRange:    fmt.Sprintf("%02d:%02d-%02d:%02d", e.startHour, e.startMin, e.endHour, e.endMin),
		UploadMbps:   e.uploadMbps,
		DownloadMbps: e.downloadMbps,
	}
}

func reconcileLimiterDirection(limiter **rate.Limiter, mbps int) {
	switch {
	case mbps > 0 && *limiter == nil:
		*limiter = NewLimiter(mbps)
	case mbps > 0:
		SetLimiterRate(*limiter, mbps)
	default:
		*limiter = nil
	}
}
