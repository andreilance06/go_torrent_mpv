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
	defaultroute "github.com/nixigaj/go-default-route"
)

const (
	shutdownTimeout = 9 * time.Second
)

var localIP net.IP
var ipErr error

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

func BuildUrl(f *torrent.File, Port int) string {
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

	if localIP.To4() == nil || ipErr != nil {
		localIP, ipErr = GetLocalIP()
		if ipErr != nil {
			return nil, fmt.Errorf("error wrapping files: %w", ipErr)
		}
	}

	files := make([]FileInfo, 0, len(Files))
	for _, f := range Files {
		files = append(files, FileInfo{
			Name:     filepath.Base(f.DisplayPath()),
			URL:      BuildUrl(f, config.Port),
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

func GetLocalIP() (net.IP, error) {
	defaultRoute, err := defaultroute.DefaultRoute()
	if err != nil {
		return nil, fmt.Errorf("error getting local ip: %w", err)
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("error getting local ip: %w", err)
	}

	if defaultRoute.InterfaceIndex == 0 {
		return net.ParseIP("127.0.0.1").To4(), nil
	}

	var iface net.Interface
	for _, i := range interfaces {
		if i.Index == defaultRoute.InterfaceIndex {
			iface = i
			break
		}
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("error getting local ip: %w", err)
	}

	var _localIP net.IP
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip := ipnet.IP.To4(); ip != nil {
			_localIP = ip
			if ip[0] == 192 {
				break
			}
		}
	}

	return _localIP, nil
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
