package trafficquota

type Persister interface {
	Load(user, periodKey string) (int64, error)
	LoadAll(periodKey string) (map[string]int64, error)
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
