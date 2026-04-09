package speedlimiter

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/common/compatible"
	"github.com/sagernet/sing-box/option"

	"golang.org/x/time/rate"
)

// UserLimiter holds a pair of rate limiters for a single user (or user+client).
type UserLimiter struct {
	Upload     *rate.Limiter
	Download   *rate.Limiter
	lastActive atomic.Int64 // unix timestamp, for TTL cleanup
}

// touch updates the last active timestamp with the given unix time.
func (ul *UserLimiter) touch(unixTime int64) {
	ul.lastActive.Store(unixTime)
}

// UserSchedule is a runtime per-user schedule rule.
type UserSchedule struct {
	TimeRange    string `json:"time_range"`
	UploadMbps   int    `json:"upload_mbps,omitempty"`
	DownloadMbps int    `json:"download_mbps,omitempty"`
}

// speedConfig holds resolved upload/download Mbps for a user.
type speedConfig struct {
	UploadMbps   int
	DownloadMbps int
	PerClient    bool
}

// scheduleEntry is a parsed schedule rule.
type scheduleEntry struct {
	startHour, startMin int
	endHour, endMin     int
	uploadMbps          int
	downloadMbps        int
	groups              map[string]bool // empty means all groups/users
}

// clientLimiterKeySep is the separator for composite limiter keys (user|sourceIP).
// Must NOT appear in usernames or IP addresses (including IPv6).
const clientLimiterKeySep = "|"

// defaultClientTTLMinutes is the default TTL for per-client limiters.
const defaultClientTTLMinutes = 10

// configSnapshot is an immutable snapshot of all configuration data.
// Stored via atomic.Pointer for lock-free hot-path reads.
type configSnapshot struct {
	defaultConfig *speedConfig
	groups        map[string]*speedConfig             // group name -> speed
	userGroups    map[string]string                   // user name -> group name
	userOverrides map[string]*speedConfig             // user name -> per-user override
	userRawConfig map[string]*option.SpeedLimiterUser // raw config for partial override detection
	schedules     []scheduleEntry
	userSchedules map[string][]scheduleEntry
	clientTTL     time.Duration
}

// resolveConfig determines the effective speed config for a user.
// Priority: per-user override > group > default.
// Per-user override fields with value 0 fall through to group/default.
func (s *configSnapshot) resolveConfig(userName string) *speedConfig {
	var base *speedConfig

	// Start with default
	if s.defaultConfig != nil {
		base = &speedConfig{
			UploadMbps:   s.defaultConfig.UploadMbps,
			DownloadMbps: s.defaultConfig.DownloadMbps,
			PerClient:    s.defaultConfig.PerClient,
		}
	}

	// Apply group config
	if groupName, ok := s.userGroups[userName]; ok {
		if groupCfg, ok := s.groups[groupName]; ok {
			if base == nil {
				base = &speedConfig{}
			}
			if groupCfg.UploadMbps > 0 {
				base.UploadMbps = groupCfg.UploadMbps
			}
			if groupCfg.DownloadMbps > 0 {
				base.DownloadMbps = groupCfg.DownloadMbps
			}
			// Group PerClient is stored with a sentinel: true/false from *bool
			// The group's speedConfig.PerClient is set only when the group option's *bool was non-nil
			// We use a separate lookup to check if group had explicit PerClient
			base.PerClient = groupCfg.PerClient
		}
	}

	// Apply per-user override (non-zero fields only)
	if override, ok := s.userOverrides[userName]; ok {
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

	// Apply per-user PerClient override (from raw config, *bool semantics)
	if raw, ok := s.userRawConfig[userName]; ok && raw != nil && raw.PerClient != nil {
		if base == nil {
			base = &speedConfig{}
		}
		base.PerClient = *raw.PerClient
	}

	if base == nil {
		return nil
	}

	return base
}

func (s *configSnapshot) hasFixedUserUploadOverride(userName string) bool {
	raw := s.userRawConfig[userName]
	return raw != nil && raw.UploadMbps > 0
}

func (s *configSnapshot) hasFixedUserDownloadOverride(userName string) bool {
	raw := s.userRawConfig[userName]
	return raw != nil && raw.DownloadMbps > 0
}

func (s *configSnapshot) effectiveSpeed(userName string, now time.Time) *speedConfig {
	base := s.resolveConfig(userName)
	if base == nil {
		activeUserSchedule := s.activeUserSchedule(userName, now)
		if activeUserSchedule == nil {
			return nil
		}
		base = &speedConfig{}
	}

	if activeGlobal := s.activeGlobalSchedule(userName, now); activeGlobal != nil {
		if !s.hasFixedUserUploadOverride(userName) && activeGlobal.uploadMbps > 0 {
			base.UploadMbps = activeGlobal.uploadMbps
		}
		if !s.hasFixedUserDownloadOverride(userName) && activeGlobal.downloadMbps > 0 {
			base.DownloadMbps = activeGlobal.downloadMbps
		}
	}

	if activeUser := s.activeUserSchedule(userName, now); activeUser != nil {
		if !s.hasFixedUserUploadOverride(userName) && activeUser.uploadMbps > 0 {
			base.UploadMbps = activeUser.uploadMbps
		}
		if !s.hasFixedUserDownloadOverride(userName) && activeUser.downloadMbps > 0 {
			base.DownloadMbps = activeUser.downloadMbps
		}
	}

	return base
}

func (s *configSnapshot) activeGlobalSchedule(userName string, now time.Time) *scheduleEntry {
	hour, min := now.Hour(), now.Minute()
	var active *scheduleEntry
	for i := range s.schedules {
		schedule := &s.schedules[i]
		if !schedule.matchesTime(hour, min) {
			continue
		}
		if len(schedule.groups) > 0 {
			groupName, inGroup := s.userGroups[userName]
			if !inGroup || !schedule.groups[groupName] {
				continue
			}
		}
		active = schedule
	}
	return active
}

func (s *configSnapshot) activeUserSchedule(userName string, now time.Time) *scheduleEntry {
	schedules := s.userSchedules[userName]
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

// clone returns a deep copy of this snapshot for mutation during config updates.
func (s *configSnapshot) clone() *configSnapshot {
	c := &configSnapshot{
		clientTTL: s.clientTTL,
	}
	if s.defaultConfig != nil {
		dc := *s.defaultConfig
		c.defaultConfig = &dc
	}
	c.groups = make(map[string]*speedConfig, len(s.groups))
	for k, v := range s.groups {
		vc := *v
		c.groups[k] = &vc
	}
	c.userGroups = make(map[string]string, len(s.userGroups))
	for k, v := range s.userGroups {
		c.userGroups[k] = v
	}
	c.userOverrides = make(map[string]*speedConfig, len(s.userOverrides))
	for k, v := range s.userOverrides {
		vc := *v
		c.userOverrides[k] = &vc
	}
	c.userRawConfig = make(map[string]*option.SpeedLimiterUser, len(s.userRawConfig))
	for k, v := range s.userRawConfig {
		vc := *v
		c.userRawConfig[k] = &vc
	}
	c.schedules = make([]scheduleEntry, len(s.schedules))
	for i, entry := range s.schedules {
		c.schedules[i] = entry
		if len(entry.groups) > 0 {
			c.schedules[i].groups = make(map[string]bool, len(entry.groups))
			for k, v := range entry.groups {
				c.schedules[i].groups[k] = v
			}
		}
	}
	c.userSchedules = make(map[string][]scheduleEntry, len(s.userSchedules))
	for k, v := range s.userSchedules {
		entries := make([]scheduleEntry, len(v))
		copy(entries, v)
		c.userSchedules[k] = entries
	}
	return c
}

// LimiterManager manages per-user rate limiters with group and schedule support.
// Hot path is fully lock-free: atomic.Pointer for config, compatible.Map for limiters.
type LimiterManager struct {
	config   atomic.Pointer[configSnapshot]
	limiters compatible.Map[string, *UserLimiter]

	// schedule state
	lastCheckTime atomic.Int64 // unix timestamp
	hasChecked    atomic.Bool
	now           func() time.Time
}

// NewLimiterManager creates a LimiterManager from config options.
func NewLimiterManager(options option.SpeedLimiterServiceOptions) (*LimiterManager, error) {
	snap := &configSnapshot{
		groups:        make(map[string]*speedConfig),
		userGroups:    make(map[string]string),
		userOverrides: make(map[string]*speedConfig),
		userRawConfig: make(map[string]*option.SpeedLimiterUser),
		userSchedules: make(map[string][]scheduleEntry),
	}

	if options.Default != nil {
		snap.defaultConfig = &speedConfig{
			UploadMbps:   options.Default.UploadMbps,
			DownloadMbps: options.Default.DownloadMbps,
			PerClient:    options.Default.PerClient,
		}
		if options.Default.ClientTTLMinutes > 0 {
			snap.clientTTL = time.Duration(options.Default.ClientTTLMinutes) * time.Minute
		}
	}
	if snap.clientTTL == 0 {
		snap.clientTTL = defaultClientTTLMinutes * time.Minute
	}

	for _, g := range options.Groups {
		if g.Name == "" {
			return nil, fmt.Errorf("speed-limiter group missing name")
		}
		gc := &speedConfig{
			UploadMbps:   g.UploadMbps,
			DownloadMbps: g.DownloadMbps,
		}
		if g.PerClient != nil {
			gc.PerClient = *g.PerClient
		}
		snap.groups[g.Name] = gc
	}

	for i := range options.Users {
		u := &options.Users[i]
		if u.Name == "" {
			return nil, fmt.Errorf("speed-limiter user missing name")
		}
		if u.Group != "" {
			if _, ok := snap.groups[u.Group]; !ok {
				return nil, fmt.Errorf("speed-limiter user %q references unknown group %q", u.Name, u.Group)
			}
			snap.userGroups[u.Name] = u.Group
		}
		if u.UploadMbps > 0 || u.DownloadMbps > 0 {
			snap.userOverrides[u.Name] = &speedConfig{
				UploadMbps:   u.UploadMbps,
				DownloadMbps: u.DownloadMbps,
			}
		}
		snap.userRawConfig[u.Name] = u
	}

	for _, s := range options.Schedules {
		entry, err := parseSchedule(s)
		if err != nil {
			return nil, err
		}
		snap.schedules = append(snap.schedules, entry)
	}

	m := &LimiterManager{
		now: time.Now,
	}
	m.config.Store(snap)
	return m, nil
}

// GetOrCreateLimiterForClient returns the limiter for a user+sourceAddr pair.
// In per_client mode, each source IP gets an independent limiter.
// In per_user mode, sourceAddr is ignored and all connections share one limiter.
// Returns nil if no rate limit applies to this user.
func (m *LimiterManager) GetOrCreateLimiterForClient(userName string, sourceAddr netip.Addr) *UserLimiter {
	snap := m.config.Load()
	var cfg *speedConfig
	if m.hasChecked.Load() {
		cfg = snap.effectiveSpeed(userName, time.Unix(m.lastCheckTime.Load(), 0))
	} else {
		cfg = snap.resolveConfig(userName)
	}
	if cfg == nil {
		return nil
	}

	key := userName
	if cfg.PerClient && sourceAddr.IsValid() {
		key = userName + clientLimiterKeySep + sourceAddr.String()
	}

	nowUnix := m.now().Unix()

	// Fast path: existing limiter
	if ul, ok := m.limiters.Load(key); ok {
		ul.touch(nowUnix)
		return ul
	}

	// Slow path: create and try to store
	ul := &UserLimiter{
		Upload:   NewLimiter(cfg.UploadMbps),
		Download: NewLimiter(cfg.DownloadMbps),
	}
	if ul.Upload == nil && ul.Download == nil {
		return nil
	}
	ul.touch(nowUnix)

	actual, _ := m.limiters.LoadOrStore(key, ul)
	return actual
}

// GetOrCreateLimiter returns the limiter pair for a user, creating it if needed.
// Returns nil if no rate limit applies to this user.
// Backward compatible: delegates to GetOrCreateLimiterForClient with invalid addr.
func (m *LimiterManager) GetOrCreateLimiter(userName string) *UserLimiter {
	return m.GetOrCreateLimiterForClient(userName, netip.Addr{})
}

func (m *LimiterManager) CurrentSpeed(userName string) (int, int, bool) {
	snap := m.config.Load()
	var cfg *speedConfig
	if m.hasChecked.Load() {
		cfg = snap.effectiveSpeed(userName, time.Unix(m.lastCheckTime.Load(), 0))
	} else {
		cfg = snap.resolveConfig(userName)
	}
	if cfg == nil {
		return 0, 0, false
	}
	return cfg.UploadMbps, cfg.DownloadMbps, true
}

func (m *LimiterManager) ApplyConfig(user option.SpeedLimiterUser) error {
	if user.Name == "" {
		return fmt.Errorf("speed-limiter user missing name")
	}

	snap := m.config.Load()
	if user.Group != "" {
		if _, ok := snap.groups[user.Group]; !ok {
			return fmt.Errorf("speed-limiter user %q references unknown group %q", user.Name, user.Group)
		}
	}
	if user.Group == "" && user.UploadMbps <= 0 && user.DownloadMbps <= 0 {
		return fmt.Errorf("speed-limiter user %q missing runtime speed", user.Name)
	}

	newSnap := snap.clone()
	if user.Group == "" {
		delete(newSnap.userGroups, user.Name)
	} else {
		newSnap.userGroups[user.Name] = user.Group
	}
	if user.UploadMbps > 0 || user.DownloadMbps > 0 {
		newSnap.userOverrides[user.Name] = &speedConfig{
			UploadMbps:   user.UploadMbps,
			DownloadMbps: user.DownloadMbps,
		}
	} else {
		delete(newSnap.userOverrides, user.Name)
	}
	newSnap.userRawConfig[user.Name] = &user
	m.config.Store(newSnap)
	m.applyRuntimeState(m.now())
	return nil
}

func (m *LimiterManager) RemoveConfig(user string) error {
	if user == "" {
		return fmt.Errorf("speed-limiter user missing name")
	}
	snap := m.config.Load()
	newSnap := snap.clone()
	delete(newSnap.userGroups, user)
	delete(newSnap.userOverrides, user)
	delete(newSnap.userRawConfig, user)
	m.config.Store(newSnap)

	// Remove all limiters for this user (both per-user and per-client keys)
	prefix := user + clientLimiterKeySep
	m.limiters.Range(func(key string, _ *UserLimiter) bool {
		if key == user || strings.HasPrefix(key, prefix) {
			m.limiters.Delete(key)
		}
		return true
	})
	return nil
}

func (m *LimiterManager) GetConfig(user string) (option.SpeedLimiterUser, bool) {
	snap := m.config.Load()
	raw, ok := snap.userRawConfig[user]
	if !ok || raw == nil {
		return option.SpeedLimiterUser{}, false
	}
	return *raw, true
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

	snap := m.config.Load()
	newSnap := snap.clone()
	if len(parsed) == 0 {
		delete(newSnap.userSchedules, user)
	} else {
		newSnap.userSchedules[user] = parsed
	}
	m.config.Store(newSnap)
	m.applyRuntimeState(m.now())
	return nil
}

func (m *LimiterManager) RemoveUserSchedules(user string) error {
	if user == "" {
		return fmt.Errorf("speed-limiter user missing name")
	}
	snap := m.config.Load()
	newSnap := snap.clone()
	delete(newSnap.userSchedules, user)
	m.config.Store(newSnap)
	m.applyRuntimeState(m.now())
	return nil
}

func (m *LimiterManager) GetUserSchedules(user string) ([]UserSchedule, bool) {
	snap := m.config.Load()
	schedules, ok := snap.userSchedules[user]
	if !ok || len(schedules) == 0 {
		return nil, false
	}
	result := make([]UserSchedule, 0, len(schedules))
	for _, schedule := range schedules {
		result = append(result, schedule.toUserSchedule())
	}
	return result, true
}

// CheckSchedules evaluates current time against schedules and updates limiter rates.
// Should be called periodically (e.g., every minute).
func (m *LimiterManager) CheckSchedules(now time.Time) {
	m.lastCheckTime.Store(now.Unix())
	m.hasChecked.Store(true)
	m.updateLimiterRates(now)
	m.cleanExpiredClientLimiters(now)
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

// applyRuntimeState marks the config as checked and updates all limiter rates.
// Called from config-change paths (ApplyConfig, ReplaceUserSchedules, etc.).
func (m *LimiterManager) applyRuntimeState(now time.Time) {
	m.lastCheckTime.Store(now.Unix())
	m.hasChecked.Store(true)
	m.updateLimiterRates(now)
}

// updateLimiterRates updates all existing limiter rates based on current config.
func (m *LimiterManager) updateLimiterRates(now time.Time) {
	snap := m.config.Load()
	m.limiters.Range(func(key string, limiter *UserLimiter) bool {
		userName := extractUserName(key)
		cfg := snap.effectiveSpeed(userName, now)
		if cfg == nil {
			m.limiters.Delete(key)
			return true
		}
		reconcileLimiterDirection(&limiter.Upload, cfg.UploadMbps)
		reconcileLimiterDirection(&limiter.Download, cfg.DownloadMbps)
		if limiter.Upload == nil && limiter.Download == nil {
			m.limiters.Delete(key)
		}
		return true
	})
}

// cleanExpiredClientLimiters removes per-client limiters that have been inactive
// beyond the configured TTL. Per-user limiters (no separator in key) are never cleaned.
func (m *LimiterManager) cleanExpiredClientLimiters(now time.Time) {
	snap := m.config.Load()
	ttlSeconds := int64(snap.clientTTL / time.Second)
	nowUnix := now.Unix()

	m.limiters.Range(func(key string, limiter *UserLimiter) bool {
		// Only clean per-client keys (contain separator)
		if !strings.Contains(key, clientLimiterKeySep) {
			return true
		}
		lastActive := limiter.lastActive.Load()
		if lastActive > 0 && (nowUnix-lastActive) > ttlSeconds {
			m.limiters.Delete(key)
		}
		return true
	})
}

// extractUserName returns the user portion of a limiter key.
// For per-user keys ("alice"), returns the key as-is.
// For per-client keys ("alice|1.2.3.4"), returns the part before the separator.
func extractUserName(key string) string {
	if name, _, ok := strings.Cut(key, clientLimiterKeySep); ok {
		return name
	}
	return key
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
