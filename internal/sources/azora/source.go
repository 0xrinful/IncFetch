package azora

import (
	"github.com/0xrinful/InkFetch/internal/requester"
	"github.com/0xrinful/InkFetch/internal/sources"
)

const (
	baseURL    = "https://azorafly.com"
	apiURL     = "https://api.azorafly.com"
	sourceID   = "azora"
	sourceName = "Azora"
)

func Config() sources.Config {
	return sources.Config{
		SourceID:   sourceID,
		BaseURL:    baseURL,
		NeedsFlare: false,
		Headers: map[string]string{
			"Referer": baseURL + "/",
			"Origin":  baseURL,
		},
	}
}

type Source struct {
	req     *requester.Requester
	baseURL string
	apiURL  string
}

func New(req *requester.Requester) sources.Source {
	return &Source{
		req:     req,
		baseURL: baseURL,
		apiURL:  apiURL,
	}
}

func (s *Source) Info() sources.SourceInfo {
	return sources.SourceInfo{
		ID:   sourceID,
		Name: sourceName,
	}
}
