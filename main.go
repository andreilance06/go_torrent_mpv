package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/types/infohash"
	"golang.org/x/time/rate"
)

type ClientConfig struct {
	DeleteTorrentFilesOnExit bool
	DisableUTP               bool
	DownloadDir              string
	Port                     int
	Readahead                int64
	ResumeTorrents           bool
}

const (
	torrentPattern   = "\\.torrent$"
	infoHashPattern  = "^[0-9a-fA-F]{40}$"
	magnetPattern    = "^magnet:"
	httpPattern      = "^https?"
	shutdownTimeout  = 9 * time.Second
	defaultReadahead = 32 * 1024 * 1024 // 32 MB
	defaultHTTPPort  = 6969
)

func GetLocalIPs() ([]net.IP, error) {
	var ips []net.IP
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("failed to get local interface addresses: %w", err)
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

func InitClient(userConfig *ClientConfig) (*torrent.Client, storage.ClientImplCloser, error) {
	config := torrent.NewDefaultClientConfig()
	config.AlwaysWantConns = true
	db := storage.NewBoltDB(userConfig.DownloadDir)
	config.DefaultStorage = db
	config.DialRateLimiter = rate.NewLimiter(rate.Inf, 0)
	config.DisableUTP = userConfig.DisableUTP
	config.EstablishedConnsPerTorrent = 100
	config.Seed = true

	c, err := torrent.NewClient(config)
	if err != nil {
		return nil, nil, fmt.Errorf("error initializing torrent client: %w", err)
	}

	if !userConfig.ResumeTorrents {
		return c, db, nil
	}

	files, err := os.ReadDir(filepath.Join(userConfig.DownloadDir, "torrents"))
	if err != nil && !os.IsNotExist(err) {
		log.Printf("failed to retrieve saved torrents: %v", err)
	}

	for _, v := range files {
		_, err := AddTorrent(c, filepath.Join(userConfig.DownloadDir, "torrents", v.Name()))
		if err != nil {
			log.Printf(
				"failed to resume torrent %s: %v",
				v.Name(),
				err,
			)
		}
	}

	return c, db, nil
}

func InitServer(c *torrent.Client, config *ClientConfig) *http.Server {
	mux := http.NewServeMux()
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", config.Port),
		Handler: mux,
	}
	AddRoutes(mux, c, server, config)
	return server
}

func BuildPlaylist(t *torrent.Torrent, config *ClientConfig) (string, error) {
	<-t.GotInfo()

	ips, err := GetLocalIPs()
	if err != nil {
		return "", err
	}

	localIP := ips[0]
	playlist := []string{"#EXTM3U"}

	for _, file := range t.Files() {
		ext := mime.TypeByExtension(filepath.Ext(file.DisplayPath()))
		if strings.HasPrefix(ext, "video") {
			playlist = append(playlist, fmt.Sprintf("#EXTINF:0,%s", filepath.Base(file.DisplayPath())))
			playlist = append(playlist, fmt.Sprintf("http://%s:%d/torrents/%s/%s", localIP, config.Port, t.InfoHash(), file.DisplayPath()))
		}
	}

	return strings.Join(playlist, "\n"), nil
}

func AddTorrent(c *torrent.Client, id string) (*torrent.Torrent, error) {
	log.Printf("AddTorrent: got %s", id)

	switch {
	case isMatched(httpPattern, id):
		resp, err := http.Get(id)
		if err != nil {
			return nil, fmt.Errorf("failed to get torrent from URL: %w", err)
		}
		defer resp.Body.Close()

		metaInfo, err := metainfo.Load(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to load torrent metadata: %w", err)
		}

		return c.AddTorrent(metaInfo)

	case isMatched(torrentPattern, id):
		return c.AddTorrentFromFile(id)

	case isMatched(infoHashPattern, id):
		ih := infohash.FromHexString(id)
		t, _ := c.AddTorrentInfoHash(ih)
		return t, nil

	case isMatched(magnetPattern, id):
		return c.AddMagnet(id)

	default:
		return nil, errors.New("invalid torrent id")
	}
}

func isMatched(pattern, input string) bool {
	matched, _ := regexp.MatchString(pattern, input)
	return matched
}

func saveTorrentFile(config *ClientConfig, t *torrent.Torrent) error {
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

func gracefulShutdown(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("error shutting down server: %w", err)
	}

	log.Print("Server shutdown successfully")
	return nil
	}

func deleteDatabase(config *ClientConfig, db storage.ClientImplCloser) error {
	if err := db.Close(); err != nil {
		return fmt.Errorf("error closing database: %w", err)
	}
	if err := os.Remove(filepath.Join(config.DownloadDir, "bolt.db")); err != nil {
		return fmt.Errorf("error deleting database: %w", err)
	}
	return nil
}

func run(ctx context.Context, config *ClientConfig) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	c, db, err := InitClient(config)
	if err != nil {
		return err
	}
	defer func() {
		c.Close()
		<-c.Closed()
		log.Print("Torrent client shutdown successfully")
		if config.DeleteTorrentFilesOnExit {
			if err := deleteDatabase(config, db); err != nil {
				log.Print(err)
			}
		}
	}()

	server := InitServer(c, config)
	serverErr := make(chan error, 1)
	defer close(serverErr)

	go func() {
		serverErr <- server.ListenAndServe()
	}()

	log.Printf("Listening on %s...", server.Addr)
	select {
	case <-ctx.Done():
		log.Print("Shutdown initiated")
		if err := gracefulShutdown(server); err != nil {
			return err
		}
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %s", err)
		}
	}

	return nil
}

func main() {
	DeleteTorrentFilesOnExit := flag.Bool("DeleteTorrentFilesOnExit", false, "Delete downloaded files before exiting")
	DisableUTP := flag.Bool("DisableUTP", true, "Disables UTP")
	DownloadDir := flag.String("DownloadDir", os.TempDir(), "Directory where downloaded files are stored")
	Port := flag.Int("Port", defaultHTTPPort, "HTTP Server port")
	Readahead := flag.Int64("Readahead", defaultReadahead, "Bytes ahead of read to prioritize")
	ResumeTorrents := flag.Bool("ResumeTorrents", true, "Resume previous torrents on startup")
	flag.Parse()

	config := ClientConfig{
		DeleteTorrentFilesOnExit: *DeleteTorrentFilesOnExit,
		DisableUTP:               *DisableUTP,
		DownloadDir:              *DownloadDir,
		Port:                     *Port,
		Readahead:                *Readahead,
		ResumeTorrents:           *ResumeTorrents,
	}

	ctx := context.Background()
	if err := run(ctx, &config); err != nil {
		log.Fatal(err)
	}
}
