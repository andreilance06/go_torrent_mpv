package torrentclient

import (
	"path/filepath"

	"github.com/andreilance06/go_torrent_mpv/internal/options"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/squirrel"
	"github.com/anacrolix/torrent/storage"
	sqliteStorage "github.com/anacrolix/torrent/storage/sqlite"
)

func InitStorage(cfg *options.Config) (storage.ClientImplCloser, error) {
	return sqliteStorage.NewDirectStorage(CreateDBOptions(cfg))
}

func CreateDBOptions(cfg *options.Config) squirrel.NewCacheOpts {
	opts := squirrel.NewCacheOpts{}
	opts.SetAutoVacuum = generics.Some("incremental")
	opts.RequireAutoVacuum = generics.Some[any](2)
	opts.SetJournalMode = "wal"
	opts.SetSynchronous = 0
	opts.Path = filepath.Join(cfg.DownloadDir, "torrents.db")
	opts.Capacity = -1
	opts.MmapSizeOk = true
	opts.MmapSize = 64 << 20
	opts.CacheSize = generics.Some[int64](-32 << 20)
	opts.SetLockingMode = "normal"
	opts.JournalSizeLimit.Set(256 << 20)

	return opts
}
