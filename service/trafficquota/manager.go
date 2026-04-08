package trafficquota

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/common/compatible"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

type PeriodType string

const (
	PeriodDaily   PeriodType = "daily"
	PeriodWeekly  PeriodType = "weekly"
	PeriodMonthly PeriodType = "monthly"
	PeriodCustom  PeriodType = "custom"
)

type QuotaConfig struct {
	quotaBytes  int64
	period      PeriodType
	periodStart time.Time
	periodDays  int
}

type RuntimeState struct {
	User         option.TrafficQuotaUser
	UsageBytes   int64
	PendingDelta int64
	Exceeded     bool
	PeriodKey    string
	activeConns  *connList
}

type quotaTrackedConn interface {
	markQuotaExceeded()
}

type userState struct {
	usage        atomic.Int64
	pendingDelta atomic.Int64
	exceeded     atomic.Bool
	periodAccess sync.RWMutex
	periodKey    string
}

type PeriodReset struct {
	User          string
	PreviousKey   string
	CurrentPeriod string
}

type Status struct {
	UsageBytes int64
	QuotaBytes int64
	Exceeded   bool
}

type connList struct {
	access sync.Mutex
	conns  []quotaTrackedConn
}

func (l *connList) add(conn quotaTrackedConn) {
	l.access.Lock()
	l.conns = append(l.conns, conn)
	l.access.Unlock()
}

func (l *connList) remove(conn quotaTrackedConn) {
	l.access.Lock()
	defer l.access.Unlock()
	for i := range l.conns {
		if l.conns[i] == conn {
			l.conns = append(l.conns[:i], l.conns[i+1:]...)
			return
		}
	}
}

func (l *connList) closeAll() {
	l.access.Lock()
	conns := append([]quotaTrackedConn(nil), l.conns...)
	l.access.Unlock()
	for _, conn := range conns {
		conn.markQuotaExceeded()
	}
}

func (l *connList) len() int {
	l.access.Lock()
	defer l.access.Unlock()
	return len(l.conns)
}

func (s *userState) getPeriodKey() string {
	s.periodAccess.RLock()
	defer s.periodAccess.RUnlock()
	return s.periodKey
}

func (s *userState) setPeriodKey(periodKey string) {
	s.periodAccess.Lock()
	s.periodKey = periodKey
	s.periodAccess.Unlock()
}

func (s *userState) setPeriodKeyIfEmpty(periodKey string) {
	s.periodAccess.Lock()
	if s.periodKey == "" {
		s.periodKey = periodKey
	}
	s.periodAccess.Unlock()
}

type QuotaManager struct {
	userConfigAccess sync.RWMutex
	userConfigs      map[string]*QuotaConfig
	activeConns      compatible.Map[string, *connList]
	states           compatible.Map[string, *userState]
	now              func() time.Time
}

func NewQuotaManager(options option.TrafficQuotaServiceOptions) (*QuotaManager, error) {
	groupConfigs := make(map[string]*QuotaConfig, len(options.Groups))
	for _, group := range options.Groups {
		if group.Name == "" {
			return nil, E.New("traffic-quota group missing name")
		}
		config, err := newQuotaConfig(group.QuotaGB, group.Period, group.PeriodStart, group.PeriodDays, nil)
		if err != nil {
			return nil, E.Cause(err, "invalid traffic-quota group ", group.Name)
		}
		groupConfigs[group.Name] = config
	}

	userConfigs := make(map[string]*QuotaConfig, len(options.Users))
	for _, user := range options.Users {
		if user.Name == "" {
			return nil, E.New("traffic-quota user missing name")
		}
		var base *QuotaConfig
		if user.Group != "" {
			groupConfig, loaded := groupConfigs[user.Group]
			if !loaded {
				return nil, E.New("traffic-quota user ", user.Name, " references unknown group ", user.Group)
			}
			groupCopy := *groupConfig
			base = &groupCopy
		}
		config, err := newQuotaConfig(user.QuotaGB, user.Period, user.PeriodStart, user.PeriodDays, base)
		if err != nil {
			return nil, E.Cause(err, "invalid traffic-quota user ", user.Name)
		}
		if config != nil {
			userConfigs[user.Name] = config
		}
	}

	return &QuotaManager{
		userConfigs: userConfigs,
		now:         time.Now,
	}, nil
}

func (m *QuotaManager) RegisterConn(user string, conn quotaTrackedConn) (func(int), func()) {
	if _, loaded := m.loadConfig(user); !loaded {
		return nil, nil
	}
	state := m.stateFor(user)
	state.setPeriodKeyIfEmpty(m.mustPeriodKey(user, m.now()))
	connList, _ := m.activeConns.LoadOrStore(user, &connList{})
	connList.add(conn)
	if state.exceeded.Load() {
		conn.markQuotaExceeded()
	}
	return func(n int) {
			m.AddBytes(user, n)
		}, func() {
			connList.remove(conn)
		}
}

func (m *QuotaManager) ApplyConfig(user option.TrafficQuotaUser) error {
	if user.Name == "" {
		return E.New("traffic-quota user missing name")
	}
	config, err := newQuotaConfig(user.QuotaGB, user.Period, user.PeriodStart, user.PeriodDays, nil)
	if err != nil {
		return E.Cause(err, "invalid traffic-quota user ", user.Name)
	}
	if config == nil {
		return E.New("invalid traffic-quota user ", user.Name)
	}
	m.storeConfig(user.Name, config)
	state := m.stateFor(user.Name)
	state.setPeriodKeyIfEmpty(m.mustPeriodKey(user.Name, m.now()))
	if state.usage.Load() > config.quotaBytes {
		m.tripExceeded(user.Name, state)
	} else {
		state.exceeded.Store(false)
	}
	return nil
}

func (m *QuotaManager) RemoveConfig(user string) error {
	if user == "" {
		return E.New("traffic-quota user missing name")
	}
	m.deleteConfig(user)
	m.activeConns.Delete(user)
	m.states.Delete(user)
	return nil
}

func (m *QuotaManager) Status(user string) (Status, bool) {
	config, loaded := m.loadConfig(user)
	if !loaded {
		return Status{}, false
	}
	state, loaded := m.states.Load(user)
	if !loaded {
		return Status{QuotaBytes: config.quotaBytes}, true
	}
	return Status{
		UsageBytes: state.usage.Load(),
		QuotaBytes: config.quotaBytes,
		Exceeded:   state.exceeded.Load(),
	}, true
}

func (m *QuotaManager) GetConfig(user string) (option.TrafficQuotaUser, bool) {
	config, loaded := m.loadConfig(user)
	if !loaded {
		return option.TrafficQuotaUser{}, false
	}
	return quotaConfigToOption(user, config), true
}

func (m *QuotaManager) SnapshotState(user string) (RuntimeState, bool) {
	config, loaded := m.loadConfig(user)
	if !loaded {
		return RuntimeState{}, false
	}
	snapshot := RuntimeState{
		User: quotaConfigToOption(user, config),
	}
	if state, loaded := m.states.Load(user); loaded {
		snapshot.UsageBytes = state.usage.Load()
		snapshot.PendingDelta = state.pendingDelta.Load()
		snapshot.Exceeded = state.exceeded.Load()
		snapshot.PeriodKey = state.getPeriodKey()
	}
	if activeConns, loaded := m.activeConns.Load(user); loaded {
		snapshot.activeConns = activeConns
	}
	return snapshot, true
}

func (m *QuotaManager) RestoreState(state RuntimeState) error {
	config, err := newQuotaConfig(state.User.QuotaGB, state.User.Period, state.User.PeriodStart, state.User.PeriodDays, nil)
	if err != nil {
		return E.Cause(err, "invalid traffic-quota user ", state.User.Name)
	}
	if config == nil {
		return E.New("invalid traffic-quota user ", state.User.Name)
	}
	m.storeConfig(state.User.Name, config)
	userState := m.stateFor(state.User.Name)
	userState.usage.Store(state.UsageBytes)
	userState.pendingDelta.Store(state.PendingDelta)
	userState.exceeded.Store(state.Exceeded)
	userState.setPeriodKey(state.PeriodKey)
	if state.activeConns != nil {
		m.activeConns.Store(state.User.Name, state.activeConns)
	} else {
		m.activeConns.Delete(state.User.Name)
	}
	return nil
}

func (m *QuotaManager) HasQuota(user string) bool {
	_, loaded := m.loadConfig(user)
	return loaded
}

func (m *QuotaManager) AddBytes(user string, n int) {
	if n <= 0 {
		return
	}
	config, loaded := m.loadConfig(user)
	if !loaded {
		return
	}
	state := m.stateFor(user)
	state.setPeriodKeyIfEmpty(m.mustPeriodKey(user, m.now()))
	total := state.usage.Add(int64(n))
	state.pendingDelta.Add(int64(n))
	if total > config.quotaBytes {
		m.tripExceeded(user, state)
	}
}

func (m *QuotaManager) LoadUsage(user string, bytes int64) {
	config, loaded := m.loadConfig(user)
	if !loaded {
		return
	}
	state := m.stateFor(user)
	state.setPeriodKeyIfEmpty(m.mustPeriodKey(user, m.now()))
	state.usage.Store(bytes)
	if bytes > config.quotaBytes {
		m.tripExceeded(user, state)
		return
	}
	state.exceeded.Store(false)
}

func (m *QuotaManager) Usage(user string) int64 {
	state, loaded := m.states.Load(user)
	if !loaded {
		return 0
	}
	return state.usage.Load()
}

func (m *QuotaManager) IsExceeded(user string) bool {
	state, loaded := m.states.Load(user)
	return loaded && state.exceeded.Load()
}

func (m *QuotaManager) CheckPeriodReset(now time.Time) []PeriodReset {
	var resets []PeriodReset
	for _, user := range m.userNames() {
		state, loaded := m.states.Load(user)
		if !loaded {
			continue
		}
		currentKey := m.mustPeriodKey(user, now)
		previousKey := state.getPeriodKey()
		if previousKey == "" {
			state.setPeriodKey(currentKey)
			continue
		}
		if currentKey == previousKey {
			continue
		}
		state.setPeriodKey(currentKey)
		state.usage.Store(0)
		state.pendingDelta.Store(0)
		state.exceeded.Store(false)
		resets = append(resets, PeriodReset{
			User:          user,
			PreviousKey:   previousKey,
			CurrentPeriod: currentKey,
		})
	}
	return resets
}

func (m *QuotaManager) GetCurrentPeriodKey(user string, now time.Time) (string, error) {
	config, loaded := m.loadConfig(user)
	if !loaded {
		return "", E.New("traffic-quota user not configured: ", user)
	}
	return periodKey(config, now)
}

func (m *QuotaManager) CurrentPeriodKey(user string) string {
	state := m.stateFor(user)
	periodKey := state.getPeriodKey()
	if periodKey != "" {
		return periodKey
	}
	periodKey = m.mustPeriodKey(user, m.now())
	state.setPeriodKeyIfEmpty(periodKey)
	return periodKey
}

func (m *QuotaManager) Users() []string {
	users := make([]string, 0, len(m.userConfigs))
	return append(users, m.userNames()...)
}

func (m *QuotaManager) ConsumePendingDeltas() map[string]int64 {
	pending := make(map[string]int64)
	for _, user := range m.userNames() {
		state, loaded := m.states.Load(user)
		if !loaded {
			continue
		}
		delta := state.pendingDelta.Swap(0)
		if delta != 0 {
			pending[user] = delta
		}
	}
	return pending
}

func (m *QuotaManager) RestorePendingDelta(user string, delta int64) {
	if delta == 0 || !m.HasQuota(user) {
		return
	}
	state := m.stateFor(user)
	state.pendingDelta.Add(delta)
}

func (m *QuotaManager) stateFor(user string) *userState {
	state, _ := m.states.LoadOrStore(user, &userState{})
	return state
}

func (m *QuotaManager) loadConfig(user string) (*QuotaConfig, bool) {
	m.userConfigAccess.RLock()
	config, loaded := m.userConfigs[user]
	m.userConfigAccess.RUnlock()
	return config, loaded
}

func (m *QuotaManager) storeConfig(user string, config *QuotaConfig) {
	m.userConfigAccess.Lock()
	m.userConfigs[user] = config
	m.userConfigAccess.Unlock()
}

func (m *QuotaManager) deleteConfig(user string) {
	m.userConfigAccess.Lock()
	delete(m.userConfigs, user)
	m.userConfigAccess.Unlock()
}

func (m *QuotaManager) userNames() []string {
	m.userConfigAccess.RLock()
	users := make([]string, 0, len(m.userConfigs))
	for user := range m.userConfigs {
		users = append(users, user)
	}
	m.userConfigAccess.RUnlock()
	return users
}

func (m *QuotaManager) tripExceeded(user string, state *userState) {
	if !state.exceeded.CompareAndSwap(false, true) {
		return
	}
	connList, loaded := m.activeConns.Load(user)
	if loaded {
		connList.closeAll()
	}
}

func (m *QuotaManager) mustPeriodKey(user string, now time.Time) string {
	key, err := m.GetCurrentPeriodKey(user, now)
	if err != nil {
		panic(err)
	}
	return key
}

func newQuotaConfig(quotaGB float64, period string, periodStart string, periodDays int, base *QuotaConfig) (*QuotaConfig, error) {
	var resolved QuotaConfig
	if base != nil {
		resolved = *base
	}

	if quotaGB > 0 {
		resolved.quotaBytes = quotaBytes(quotaGB)
	}
	if period != "" {
		resolved.period = PeriodType(period)
	}
	if periodStart != "" {
		parsedStart, err := parsePeriodStart(periodStart)
		if err != nil {
			return nil, err
		}
		resolved.periodStart = parsedStart
	}
	if periodDays > 0 {
		resolved.periodDays = periodDays
	}

	if resolved.quotaBytes <= 0 {
		return nil, nil
	}
	if err := validateQuotaConfig(&resolved); err != nil {
		return nil, err
	}
	return &resolved, nil
}

func validateQuotaConfig(config *QuotaConfig) error {
	switch config.period {
	case PeriodDaily, PeriodWeekly, PeriodMonthly:
		return nil
	case PeriodCustom:
		if config.periodStart.IsZero() {
			return E.New("custom period requires period_start")
		}
		if config.periodDays <= 0 {
			return E.New("custom period requires period_days")
		}
		return nil
	case "":
		return E.New("missing period")
	default:
		return E.New("unsupported period: ", config.period)
	}
}

func periodKey(config *QuotaConfig, now time.Time) (string, error) {
	normalizedNow := now.UTC()
	switch config.period {
	case PeriodDaily:
		return normalizedNow.Format("2006-01-02"), nil
	case PeriodWeekly:
		weekday := int(normalizedNow.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		weekStart := beginningOfDay(normalizedNow).AddDate(0, 0, -(weekday - 1))
		return weekStart.Format("2006-01-02"), nil
	case PeriodMonthly:
		return normalizedNow.Format("2006-01"), nil
	case PeriodCustom:
		if config.periodStart.IsZero() || config.periodDays <= 0 {
			return "", E.New("invalid custom period configuration")
		}
		start := beginningOfDay(config.periodStart.UTC())
		current := beginningOfDay(normalizedNow)
		if current.Before(start) {
			return start.Format("2006-01-02"), nil
		}
		days := int(current.Sub(start) / (24 * time.Hour))
		offset := (days / config.periodDays) * config.periodDays
		return start.AddDate(0, 0, offset).Format("2006-01-02"), nil
	default:
		return "", E.New("unsupported period: ", config.period)
	}
}

func parsePeriodStart(value string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse period_start %q", value)
}

func beginningOfDay(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func quotaBytes(quotaGB float64) int64 {
	return int64(math.Round(quotaGB * float64(1<<30)))
}

func formatPeriodStart(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func quotaConfigToOption(user string, config *QuotaConfig) option.TrafficQuotaUser {
	return option.TrafficQuotaUser{
		Name:        user,
		QuotaGB:     float64(config.quotaBytes) / float64(1<<30),
		Period:      string(config.period),
		PeriodStart: formatPeriodStart(config.periodStart),
		PeriodDays:  config.periodDays,
	}
}
