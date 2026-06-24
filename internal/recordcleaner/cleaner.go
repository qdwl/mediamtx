// Package recordcleaner contains the recording cleaner.
package recordcleaner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/recordstore"
)

var timeNow = time.Now

// Cleaner removes expired recording segments from disk.
type Cleaner struct {
	PathConfs map[string]*conf.Path
	Parent    logger.Writer

	ctx       context.Context
	ctxCancel func()

	mutex                      sync.RWMutex
	recordDeleteAfterOverrides map[string]conf.Duration
	chReloadConf               chan map[string]*conf.Path
	done                       chan struct{}
}

// Initialize initializes a Cleaner.
func (c *Cleaner) Initialize() {
	c.ctx, c.ctxCancel = context.WithCancel(context.Background())
	c.recordDeleteAfterOverrides = make(map[string]conf.Duration)
	c.chReloadConf = make(chan map[string]*conf.Path)
	c.done = make(chan struct{})

	go c.run()
}

// Close closes the Cleaner.
func (c *Cleaner) Close() {
	c.ctxCancel()
	<-c.done
}

// Log implements logger.Writer.
func (c *Cleaner) Log(level logger.Level, format string, args ...interface{}) {
	c.Parent.Log(level, "[record cleaner]"+format, args...)
}

// ReloadPathConfs is called by core.Core.
func (c *Cleaner) ReloadPathConfs(pathConfs map[string]*conf.Path) {
	select {
	case c.chReloadConf <- pathConfs:
	case <-c.ctx.Done():
	}
}

// SetRecordDeleteAfterOverride sets a per-path retention override.
func (c *Cleaner) SetRecordDeleteAfterOverride(pathName string, recordDeleteAfter *conf.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if recordDeleteAfter == nil {
		delete(c.recordDeleteAfterOverrides, pathName)
		return
	}

	c.recordDeleteAfterOverrides[pathName] = *recordDeleteAfter
}

func (c *Cleaner) run() {
	defer close(c.done)

	c.doRun() //nolint:errcheck

	for {
		select {
		case <-time.After(c.cleanInterval()):
			c.doRun()

		case cnf := <-c.chReloadConf:
			c.PathConfs = cnf

		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Cleaner) cleanInterval() time.Duration {
	interval := 30 * 60 * time.Second

	for _, e := range c.PathConfs {
		if e.RecordDeleteAfter != 0 && interval > (time.Duration(e.RecordDeleteAfter)/2) {
			interval = time.Duration(e.RecordDeleteAfter) / 2
		}
	}

	c.mutex.RLock()
	defer c.mutex.RUnlock()

	for _, e := range c.recordDeleteAfterOverrides {
		if e != 0 && interval > (time.Duration(e)/2) {
			interval = time.Duration(e) / 2
		}
	}

	return interval
}

func (c *Cleaner) doRun() {
	now := timeNow()

	pathNames := recordstore.FindAllPathsWithSegments(c.PathConfs)

	for _, pathName := range pathNames {
		c.processPath(now, pathName) //nolint:errcheck
	}
}

func (c *Cleaner) processPath(now time.Time, pathName string) error {
	pathConf, _, err := conf.FindPathConf(c.PathConfs, pathName)
	if err != nil {
		return err
	}

	recordDeleteAfter := c.recordDeleteAfter(pathName, pathConf)
	if recordDeleteAfter == 0 {
		return nil
	}

	err = c.deleteExpiredSegments(now, pathName, pathConf, recordDeleteAfter)
	if err != nil {
		return err
	}

	c.deleteEmptyDirs(pathConf)

	return nil
}

func (c *Cleaner) recordDeleteAfter(pathName string, pathConf *conf.Path) conf.Duration {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if v, ok := c.recordDeleteAfterOverrides[pathName]; ok {
		return v
	}

	return pathConf.RecordDeleteAfter
}

func (c *Cleaner) deleteExpiredSegments(
	now time.Time,
	pathName string,
	pathConf *conf.Path,
	recordDeleteAfter conf.Duration,
) error {
	end := now.Add(-time.Duration(recordDeleteAfter))
	segments, err := recordstore.FindSegments(pathConf, pathName, nil, &end)
	if err != nil {
		return err
	}

	for _, seg := range segments {
		c.Log(logger.Debug, "removing %s", seg.Fpath)
		os.Remove(seg.Fpath)
	}

	return nil
}

func (c *Cleaner) deleteEmptyDirs(pathConf *conf.Path) {
	recordPath := strings.ReplaceAll(pathConf.RecordPath, "%path", pathConf.Name)
	commonPath := recordstore.CommonPath(recordPath)

	filepath.WalkDir(commonPath, func(fpath string, info fs.DirEntry, err error) error { //nolint:errcheck
		if err != nil {
			return err
		}

		if info.IsDir() {
			os.Remove(fpath)
		}

		return nil
	})
}
