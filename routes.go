package main

import (
	"net/http"

	"github.com/anacrolix/torrent"
)

func AddRoutes(mux *http.ServeMux, c *torrent.Client, config *ClientConfig, serverErr chan error) {
	mux.Handle("GET /torrents", HandleGetTorrents(c, config))
	mux.Handle("POST /torrents", HandlePostTorrents(c, config))
	mux.Handle("GET /torrents/{infohash}", HandleGetInfoHash(c, config))
	mux.Handle("DELETE /torrents/{infohash}", HandleDeleteInfoHash(c, config))
	mux.Handle("GET /torrents/{infohash}/{query}", HandleGetInfoHashFile(c, config))
	mux.Handle("GET /exit", HandleExit(serverErr))
}