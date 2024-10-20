package main

import (
	"context"
	"net/http"

	"github.com/anacrolix/torrent"
)

func RegisterRoutes(mux *http.ServeMux, c *torrent.Client, config *ClientConfig, cancel context.CancelFunc) {
	mux.Handle("GET /torrents", HandleGetTorrents(c, config))
	mux.Handle("POST /torrents", HandlePostTorrents(c, config))
	mux.Handle("GET /torrents/{infohash}", HandleGetInfoHash(c, config))
	mux.Handle("DELETE /torrents/{infohash}", HandleDeleteInfoHash(c, config))
	mux.Handle("GET /torrents/{infohash}/{query...}", HandleGetInfoHashFile(c, config))
	mux.Handle("GET /exit", HandleExit(cancel))
}
