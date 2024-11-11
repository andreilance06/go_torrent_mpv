package options

import (
	"flag"
	"os"
)

const (
	defaultHTTPPort  = 6969
	defaultMaxConns  = 200
	defaultReadahead = 32 * 1024 * 1024 // 32 MB
)

type Config struct {
	DownloadDir        string
	ListenAddr         string
	LocalAddr          string
	MaxConnsPerTorrent int
	Port               int
	Readahead          int64
	Responsive         bool
	ResumeTorrents     bool
	Profiling          bool
}

func ParseFlags() *Config {
	config := &Config{}

	flag.StringVar(&config.DownloadDir, "DownloadDir", os.TempDir(), "Directory where downloaded files are stored")
	flag.StringVar(&config.ListenAddr, "ListenAddr", ":0", "Address to listen for incoming connections")
	flag.StringVar(&config.LocalAddr, "LocalAddr", ":0", "Address to use for outgoing connections")
	flag.IntVar(&config.MaxConnsPerTorrent, "MaxConnsPerTorrent", defaultMaxConns, "Maximum connections per torrent")
	flag.IntVar(&config.Port, "Port", defaultHTTPPort, "HTTP Server port")
	flag.Int64Var(&config.Readahead, "Readahead", defaultReadahead, "Bytes ahead of read to prioritize")
	flag.BoolVar(&config.Responsive, "Responsive", false, "Read calls return as soon as possible")
	flag.BoolVar(&config.ResumeTorrents, "ResumeTorrents", true, "Resume previous torrents on startup")
	flag.BoolVar(&config.Profiling, "Profiling", false, "Add pprof handlers for profiling")

	flag.Parse()
	return config
}
