package main

import (
	"context"
	"encoding/json"
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
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/squirrel"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	sqliteStorage "github.com/anacrolix/torrent/storage/sqlite"
	"github.com/anacrolix/torrent/types/infohash"
	"golang.org/x/sys/windows"
	"golang.org/x/time/rate"
)

type ClientConfig struct {
	DeleteDatabaseOnExit    bool
	DeleteDataOnTorrentDrop bool
	DisableUTP              bool
	DownloadDir             string
	MaxConnsPerTorrent      int
	Port                    int
	Readahead               int64
	Responsive              bool
	ResumeTorrents          bool

	Profiling bool
}

type TorrentInfo struct {
	Name     string
	InfoHash string
	Files    []FileInfo
}

type FileInfo struct {
	Name   string
	URL    string
	Length int64
}

const (
	torrentPattern   = "\\.torrent$"
	magnetPattern    = "^magnet:"
	infoHashPattern  = "^[0-9a-fA-F]{40}$"
	httpPattern      = "^https?"
	defaultHTTPPort  = 6969
	defaultMaxConns  = 200
	defaultReadahead = 32 * 1024 * 1024 // 32 MB
	shutdownTimeout  = 9 * time.Second
)

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

func MarshalTorrents(c *torrent.Client, config *ClientConfig) ([]byte, error) {
	torrents := make([]TorrentInfo, 0, len(c.Torrents()))

	for _, t := range c.Torrents() {
		<-t.GotInfo()

		torrentInfo, err := WrapTorrent(t, config)
		if err != nil {
			return nil, err
		}
		torrents = append(torrents, torrentInfo)

	}

	return json.Marshal(torrents)
}

func WrapTorrent(t *torrent.Torrent, config *ClientConfig) (TorrentInfo, error) {
	<-t.GotInfo()
	ips, err := GetLocalIPs()
	if err != nil {
		return TorrentInfo{}, err
	}

	localIP := ips[0]
	files := make([]FileInfo, 0, len(t.Files()))

	for _, f := range t.Files() {
		files = append(files, FileInfo{
			Name:   filepath.Base(f.DisplayPath()),
			URL:    BuildUrl(f, localIP, config.Port),
			Length: f.Length(),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})

	return TorrentInfo{
		Name:     t.Name(),
		InfoHash: t.InfoHash().String(),
		Files:    files,
	}, nil
}

func createDBOptions(config *ClientConfig) squirrel.NewCacheOpts {
	opts := squirrel.NewCacheOpts{}
	opts.SetAutoVacuum = generics.Some("incremental")
	opts.RequireAutoVacuum = generics.Some[any](2)
	opts.SetJournalMode = "wal"
	opts.SetSynchronous = 0
	opts.Path = filepath.Join(config.DownloadDir, "torrents.db")
	opts.Capacity = -1
	opts.MmapSizeOk = true
	opts.MmapSize = 64 << 20
	opts.CacheSize = generics.Some[int64](-32 << 20)
	opts.SetLockingMode = "normal"
	opts.JournalSizeLimit.Set(256 << 20)

	return opts
}

func InitStorage(config *ClientConfig) (storage.ClientImplCloser, error) {
	return sqliteStorage.NewDirectStorage(createDBOptions(config))
}

func InitClient(userConfig *ClientConfig, db storage.ClientImplCloser) (*torrent.Client, error) {
	config := torrent.NewDefaultClientConfig()
	config.AlwaysWantConns = true
	config.DefaultStorage = db
	config.DialRateLimiter = rate.NewLimiter(rate.Inf, 0)
	config.DisableUTP = userConfig.DisableUTP
	config.EstablishedConnsPerTorrent = userConfig.MaxConnsPerTorrent
	config.Seed = true

	c, err := torrent.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("error initializing torrent client: %w", err)
	}

	if !userConfig.ResumeTorrents {
		return c, nil
	}

	files, err := os.ReadDir(filepath.Join(userConfig.DownloadDir, "torrents"))
	if err != nil && !os.IsNotExist(err) {
		log.Printf("error retrieving saved torrents: %v", err)
	}

	for _, v := range files {
		_, err := AddTorrent(c, filepath.Join(userConfig.DownloadDir, "torrents", v.Name()))
		if err != nil {
			log.Printf(
				"error resuming torrent %s: %v",
				v.Name(),
				err,
			)
		}
	}

	return c, nil
}

func InitServer(c *torrent.Client, config *ClientConfig, cancel context.CancelFunc) *http.Server {
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

func BuildUrl(f *torrent.File, localIP net.IP, Port int) string {
	return fmt.Sprintf("http://%s:%d/torrents/%s/%s", localIP, Port, f.Torrent().InfoHash(), f.DisplayPath())
}

func BuildPlaylist(t *torrent.Torrent, config *ClientConfig) (string, error) {
	<-t.GotInfo()

	torrentInfo, err := WrapTorrent(t, config)
	if err != nil {
		return "", err
	}

	playlist := []string{"#EXTM3U"}
	files := torrentInfo.Files

	for _, file := range files {
		ext := mime.TypeByExtension(filepath.Ext(file.Name))
		if strings.HasPrefix(ext, "video") {
			playlist = append(playlist, fmt.Sprintf("#EXTINF:0,%s", file.Name))
			playlist = append(playlist, file.URL)
		}
	}

	return strings.Join(playlist, "\n"), nil
}

func AddTorrent(c *torrent.Client, id string) (*torrent.Torrent, error) {
	log.Printf("Adding torrent: %s", id)

	switch {
	case isMatched(httpPattern, id):
		resp, err := http.Get(id)
		if err != nil {
			return nil, fmt.Errorf("error getting torrent from URL: %w", err)
		}
		defer resp.Body.Close()

		metaInfo, err := metainfo.Load(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("error loading torrent metadata: %w", err)
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
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	db, err := InitStorage(config)
	if err != nil {
		return err
	}

	c, err := InitClient(config, db)
	if err != nil {
		return err
	}
	log.Print("Torrent client started")

	defer func() {
		errs := c.Close()
		<-c.Closed()
		for _, err := range errs {
			log.Printf("error shutting down client: %v", err)
		}
		log.Print("Torrent client shutdown successfully")
		if config.DeleteDatabaseOnExit {
			if err := deleteDatabase(config, db); err != nil {
				log.Print(err)
			}
		}
	}()

	server := InitServer(c, config, cancel)
	log.Printf("Listening on %s...", server.Addr)

	<-ctx.Done()
	log.Print("Shutdown signal received")
	if err := gracefulShutdown(server); err != nil {
		return err
	}

	return nil
}

func main() {
	DeleteDatabaseOnExit := flag.Bool("DeleteDatabaseOnExit", false, "Delete all downloaded files before exiting")
	DeleteDataOnTorrentDrop := flag.Bool("DeleteDataOnTorrentDrop", false, "Delete a torrent's files after it is dropped")
	DisableUTP := flag.Bool("DisableUTP", true, "Disables UTP")
	DownloadDir := flag.String("DownloadDir", os.TempDir(), "Directory where downloaded files are stored")
	MaxConnsPerTorrent := flag.Int("MaxConnsPerTorrent", defaultMaxConns, "Maximum connections per torrent")
	Port := flag.Int("Port", defaultHTTPPort, "HTTP Server port")
	Readahead := flag.Int64("Readahead", defaultReadahead, "Bytes ahead of read to prioritize. Set to a negative value to use the default readahead function.")
	Responsive := flag.Bool("Responsive", false, "Read calls return as soon as possible without waiting for pieces to be verified.")
	ResumeTorrents := flag.Bool("ResumeTorrents", true, "Resume previous torrents on startup")
	Profiling := flag.Bool("Profiling", false, "Add pprof handlers for profiling")
	flag.Parse()

	config := ClientConfig{
		DeleteDatabaseOnExit:    *DeleteDatabaseOnExit,
		DeleteDataOnTorrentDrop: *DeleteDataOnTorrentDrop,
		DisableUTP:              *DisableUTP,
		DownloadDir:             *DownloadDir,
		MaxConnsPerTorrent:      *MaxConnsPerTorrent,
		Port:                    *Port,
		Readahead:               *Readahead,
		Responsive:              *Responsive,
		ResumeTorrents:          *ResumeTorrents,

		Profiling: *Profiling,
	}

	_, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/torrents", config.Port))

	if err == nil {
		log.Fatalf("server already listening on port %d", config.Port)
	}

	if err != nil && !errors.Is(err, windows.WSAECONNREFUSED) {
		log.Fatalf("error checking if server already exists: %v", err)
	}

	ctx := context.Background()
	if err := run(ctx, &config); err != nil {
		log.Fatal(err)
	}
}
