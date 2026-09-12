// Package store is the SQLite index of every media file Shelf knows about.
package store

import (
	"strings"
	"time"

	"github.com/rotisserie/eris"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// File is one indexed media file. Root is the resolved absolute path of the
// folder it was found under; RelPath is slash-separated from there.
type File struct {
	ID      int64  `gorm:"primaryKey" json:"id"`
	Root    string `gorm:"uniqueIndex:idx_root_rel;index" json:"root"`
	RelPath string `gorm:"uniqueIndex:idx_root_rel" json:"path"`
	Dir     string `gorm:"index" json:"dir"` // rel dir, "" at the root
	Name    string `json:"name"`
	Ext     string `json:"ext"`               // lower-case, without the dot
	Kind    string `gorm:"index" json:"kind"` // photo | video | gif

	Size    int64     `json:"size"`
	ModTime time.Time `gorm:"index" json:"mod_time"`
	AddedAt time.Time `gorm:"index" json:"added_at"` // first indexed

	Width      int    `json:"width"`
	Height     int    `json:"height"`
	DurationMs int    `json:"duration_ms"`
	VCodec     string `json:"vcodec"`
	ACodec     string `json:"acodec"`
	// Direct means a browser can play the file as it is; anything else is
	// remuxed through ffmpeg on the way out.
	Direct bool `json:"direct"`

	Probed bool  `gorm:"index" json:"-"` // metadata pass done
	Seen   int64 `gorm:"index" json:"-"` // scan generation that last saw it
}

func (File) TableName() string { return "files" }

func Init(path string) error {
	db, err := gorm.Open(sqlite.Open(path+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return eris.Wrap(err, "failed to open database")
	}
	// An in-memory database exists per connection; the pool must not hand
	// a second goroutine an empty one of its own.
	if strings.Contains(path, ":memory:") {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.SetMaxOpenConns(1)
		}
	}
	if err := db.AutoMigrate(&File{}); err != nil {
		return eris.Wrap(err, "failed to migrate database")
	}
	DB = db
	return nil
}
