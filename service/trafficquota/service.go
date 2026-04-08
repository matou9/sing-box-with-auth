package trafficquota

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/dynamicconfig"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

var (
	_ adapter.ConnectionTracker = (*Service)(nil)
	_ adapter.Service           = (*Service)(nil)
)

var (
	newRedisPersisterFunc = func(ctx context.Context, options *option.TrafficQuotaRedisOptions) (Persister, error) {
		return NewRedisPersister(ctx, options)
	}
	newPostgresPersisterFunc = func(ctx context.Context, options *option.TrafficQuotaPostgresOptions) (Persister, error) {
		return NewPostgresPersister(ctx, options)
	}
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.TrafficQuotaServiceOptions](registry, C.TypeTrafficQuota, NewService)
}

type Service struct {
	boxService.Adapter
	ctx            context.Context
	cancel         context.CancelFunc
	logger         log.ContextLogger
	manager        *QuotaManager
	persistence    *option.TrafficQuotaPersistence
	persister      Persister
	persistAccess  sync.Mutex
	applyMu        sync.Mutex
	flushInterval  time.Duration
	dynamicOptions *option.DynamicConfigOptions
	wg             sync.WaitGroup
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.TrafficQuotaServiceOptions) (adapter.Service, error) {
	manager, err := NewQuotaManager(options)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	flushInterval := time.Duration(options.FlushInterval)
	if flushInterval == 0 {
		flushInterval = 30 * time.Second
	}
	return &Service{
		Adapter:        boxService.NewAdapter(C.TypeTrafficQuota, tag),
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
		manager:        manager,
		persistence:    options.Persistence,
		flushInterval:  flushInterval,
		dynamicOptions: options.Dynamic,
	}, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	router := service.FromContext[adapter.Router](s.ctx)
	if router == nil {
		return E.New("missing router in context")
	}
	router.AppendTracker(s)
	if err := s.initPersister(); err != nil {
		return err
	}
	s.restoreUsage()
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.runFlushLoop() }()
	go func() { defer s.wg.Done(); s.runPeriodResetLoop() }()
	s.startDynamicSources()
	return nil
}

func (s *Service) startDynamicSources() {
	if s.dynamicOptions == nil {
		return
	}
	receiver := &dynamicconfig.Receiver{
		Apply:  s.applyDynamic,
		Remove: s.removeDynamic,
	}
	if s.dynamicOptions.Postgres != nil {
		source, err := dynamicconfig.NewPostgresSource(s.ctx, s.logger, s.dynamicOptions.Postgres, receiver)
		if err != nil {
			s.logger.Warn("traffic-quota dynamic postgres unavailable: ", err)
		} else {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				source.Run(s.ctx)
			}()
		}
	}
	if s.dynamicOptions.Redis != nil {
		source := dynamicconfig.NewRedisSource(s.logger, s.dynamicOptions.Redis, receiver)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			source.Run(s.ctx)
		}()
	}
}

func (s *Service) applyDynamic(row dynamicconfig.ConfigRow) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	isNew := !s.manager.HasQuota(row.User)
	if err := s.manager.ApplyConfig(option.TrafficQuotaUser{
		Name:        row.User,
		QuotaGB:     row.QuotaGB,
		Period:      row.Period,
		PeriodStart: row.PeriodStart,
		PeriodDays:  row.PeriodDays,
	}); err != nil {
		return err
	}
	if isNew && s.persister != nil {
		s.persistAccess.Lock()
		periodKey := s.manager.CurrentPeriodKey(row.User)
		if bytes, err := s.persister.Load(row.User, periodKey); err == nil {
			s.manager.LoadUsage(row.User, bytes)
		}
		s.persistAccess.Unlock()
	}
	return nil
}

func (s *Service) removeDynamic(user string) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	return s.removeConfigLocked(user)
}

func (s *Service) ApplyConfig(user option.TrafficQuotaUser) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	isNew := !s.manager.HasQuota(user.Name)
	if err := s.manager.ApplyConfig(user); err != nil {
		return err
	}
	if isNew && s.persister != nil {
		s.persistAccess.Lock()
		periodKey := s.manager.CurrentPeriodKey(user.Name)
		if bytes, err := s.persister.Load(user.Name, periodKey); err == nil {
			s.manager.LoadUsage(user.Name, bytes)
		}
		s.persistAccess.Unlock()
	}
	return nil
}

func (s *Service) RemoveConfig(user string) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	return s.removeConfigLocked(user)
}

func (s *Service) removeConfigLocked(user string) error {
	s.persistAccess.Lock()
	defer s.persistAccess.Unlock()
	if s.persister != nil && s.manager.HasQuota(user) {
		if err := s.persister.Delete(user, s.manager.CurrentPeriodKey(user)); err != nil {
			return err
		}
	}
	return s.manager.RemoveConfig(user)
}

func (s *Service) GetConfig(user string) (option.TrafficQuotaUser, bool) {
	return s.manager.GetConfig(user)
}

func (s *Service) SnapshotState(user string) (RuntimeState, bool) {
	return s.manager.SnapshotState(user)
}

func (s *Service) RestoreState(state RuntimeState) error {
	s.persistAccess.Lock()
	defer s.persistAccess.Unlock()
	if err := s.manager.RestoreState(state); err != nil {
		return err
	}
	if s.persister != nil {
		periodKey := state.PeriodKey
		if periodKey == "" {
			periodKey = s.manager.CurrentPeriodKey(state.User.Name)
		}
		if err := s.persister.Save(state.User.Name, periodKey, state.UsageBytes); err != nil {
			return err
		}
		s.manager.ClearPendingDelta(state.User.Name)
	}
	return nil
}

func (s *Service) QuotaStatus(user string) (Status, bool) {
	return s.manager.Status(user)
}

func (s *Service) RoutedConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) net.Conn {
	if metadata.User == "" || !s.manager.HasQuota(metadata.User) {
		return conn
	}
	if s.manager.IsExceeded(metadata.User) {
		quotaConn := NewQuotaConn(conn, nil, nil)
		quotaConn.markQuotaExceeded()
		return quotaConn
	}
	quotaConn := NewQuotaConn(conn, nil, nil)
	onBytes, onClose := s.manager.RegisterConn(metadata.User, quotaConn)
	if onBytes == nil {
		return conn
	}
	quotaConn.onBytes = onBytes
	quotaConn.onClose = onClose
	return quotaConn
}

func (s *Service) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) N.PacketConn {
	if metadata.User == "" || !s.manager.HasQuota(metadata.User) {
		return conn
	}
	if s.manager.IsExceeded(metadata.User) {
		quotaConn := NewQuotaPacketConn(conn, nil, nil)
		quotaConn.markQuotaExceeded()
		return quotaConn
	}
	quotaConn := NewQuotaPacketConn(conn, nil, nil)
	onBytes, onClose := s.manager.RegisterConn(metadata.User, quotaConn)
	if onBytes == nil {
		return conn
	}
	quotaConn.onBytes = onBytes
	quotaConn.onClose = onClose
	return quotaConn
}

func (s *Service) Close() error {
	s.cancel()
	s.wg.Wait()
	var errs []error
	if flushErr := s.flushPending(); flushErr != nil {
		errs = append(errs, flushErr)
	}
	if s.persister != nil {
		if closeErr := s.persister.Close(); closeErr != nil {
			errs = append(errs, closeErr)
		}
	}
	return E.Errors(errs...)
}

func (s *Service) initPersister() error {
	if s.persister != nil {
		return nil
	}
	if s.persistence == nil {
		s.persister = NewNoopPersister()
		return nil
	}
	if s.persistence.Redis != nil {
		persister, err := newRedisPersisterFunc(s.ctx, s.persistence.Redis)
		if err != nil {
			s.logger.Warn("traffic-quota redis unavailable, falling back to memory mode: ", err)
			s.persister = NewNoopPersister()
			return nil
		}
		s.persister = persister
		return nil
	}
	if s.persistence.Postgres != nil {
		persister, err := newPostgresPersisterFunc(s.ctx, s.persistence.Postgres)
		if err != nil {
			s.logger.Warn("traffic-quota postgres unavailable, falling back to memory mode: ", err)
			s.persister = NewNoopPersister()
			return nil
		}
		s.persister = persister
		return nil
	}
	s.persister = NewNoopPersister()
	return nil
}

func (s *Service) restoreUsage() {
	if s.persister == nil {
		return
	}
	for _, user := range s.manager.Users() {
		periodKey := s.manager.CurrentPeriodKey(user)
		usage, err := s.persister.Load(user, periodKey)
		if err != nil {
			s.logger.Warn("restore traffic quota usage for ", user, ": ", err)
			continue
		}
		s.manager.LoadUsage(user, usage)
	}
}

func (s *Service) runFlushLoop() {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.flushPending(); err != nil {
				s.logger.Error("flush traffic quota usage: ", err)
			}
		}
	}
}

func (s *Service) runPeriodResetLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.handlePeriodResets(s.manager.now()); err != nil {
				s.logger.Error("reset traffic quota period: ", err)
			}
		}
	}
}

func (s *Service) flushPending() error {
	// Step 1: collect pending deltas under lock (fast, no I/O)
	s.persistAccess.Lock()
	if s.persister == nil {
		s.persistAccess.Unlock()
		return nil
	}
	pending := s.manager.ConsumePendingDeltas()
	// Filter out users that have been removed (RemoveConfig holds persistAccess when
	// it calls persister.Delete, so checking HasQuota here is safe and prevents ghost
	// records when RemoveConfig races with flushPending I/O in step 2).
	for user := range pending {
		if !s.manager.HasQuota(user) {
			delete(pending, user)
		}
	}
	s.persistAccess.Unlock()

	if len(pending) == 0 {
		return nil
	}

	// Step 2: execute I/O without holding the lock
	var errs []error
	periodUsers := make(map[string][]string)
	for user, delta := range pending {
		periodKey := s.manager.CurrentPeriodKey(user)
		if err := s.persister.IncrBy(user, periodKey, delta); err != nil {
			s.manager.RestorePendingDelta(user, delta)
			errs = append(errs, E.Cause(err, "persist traffic quota delta for ", user))
			continue
		}
		periodUsers[periodKey] = append(periodUsers[periodKey], user)
	}

	// Step 3: reload totals (I/O, still no lock)
	loaded := make(map[string]map[string]int64)
	for periodKey := range periodUsers {
		result, err := s.persister.LoadAll(periodKey)
		if err != nil {
			errs = append(errs, E.Cause(err, "reload traffic quota totals for period ", periodKey))
			continue
		}
		loaded[periodKey] = result
	}

	// Step 4: update in-memory state under lock
	s.persistAccess.Lock()
	for periodKey, users := range periodUsers {
		if result, ok := loaded[periodKey]; ok {
			for _, user := range users {
				if value, ok := result[user]; ok {
					s.manager.LoadUsage(user, value)
				}
			}
		}
	}
	s.persistAccess.Unlock()

	return E.Errors(errs...)
}

func (s *Service) handlePeriodResets(now time.Time) error {
	// Collect resets in memory (no I/O needed here)
	resets := s.manager.CheckPeriodReset(now)
	if len(resets) == 0 {
		return nil
	}
	if s.persister == nil {
		return nil
	}

	// Execute persister.Delete outside the lock since it is remote I/O
	var errs []error
	for _, reset := range resets {
		if err := s.persister.Delete(reset.User, reset.PreviousKey); err != nil {
			errs = append(errs, E.Cause(err, "delete previous traffic quota period for ", reset.User))
		}
	}
	return E.Errors(errs...)
}
