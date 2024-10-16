package main

import (
	"context"
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

		parsed, err := MarshalTorrents(c, config)
		if err != nil {
			log.Printf("error encoding JSON response: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodHead {
			return
		}
		w.Write(parsed)
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
			log.Printf("error adding torrent: %v", err)
			http.Error(w, fmt.Sprintf("Error adding torrent: %v", err), http.StatusBadRequest)
			return
		}

		playlist, err := BuildPlaylist(t, config)
		if err != nil {
			log.Printf("error building playlist: %v", err)
			http.Error(w, fmt.Sprintf("Error building playlist: %v", err), http.StatusInternalServerError)
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
			http.Error(w, "Torrent not found", http.StatusNotFound)
			return
		}

		playlist, err := BuildPlaylist(t, config)
		if err != nil {
			log.Printf("error building playlist: %v", err)
			http.Error(w, fmt.Sprintf("Error building playlist %v", err), http.StatusInternalServerError)
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
			http.Error(w, "Torrent not found", http.StatusNotFound)
			return
		}

		t.Drop()
		w.WriteHeader(http.StatusNoContent)
	})
}

func HandleGetInfoHashFile(c *torrent.Client, config *ClientConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih := infohash.FromHexString(r.PathValue("infohash"))
		query := r.PathValue("query")

		t, ok := c.Torrent(ih)
		if !ok {
			http.Error(w, "Torrent not found", http.StatusNotFound)
			return
		}

		for _, file := range t.Files() {
			if file.DisplayPath() == query {
				reader := file.NewReader()
				defer reader.Close()

				reader.SetResponsive()
				reader.SetReadahead(config.Readahead)
				http.ServeContent(w, r, filepath.Base(query), time.Unix(t.Metainfo().CreationDate, 0), reader)
				return
			}
		}

		http.Error(w, "File not found", http.StatusNotFound)
	})
}

func HandleExit(cancel context.CancelFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "Shutdown initiated")
		cancel()
	})
}
