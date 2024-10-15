package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/types/infohash"
)

func HandleGetTorrents(c *torrent.Client, config *ClientConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		torrents := make(map[string]string)
		for _, t := range c.Torrents() {
			select {
			case <-t.GotInfo():
			torrents[t.InfoHash().String()] = t.Name()
			case <-r.Context().Done():
				http.Error(w, "Request canceled", http.StatusRequestTimeout)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(torrents); err != nil {
			log.Printf("error encoding JSON response: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
	})
}

func HandlePostTorrents(c *torrent.Client, config *ClientConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("error reading request body: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		t, err := AddTorrent(c, string(body))
		if err != nil {
			log.Printf("%s error: %v", r.URL.Path, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		playlist, err := BuildPlaylist(t, config)
		if err != nil {
			log.Printf("%s error: %v", r.URL.Path, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, playlist)

		if !config.ResumeTorrents {
			return
		}

		if err := saveTorrentFile(config, t); err != nil {
			log.Print(err)
		}

	})
}

func HandleGetInfoHash(c *torrent.Client, config *ClientConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih := infohash.FromHexString(r.PathValue("infohash"))
		t, ok := c.Torrent(ih)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		playlist, err := BuildPlaylist(t, config)
		if err != nil {
			log.Printf("%s error: %v", r.URL.Path, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, playlist)
	})
}

func HandleDeleteInfoHash(c *torrent.Client, config *ClientConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih := infohash.FromHexString(r.PathValue("infohash"))
		t, ok := c.Torrent(ih)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		t.Drop()
	})
}

func HandleGetInfoHashFile(c *torrent.Client, config *ClientConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih := infohash.FromHexString(r.PathValue("infohash"))
		query := r.PathValue("query")

		t, ok := c.Torrent(ih)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		for _, file := range t.Files() {
			if file.DisplayPath() == query {
				reader := file.NewReader()
				defer reader.Close()

				reader.SetReadahead(config.Readahead)
				http.ServeContent(w, r, filepath.Base(file.DisplayPath()), time.Unix(t.Metainfo().CreationDate, 0), reader)
				break
			}
		}

	})
}

func HandleExit(cancel context.CancelFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "Shutdown initiated")
		cancel()
	})
}
