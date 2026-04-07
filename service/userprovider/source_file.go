package userprovider

import (
	"context"
	"os"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

type FileSource struct {
	logger         log.ContextLogger
	path           string
	updateInterval time.Duration
	lastModTime    time.Time
}

func NewFileSource(logger log.ContextLogger, options *option.UserProviderFileOptions) *FileSource {
	updateInterval := time.Duration(options.UpdateInterval)
	if updateInterval == 0 {
		updateInterval = time.Minute
	}
	return &FileSource{
		logger:         logger,
		path:           options.Path,
		updateInterval: updateInterval,
	}
}

func (s *FileSource) Load() ([]option.User, error) {
	content, err := os.ReadFile(s.path)
	if err != nil {
		return nil, E.Cause(err, "read user file")
	}
	var users []option.User
	err = json.Unmarshal(content, &users)
	if err != nil {
		return nil, E.Cause(err, "parse user file")
	}
	info, _ := os.Stat(s.path)
	if info != nil {
		s.lastModTime = info.ModTime()
	}
	return users, nil
}

func (s *FileSource) Run(ctx context.Context, onUpdate func()) {
	ticker := time.NewTicker(s.updateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(s.path)
			if err != nil {
				s.logger.Error("stat user file: ", err)
				continue
			}
			if info.ModTime().After(s.lastModTime) {
				s.logger.Info("user file changed, reloading")
				s.lastModTime = info.ModTime()
				onUpdate()
			}
		}
	}
}
