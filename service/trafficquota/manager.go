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

// connList shrink thresholds. See Unit 3 of the 2026-05-14 memory-leak
// plan: a long peak followed by a low water mark would otherwise pin the
// backing array forever. connListShrinkMinCap keeps shrink off the hot
// path for short lists; connListShrinkRatio gates shrink to genuinely
// underwater capacity so normal connection churn does not thrash.
const (
	connListShrinkMinCap = 1024
	connListShrinkRatio  = 4
)

type connList struct {
	access sync.Mutex
	conns  []quotaTrackedConn
}

func (l *connList) add(conn quotaTrackedConn) {
	l.access.Lock()
	l.conns = append(l.conns, conn)
	l.access.Unlock()
}

// remove deletes conn from the list using swap-and-zero. The last slot is
// nil-ed before the slice is truncated so the interface value (which
// references the *QuotaConn and its onClose closure) is released for GC;
// this is the fix for the residual reference identified in Unit 3 P1.
// When the backing array has grown well past the live set, the slice is
// reallocated to a smaller cap so a historical peak does not pin memory
// permanently.
func (l *connList) remove(conn quotaTrackedConn) {
	l.access.Lock()
	defer l.access.Unlock()
	for i := range l.conns {
		if l.conns[i] == conn {
			last := len(l.conns) - 1
			if i != last {
				l.conns[i] = l.conns[last]
			}
			l.conns[last] = nil
			l.conns = l.conns[:last]
			if cap(l.conns) > connListShrinkMinCap && len(l.conns) < cap(l.conns)/connListShrinkRatio {
				shrunk := make([]quotaTrackedConn, len(l.conns), len(l.conns)*2+1)
				copy(shrunk, l.conns)
				l.conns = shrunk
			}
			return
		}
	}
}

// closeAll snapshots the current list under the lock and notifies each
// tracked conn outside the lock. Used by quota-exceeded trips where the
// list itself must keep accepting future Register/Remove (i.e. the user
// is not being deleted, only marked exceeded).
func (l *connList) closeAll() {
	l.access.Lock()
	conns := append([]quotaTrackedConn(nil), l.conns...)
	l.access.Unlock()
	for _, conn := range conns {
		conn.markQuotaExceeded()
	}
}

// closeAllAndClear behaves like closeAll but also empties the list and
// drops the backing array. Used by RemoveConfig so that, even if the
// outer sync.Map.Delete is lazy (read map retains the value until the
// next dirty-map promotion), the connList value held by that residual
// entry no longer references any *QuotaConn or onClose closure.
func (l *connList) closeAllAndClear() {
	l.access.Lock()
	snapshot := append([]quotaTrackedConn(nil), l.conns...)
	clear(l.conns)
	l.conns = nil
	l.access.Unlock()
	for _, conn := range snapshot {
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

// PendingDelta carries a single user's accumulated traffic increment
// stamped with the period it belongs to. ConsumePendingDeltas returns
// these so flushPending can attribute deltas to the correct period even
// when a period reset races with the flush.
type PendingDelta struct {
	User      string
	PeriodKey string
	Delta     int64
}

// QuotaManager lock order (enforced; see Unit 6 of the 2026-05-14
// memory-leak plan):
//
//	lifecycle (R/W)  →  userState.periodAccess  →  connList.access  →  (no-lock callbacks)
//
// Additional rules:
//   - RegisterConn takes lifecycle.RLock for the whole sequence
//     loadConfig → stateFor → activeConns.LoadOrStore → connList.add.
//     RemoveConfig takes lifecycle.Lock briefly to swap map state, then
//     releases before calling connList.closeAllAndClear in the open. This
//     pair closes the orphan-conn race where a new conn could be added to
//     a list already removed from activeConns.
//   - tripExceeded does NOT take lifecycle: the usage hot path must not
//     wait on configuration changes. It is safe to closeAll a list that
//     has just been unlinked — by Unit 3 Safety Proof P3 a redundant
//     markQuotaExceeded is a no-op.
//   - QuotaConn.Close's onClose callback (which removes from connList)
//     holds no outer lock; it is allowed to run against a list that has
//     already been zeroed out by closeAllAndClear.
//   - connList.access is a leaf lock: nothing else may be acquired while
//     it is held, and no callback (markQuotaExceeded, onClose, etc.) may
//     run inside its critical section.
type QuotaManager struct {
	// lifecycle serializes RegisterConn (RLock) against RemoveConfig
	// (Lock). Read-write asymmetric because conn registration is the hot
	// path and benefits from concurrent reads; configuration changes are
	// the slow path and can afford a short exclusive section.
	lifecycle        sync.RWMutex
	userConfigAccess sync.RWMutex
	userConfigs      map[string]*QuotaConfig
	// activeConns is keyed by user name. Entries are created lazily by
	// RegisterConn (LoadOrStore on first conn) and are removed *only* by
	// RemoveConfig. A user that never opens a tracked connection produces
	// no entry; once an entry exists it persists even if the user's
	// active connection count drops back to zero — this avoids a
	// LoadOrStore <-> Delete race in steady state. Capacity is therefore
	// bounded by the configured user count, not by historical connection
	// peaks (the underlying connList shrinks separately via Unit 3).
	activeConns compatible.Map[string, *connList]
	// states is keyed by user name. Entries are created lazily on the
	// first AddBytes / LoadUsage / RegisterConn and removed only by
	// RemoveConfig. A user with quota=0 (config exists but no usage yet)
	// still gets a state entry once any AddBytes is observed; this is
	// deliberate so period-key bookkeeping survives an idle window.
	// Capacity contract is the same as activeConns: bounded by configured
	// user count. See Unit 7 of the 2026-05-14 memory-leak plan.
	states compatible.Map[string, *userState]
	now    func() time.Time
	// dirty-user index, see Unit 4 of the 2026-05-14 memory-leak plan.
	// AddBytes (and RestorePendingDelta) enqueue a user; ConsumePendingDeltas
	// drains the queue under dirtyMu and only inspects those users' state.
	// This decouples flush cost from the total quota-user count.
	dirtyMu    sync.Mutex
	dirtySet   map[string]struct{}
	dirtyQueue []string
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
		dirtySet:    make(map[string]struct{}),
	}, nil
}

func (m *QuotaManager) RegisterConn(user string, conn quotaTrackedConn) (func(int), func()) {
	// lifecycle.RLock spans the entire registration sequence so that a
	// concurrent RemoveConfig (which takes lifecycle.Lock) cannot
	// interleave between "load config / get connList" and "connList.add",
	// which is the orphan-conn race fixed by Unit 6 of the 2026-05-14
	// memory-leak plan. The lock is released BEFORE the closure is
	// returned — onClose / onBytes do not need lifecycle.
	m.lifecycle.RLock()
	if _, loaded := m.loadConfig(user); !loaded {
		m.lifecycle.RUnlock()
		return nil, nil
	}
	state := m.stateFor(user)
	state.setPeriodKeyIfEmpty(m.mustPeriodKey(user, m.now()))
	connList, _ := m.activeConns.LoadOrStore(user, &connList{})
	connList.add(conn)
	exceeded := state.exceeded.Load()
	m.lifecycle.RUnlock()

	if exceeded {
		// markQuotaExceeded is a no-lock callback per the leaf-lock rule;
		// run it after releasing lifecycle to avoid holding it across a
		// caller-defined notification.
		conn.markQuotaExceeded()
	}
	return func(n int) {
			m.AddBytes(user, n)
		}, func() {
			// onClose path: no outer lock. remove tolerates a list that
			// has been zeroed out by a concurrent RemoveConfig — see
			// Unit 3 Safety Proof P1/P3.
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
	// lifecycle.Lock holds only across the map mutations. The slow
	// closeAllAndClear (which iterates the list and calls
	// markQuotaExceeded on each conn) runs in the open so that a single
	// user with many active connections does not block other users'
	// RegisterConn calls. The Load → Delete pair is atomic via
	// LoadAndDelete so a racing RegisterConn either observes the
	// pre-delete connList (and adds to it; that conn is then included in
	// the closeAllAndClear we are about to run) or observes "loadConfig
	// returns false" and bails out cleanly.
	m.lifecycle.Lock()
	m.deleteConfig(user)
	cl, _ := m.activeConns.LoadAndDelete(user)
	m.states.Delete(user)
	m.lifecycle.Unlock()

	if cl != nil {
		cl.closeAllAndClear()
	}
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

func (m *QuotaManager) ClearPendingDelta(user string) {
	state, loaded := m.states.Load(user)
	if !loaded {
		return
	}
	state.pendingDelta.Store(0)
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
	// periodAccess covers the periodKey check AND the pendingDelta update
	// in the same critical section. This is what makes ConsumePendingDeltas
	// and CheckPeriodReset see a consistent (periodKey, pendingDelta) pair
	// — the invariant Unit 4 requires to prevent old-period bytes from
	// being attributed to a new period.
	state.periodAccess.Lock()
	if state.periodKey == "" {
		// An empty periodKey here can mean a concurrent RemoveConfig ran
		// between the loadConfig check above and stateFor (which then
		// resurrected an empty state). mustPeriodKey would panic on the
		// now-missing config, so re-resolve via the error-returning variant
		// and drop the bytes — the user's quota is gone anyway.
		key, err := m.GetCurrentPeriodKey(user, m.now())
		if err != nil {
			state.periodAccess.Unlock()
			return
		}
		state.periodKey = key
	}
	total := state.usage.Add(int64(n))
	state.pendingDelta.Add(int64(n))
	state.periodAccess.Unlock()

	m.markDirty(user)

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

		// All period+pending mutations happen inside the same periodAccess
		// critical section so AddBytes / ConsumePendingDeltas observe an
		// atomic boundary: either fully on the old period (with usage and
		// pendingDelta visible) or fully on the new one (cleared).
		state.periodAccess.Lock()
		previousKey := state.periodKey
		if previousKey == "" {
			state.periodKey = currentKey
			state.periodAccess.Unlock()
			continue
		}
		if currentKey == previousKey {
			state.periodAccess.Unlock()
			continue
		}
		state.pendingDelta.Swap(0)
		state.usage.Store(0)
		state.exceeded.Store(false)
		state.periodKey = currentKey
		state.periodAccess.Unlock()

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

// ConsumePendingDeltas atomically drains the dirty-user queue and returns
// one PendingDelta per user with a non-zero accumulated increment, each
// stamped with the periodKey it belonged to at consume time. Concurrent
// AddBytes calls during the drain are not lost: any user enqueued after
// the dirtyMu drain falls into the next flush cycle. A zero-delta entry
// can appear on the next round if AddBytes raced past the drain — that is
// harmless (the second flush sees delta==0 and skips), and is preferable
// to a contended single critical section.
func (m *QuotaManager) ConsumePendingDeltas() []PendingDelta {
	m.dirtyMu.Lock()
	users := m.dirtyQueue
	m.dirtyQueue = nil
	m.dirtySet = make(map[string]struct{})
	m.dirtyMu.Unlock()

	if len(users) == 0 {
		return nil
	}
	pending := make([]PendingDelta, 0, len(users))
	for _, user := range users {
		state, loaded := m.states.Load(user)
		if !loaded {
			continue
		}
		state.periodAccess.Lock()
		periodKey := state.periodKey
		delta := state.pendingDelta.Swap(0)
		state.periodAccess.Unlock()
		if delta == 0 {
			continue
		}
		pending = append(pending, PendingDelta{
			User:      user,
			PeriodKey: periodKey,
			Delta:     delta,
		})
	}
	return pending
}

// RestorePendingDelta re-adds a delta whose IncrBy failed back into the
// user's pending bucket, but only if the user is still on the same period
// the delta was originally consumed from. If the period rolled over in
// the meantime the delta is dropped to avoid carrying old-period bytes
// into the new period (the slight under-count is bounded by the
// flush-failure window and is preferable to mis-attributed accounting).
func (m *QuotaManager) RestorePendingDelta(user, periodKey string, delta int64) {
	if delta == 0 || !m.HasQuota(user) {
		return
	}
	state := m.stateFor(user)
	state.periodAccess.Lock()
	matched := state.periodKey == periodKey
	if matched {
		state.pendingDelta.Add(delta)
	}
	state.periodAccess.Unlock()
	if matched {
		m.markDirty(user)
	}
}

// markDirty enqueues a user for the next flush. The dirtyMu critical
// section is tiny: a map lookup and at most one slice append. The lock
// order is sequential — periodAccess is released before markDirty is
// called, never nested — so there is no chance of a periodAccess <→>
// dirtyMu deadlock with CheckPeriodReset or ConsumePendingDeltas.
func (m *QuotaManager) markDirty(user string) {
	m.dirtyMu.Lock()
	if _, ok := m.dirtySet[user]; !ok {
		m.dirtySet[user] = struct{}{}
		m.dirtyQueue = append(m.dirtyQueue, user)
	}
	m.dirtyMu.Unlock()
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
