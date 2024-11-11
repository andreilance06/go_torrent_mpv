package torrentserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/anacrolix/squirrel"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/types/infohash"
	"github.com/andreilance06/go_torrent_mpv/internal/options"
	"github.com/andreilance06/go_torrent_mpv/internal/torrentclient"
)

func HandleGetTorrents(c *torrent.Client, config *options.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		parsed, err := MarshalTorrents(c, config)
		if err != nil {
			log.Printf("error encoding JSON response: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(parsed)))
		if r.Method == http.MethodHead {
			return
		}
		w.Write(parsed)
	})
}

func HandlePostTorrents(c *torrent.Client, config *options.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("error reading request body: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		t, err := torrentclient.AddTorrent(c, string(body))
		if err != nil {
			log.Printf("error adding torrent: %v", err)
			http.Error(w, fmt.Sprintf("Error adding torrent: %v", err), http.StatusBadRequest)
			return
		}

		<-t.GotInfo()
		files, err := WrapFiles(t.Files(), config)
		if err != nil {
			log.Printf("error building playlist: %v", err)
			http.Error(w, fmt.Sprintf("Error building playlist: %v", err), http.StatusInternalServerError)
			return
		}

		playlist := BuildPlaylist(files, config)

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, playlist)

		if !config.ResumeTorrents {
			return
		}

		if err := SaveTorrentFile(config, t); err != nil {
			log.Print(err)
		}

	})
}

func HandleGetInfoHash(c *torrent.Client, config *options.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih := infohash.FromHexString(r.PathValue("infohash"))
		t, ok := c.Torrent(ih)

		if !ok {
			http.Error(w, "Torrent not found", http.StatusNotFound)
			return
		}

		<-t.GotInfo()
		files, err := WrapFiles(t.Files(), config)
		if err != nil {
			log.Printf("error building playlist: %v", err)
			http.Error(w, fmt.Sprintf("Error building playlist %v", err), http.StatusInternalServerError)
			return
		}

		playlist := BuildPlaylist(files, config)

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, playlist)
	})
}

func HandleDeleteInfoHash(c *torrent.Client, config *options.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih := infohash.FromHexString(r.PathValue("infohash"))
		t, ok := c.Torrent(ih)
		if !ok {
			http.Error(w, "Torrent not found", http.StatusNotFound)
			return
		}
		defer func() {
			t.Drop()
			log.Printf("Dropped torrent: %s", t.Name())
		}()

		w.WriteHeader(http.StatusNoContent)
		w.Header().Set("Content-Length", "0")

		if r.URL.Query().Get("DeleteFiles") != "true" {
			return
		}

		if t.Info() == nil {
			log.Printf("skip deleting torrent data: torrent info not available")
			return
		}

		sq, err := squirrel.NewCache(torrentclient.CreateDBOptions(config))
		if err != nil {
			log.Printf("error opening database: %v", err)
			return
		}
		defer sq.Close()

		err = sq.Tx(func(tx *squirrel.Tx) error {
			for i := range t.NumPieces() {
				p := t.Piece(i)
				piece_hash := p.Info().V1Hash().Value.HexString()
				err := tx.Delete(piece_hash)
				if err != nil && !errors.Is(err, squirrel.ErrNotFound) {
					return fmt.Errorf("error deleting piece: %w", err)
				}
			}
			return nil
		})

		if err != nil {
			log.Printf("error deleting torrent data: %v", err)
		}

		err = os.Remove(filepath.Join(config.DownloadDir, "torrents", fmt.Sprintf("%s.torrent", t.Name())))
		if err != nil && !os.IsNotExist(err) {
			log.Printf("error deleting torrent file: %v", err)
		}
	})
}

func HandleGetInfoHashFile(c *torrent.Client, config *options.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih := infohash.FromHexString(r.PathValue("infohash"))
		query := r.PathValue("query")

		t, ok := c.Torrent(ih)
		if !ok {
			http.Error(w, "Torrent not found", http.StatusNotFound)
			return
		}

		<-t.GotInfo()
		for _, file := range t.Files() {
			if file.DisplayPath() == query {
				reader := file.NewReader()
				defer reader.Close()

				if config.Responsive {
					reader.SetResponsive()
				}
				if config.Readahead >= 0 {
					reader.SetReadahead(config.Readahead)
				}
				http.ServeContent(w, r, query, time.Unix(t.Metainfo().CreationDate, 0), reader)
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
