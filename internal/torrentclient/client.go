package torrentclient

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"

	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/dialer"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/types/infohash"
	"github.com/andreilance06/go_torrent_mpv/internal/options"
	"golang.org/x/time/rate"
)

const (
	torrentPattern  = "\\.torrent$"
	magnetPattern   = "^magnet:"
	infoHashPattern = "^[0-9a-fA-F]{40}$"
	httpPattern     = "^https?"
)

type TcpSocket struct {
	torrent.Listener
	torrent.NetworkDialer
}

func isMatched(pattern, input string) bool {
	matched, _ := regexp.MatchString(pattern, input)
	return matched
}

func InitClient(userConfig *options.Config, db storage.ClientImplCloser, ctx context.Context) (*torrent.Client, error) {
	config := torrent.NewDefaultClientConfig()
	config.AlwaysWantConns = true
	config.DefaultStorage = db
	config.DialRateLimiter = rate.NewLimiter(rate.Inf, 0)
	config.DisableTCP = true
	config.DisableUTP = true
	config.EstablishedConnsPerTorrent = userConfig.MaxConnsPerTorrent
	config.Seed = true

	c, err := torrent.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("error initializing torrent client: %w", err)
	}

	_, _, err = missinggo.ParseHostPort(userConfig.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("error parsing listen address: %w", err)
	}

	TcpListenConfig := net.ListenConfig{KeepAlive: -1}
	l, err := TcpListenConfig.Listen(ctx, "tcp", userConfig.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("error listening for tcp connections: %w", err)
	}

	localAddr, err := net.ResolveTCPAddr("tcp", userConfig.LocalAddr)
	if err != nil {
		return nil, fmt.Errorf("error resolving local address: %w", err)
	}

	_dialerTCP := &net.Dialer{
		FallbackDelay: -1,
		KeepAlive:     -1,
		LocalAddr:     localAddr,
		Control: func(network, address string, c syscall.RawConn) error {
			_ = c.Control(func(fd uintptr) {
				syscall.SetsockoptLinger(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_LINGER, &syscall.Linger{Onoff: 0, Linger: 0})
			})
			return nil
		},
	}
	s := TcpSocket{
		Listener:      l,
		NetworkDialer: dialer.WithNetwork{Network: "tcp", Dialer: _dialerTCP},
	}
	c.AddDialer(s)
	c.AddListener(s)

	if !userConfig.ResumeTorrents {
		return c, nil
	}

	files, err := os.ReadDir(filepath.Join(userConfig.DownloadDir, "torrents"))
	if err != nil && !os.IsNotExist(err) {
		log.Printf("error retrieving saved torrents: %v", err)
		return c, nil
	}

	wg := sync.WaitGroup{}
	for _, v := range files {
		wg.Add(1)
		go func() {
			_, err := AddTorrent(c, filepath.Join(userConfig.DownloadDir, "torrents", v.Name()))
			if err != nil {
				log.Printf(
					"error resuming torrent %s: %v",
					v.Name(),
					err,
				)
			}
			wg.Done()
		}()
	}
	wg.Wait()

	return c, nil
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
