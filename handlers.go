package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/types/infohash"
)

func HandleGetTorrents(c *torrent.Client, config *ClientConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		torrents := make(map[string]string)
		for _, t := range c.Torrents() {
			<-t.GotInfo()
			torrents[t.InfoHash().String()] = t.Name()
		}
		parsed, _ := json.Marshal(torrents)
		fmt.Fprint(w, string(parsed))
	})
}

func HandlePostTorrents(c *torrent.Client, config *ClientConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader := io.Reader(r.Body)
		body, _ := io.ReadAll(reader)
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

func HandleExit(server *http.Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Print("Received shutdown request")
		go gracefulShutdown(server)
	})
}
