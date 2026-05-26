package trafficquota

// loadManyBatchSize caps the number of users requested per round-trip in
// LoadMany. It bounds the size of a single MGET / ANY($n) packet so that
// the LoadAll → LoadMany rewrite (Unit 4) does not trade an O(total_users)
// SCAN for an O(active_users) single packet that itself blows up under
// large dirty fan-outs. The conservative initial value is documented in
// the 2026-05-14 memory-leak plan; tune via benchmark before lowering.
const loadManyBatchSize = 512

type Persister interface {
	Load(user, periodKey string) (int64, error)
	// LoadAll is retained for external tools and migration paths. The flush
	// hot path uses LoadMany instead so cost scales with active users, not
	// total quota users; see Unit 4 of the 2026-05-14 memory-leak plan.
	LoadAll(periodKey string) (map[string]int64, error)
	// LoadMany fetches the current bytes_used for the given users within
	// periodKey. Implementations must batch internally (loadManyBatchSize)
	// so a large dirty set does not produce a single oversized backend
	// request. Empty users returns an empty map with no I/O.
	LoadMany(periodKey string, users []string) (map[string]int64, error)
	Save(user, periodKey string, bytes int64) error
	IncrBy(user, periodKey string, delta int64) error
	Delete(user, periodKey string) error
	Close() error
}

type NoopPersister struct{}

func NewNoopPersister() Persister {
	return &NoopPersister{}
}

func (p *NoopPersister) Load(string, string) (int64, error) {
	return 0, nil
}

func (p *NoopPersister) LoadAll(string) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func (p *NoopPersister) LoadMany(string, []string) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func (p *NoopPersister) Save(string, string, int64) error {
	return nil
}

func (p *NoopPersister) IncrBy(string, string, int64) error {
	return nil
}

func (p *NoopPersister) Delete(string, string) error {
	return nil
}

func (p *NoopPersister) Close() error {
	return nil
}
