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
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/types/infohash"
)

type ClientConfig struct {
	DisableUTP  bool
	DownloadDir string
	Port        int
	Readahead   int64
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

func InitClient(userConfig *ClientConfig) (*torrent.Client, error) {
	config := torrent.NewDefaultClientConfig()
	config.DefaultStorage = storage.NewBoltDB(userConfig.DownloadDir)
	config.DisableUTP = userConfig.DisableUTP
	config.Seed = true

	c, err := torrent.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize torrent client: %w", err)
	}
	return c, nil
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

func gracefulShutdown(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("error during server shutdown: %w", err)
	}

	log.Print("Server shutdown completed")
	return nil
}

func run(ctx context.Context, config *ClientConfig) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	c, err := InitClient(config)
	if err != nil {
		return err
	}
	defer c.Close()

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
	DisableUTP := flag.Bool("DisableUTP", true, "Disables UTP")
	DownloadDir := flag.String("DownloadDir", os.TempDir(), "Directory where downloaded files are stored")
	Port := flag.Int("Port", defaultHTTPPort, "HTTP Server port")
	Readahead := flag.Int64("Readahead", defaultReadahead, "Bytes ahead of read to prioritize")
	flag.Parse()

	config := ClientConfig{
		DisableUTP:  *DisableUTP,
		DownloadDir: *DownloadDir,
		Port:        *Port,
		Readahead:   *Readahead,
	}

	ctx := context.Background()
	if err := run(ctx, &config); err != nil {
		log.Fatal(err)
	}
}
