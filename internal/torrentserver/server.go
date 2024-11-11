package torrentserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/andreilance06/go_torrent_mpv/internal/options"
)

const (
	shutdownTimeout = 9 * time.Second
)

type TorrentInfo struct {
	Name     string
	InfoHash string
	Files    []FileInfo
	Length   int64
	Playlist string
}

type FileInfo struct {
	Name     string
	URL      string
	Length   int64
	MimeType string
	depth    int
}

func GetLocalIPs() ([]net.IP, error) {
	var ips []net.IP
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("error getting local interface addresses: %w", err)
	}

	for _, addr := range addresses {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip := ipnet.IP.To4(); ip != nil {
			ips = append(ips, ip)
		}
	}
	return ips, nil
}

func SaveTorrentFile(config *options.Config, t *torrent.Torrent) error {
	err := os.MkdirAll(filepath.Join(config.DownloadDir, "torrents"), 0o777)
	if err != nil {
		return fmt.Errorf("error creating torrents directory: %w", err)
	}

	f, err := os.Create(filepath.Join(config.DownloadDir, "torrents", fmt.Sprintf("%s.torrent", t.Name())))
	if err != nil {
		return fmt.Errorf("error creating torrent file: %w", err)
	}
	defer f.Close()

	infoBytes := t.Metainfo()
	if err := infoBytes.Write(f); err != nil {
		return fmt.Errorf("error writing torrent file: %w", err)
	}

	return nil
}

func BuildUrl(f *torrent.File, localIP net.IP, Port int) string {
	return fmt.Sprintf("http://%s:%d/torrents/%s/%s", localIP, Port, f.Torrent().InfoHash(), f.DisplayPath())
}

func BuildPlaylist(files []FileInfo, config *options.Config) string {

	playlist := []string{"#EXTM3U"}

	for _, file := range files {
		ext := mime.TypeByExtension(filepath.Ext(file.Name))
		if strings.HasPrefix(ext, "video") {
			playlist = append(playlist, fmt.Sprintf("#EXTINF:0,%s", file.Name))
			playlist = append(playlist, file.URL)
		}
	}

	return strings.Join(playlist, "\n")
}

func MarshalTorrents(c *torrent.Client, config *options.Config) ([]byte, error) {
	torrents := make([]TorrentInfo, 0, len(c.Torrents()))

	for _, t := range c.Torrents() {
		<-t.GotInfo()

		torrentInfo, err := WrapTorrent(t, config)
		if err != nil {
			return nil, err
		}
		torrents = append(torrents, torrentInfo)

	}

	sort.Slice(torrents, func(i, j int) bool {
		return torrents[i].Name < torrents[j].Name
	})

	return json.Marshal(torrents)
}

func WrapTorrent(t *torrent.Torrent, config *options.Config) (TorrentInfo, error) {
	<-t.GotInfo()
	files, err := WrapFiles(t.Files(), config)
	if err != nil {
		return TorrentInfo{}, err
	}

	var torrentLength int64
	for _, file := range files {
		torrentLength += file.Length
	}

	playlist := BuildPlaylist(files, config)

	return TorrentInfo{
		Name:     t.Name(),
		InfoHash: t.InfoHash().String(),
		Files:    files,
		Length:   torrentLength,
		Playlist: playlist,
	}, nil
}

func WrapFiles(Files []*torrent.File, config *options.Config) ([]FileInfo, error) {
	files := make([]FileInfo, 0, len(Files))

	ips, err := GetLocalIPs()
	if err != nil {
		return files, err
	}

	var localIP net.IP
	for _, ip := range ips {
		if ip[0] == 192 {
			localIP = ip
			break
		}
	}

	if localIP == nil {
		return files, errors.New("error wrapping files: no local ip found")
	}

	for _, f := range Files {
		files = append(files, FileInfo{
			Name:     filepath.Base(f.DisplayPath()),
			URL:      BuildUrl(f, localIP, config.Port),
			Length:   f.Length(),
			MimeType: mime.TypeByExtension(filepath.Ext(f.DisplayPath())),
			depth:    len(strings.Split(f.DisplayPath(), "/")),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].depth != files[j].depth {
			return (files[i].depth < files[j].depth)
		}

		return files[i].Name < files[j].Name
	})

	return files, nil
}

func InitServer(c *torrent.Client, config *options.Config, cancel context.CancelFunc) *http.Server {
	mux := http.NewServeMux()
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", config.Port),
		Handler: mux,
	}
	RegisterRoutes(mux, c, config, cancel)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("error on server ListenAndServe: %v", err)
		}
		cancel()
	}()

	return server
}

func GracefulShutdown(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("error shutting down server: %w", err)
	}

	log.Print("Server shutdown successfully")
	return nil
}
