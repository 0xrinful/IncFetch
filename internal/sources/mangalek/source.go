package mangalek

import (
	"github.com/0xrinful/InkFetch/internal/requester"
	"github.com/0xrinful/InkFetch/internal/sources"
)

const (
	baseURL    = "https://mangalik.net"
	sourceID   = "mangalek"
	sourceName = "MangaLek"
)

func Config() sources.Config {
	return sources.Config{
		SourceID:          sourceID,
		BaseURL:           baseURL,
		CloudflareTestURL: baseURL + "/page/5/",
		NeedsFlare:        false,
		Headers: map[string]string{
			"Referer": baseURL + "/",
		},
	}
}

type Source struct {
	req     *requester.Requester
	baseURL string
}

func New(req *requester.Requester) sources.Source {
	return &Source{
		req:     req,
		baseURL: baseURL,
	}
}

func (s *Source) Info() sources.SourceInfo {
	return sources.SourceInfo{
		ID:   sourceID,
		Name: sourceName,
	}
}
