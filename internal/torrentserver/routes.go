package torrentserver

import (
	"context"
	"net/http"
	pprof "net/http/pprof"

	"github.com/anacrolix/torrent"
	"github.com/andreilance06/go_torrent_mpv/internal/options"
)

func RegisterRoutes(mux *http.ServeMux, c *torrent.Client, config *options.Config, cancel context.CancelFunc) {
	mux.Handle("GET /torrents", HandleGetTorrents(c, config))
	mux.Handle("POST /torrents", HandlePostTorrents(c, config))
	mux.Handle("GET /torrents/{infohash}", HandleGetInfoHash(c, config))
	mux.Handle("DELETE /torrents/{infohash}", HandleDeleteInfoHash(c, config))
	mux.Handle("GET /torrents/{infohash}/{query...}", HandleGetInfoHashFile(c, config))
	mux.Handle("GET /exit", HandleExit(cancel))

	if !config.Profiling {
		return
	}

	mux.Handle("GET /goroutine", pprof.Handler("goroutine"))
	mux.Handle("GET /heap", pprof.Handler("heap"))
	mux.Handle("GET /allocs", pprof.Handler("allocs"))
	mux.Handle("GET /threadcreate", pprof.Handler("threadcreate"))
	mux.Handle("GET /block", pprof.Handler("block"))
	mux.Handle("GET /mutex", pprof.Handler("mutex"))
}
