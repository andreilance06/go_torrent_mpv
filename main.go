package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

func GetLocalIPs() ([]net.IP, error) {
	var ips []net.IP
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}

	for _, addr := range addresses {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ips = append(ips, ipnet.IP)
			}
		}
	}
	return ips, nil
}

func InitClient(userConfig *ClientConfig) (*torrent.Client, error) {
	config := torrent.NewDefaultClientConfig()

	config.DefaultStorage = storage.NewBoltDB(userConfig.DownloadDir)
	config.DisableUTP = userConfig.DisableUTP
	config.DisableWebseeds = false
	config.DisableWebtorrent = false
	config.Seed = true

	c, err := torrent.NewClient(config)
	return c, err
}

func InitServer(c *torrent.Client, config *ClientConfig, serverErr chan error) *http.Server {
	mux := http.NewServeMux()
	AddRoutes(mux, c, config,  serverErr)
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", config.Port),
		Handler: mux,
	}
	return srv
}

func BuildPlaylist(t *torrent.Torrent, config *ClientConfig) (string, error) {
	<-t.GotInfo()
	playlist := []string{"#EXTM3U"}

	ips, err := GetLocalIPs()
	if err != nil {
		return "", err
	}
	localIp := ips[0]

	for _, file := range t.Files() {
		ext := mime.TypeByExtension(filepath.Ext(file.DisplayPath()))
		if strings.HasPrefix(ext, "video") {
			playlist = append(playlist, "#EXTINF:0,"+filepath.Base(file.DisplayPath()))
			playlist = append(playlist, fmt.Sprintf("http://%s:%d/torrents/%s/%s", localIp, config.Port, t.InfoHash(), file.DisplayPath()))
		}
	}

	return strings.Join(playlist, "\n"), nil
}

func AddTorrent(c *torrent.Client, id string) (*torrent.Torrent, error) {

	log.Printf("AddTorrent: got %s", id)
	matched, err := regexp.MatchString("^https?", id)
	if err == nil && matched {
		resp, httpErr := http.Get(id)
		if httpErr != nil {
			return nil, httpErr
		}
		defer resp.Body.Close()

		reader := io.Reader(resp.Body)
		metainf, loadErr := metainfo.Load(reader)
		if loadErr != nil {
			return nil, loadErr
		}

		t, addErr := c.AddTorrent(metainf)
		if addErr != nil {
			return nil, addErr
		}

		log.Print("remote")
		return t, nil
	}

	matched, err = regexp.MatchString("\\.torrent$", id)
	if err == nil && matched {
		t, addErr := c.AddTorrentFromFile(id)
		if addErr != nil {
			return nil, addErr
		}
		log.Print("file")
		return t, nil
	}

	matched, err = regexp.MatchString("^[0-9a-fA-F]{40}$", id)
	if err == nil && matched {
		ih := infohash.FromHexString(id)
		t, _ := c.AddTorrentInfoHash(ih)

		log.Print("infohash")
		return t, nil
	}

	matched, err = regexp.MatchString("^magnet:", id)
	if err == nil && matched {
		t, addErr := c.AddMagnet(id)
		if addErr != nil {
			return nil, addErr
		}
		log.Print("magnet")
		return t, nil
	}

	return nil, errors.New("invalid torrent id")
}

func run(ctx context.Context, config *ClientConfig) []error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	errs := make([]error, 0)

	defer func() {
		err := os.Remove(filepath.Join(config.DownloadDir, "bolt.db"))
		if err != nil {
			errs = append(errs, err)
		}
	}()

	c, err := InitClient(config)
	if err != nil {
		errs = append(errs, err)
		return errs
	}
	defer c.Close()

	serverErr := make(chan error)
	defer close(serverErr)
	server := InitServer(c, config, serverErr)

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
		}
	case err := <-serverErr:
		if err != nil {
			errs = append(errs, err)
		}
	}

	serverCtx, serverCancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer serverCancel()
	err = server.Shutdown(serverCtx)
	if err != nil {
		errs = append(errs, err)
	}

	return errs
}

func main() {
	DisableUTP := flag.Bool("DisableUTP", true, "Disables UTP")
	DownloadDir := flag.String("DownloadDir", os.TempDir(), "Directory where downloaded files are stored")
	Port := flag.Int("Port", 6969, "HTTP Server port")
	Readahead := flag.Int64("Readahead", 32*1024*1024, "Number of bytes ahead of a read that should also be prioritized in preparation for futher reads")
	flag.Parse()

	config := ClientConfig{*DisableUTP, *DownloadDir, *Port, *Readahead}

	ctx := context.Background()
	if err := run(ctx, &config); len(err) > 0 {
		log.Fatal(err)
	}
}
