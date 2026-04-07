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
	userConfigs map[string]*QuotaConfig
	activeConns compatible.Map[string, *connList]
	states      compatible.Map[string, *userState]
	now         func() time.Time
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
	if _, loaded := m.userConfigs[user]; !loaded {
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

func (m *QuotaManager) HasQuota(user string) bool {
	_, loaded := m.userConfigs[user]
	return loaded
}

func (m *QuotaManager) AddBytes(user string, n int) {
	if n <= 0 {
		return
	}
	config, loaded := m.userConfigs[user]
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
	config, loaded := m.userConfigs[user]
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
	for user := range m.userConfigs {
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
	config, loaded := m.userConfigs[user]
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
	for user := range m.userConfigs {
		users = append(users, user)
	}
	return users
}

func (m *QuotaManager) ConsumePendingDeltas() map[string]int64 {
	pending := make(map[string]int64)
	for user := range m.userConfigs {
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
