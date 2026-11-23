package registry

import (
	"sort"

	"github.com/0xrinful/InkFetch/internal/downloader"
	"github.com/0xrinful/InkFetch/internal/requester"
	"github.com/0xrinful/InkFetch/internal/requester/flare"
	"github.com/0xrinful/InkFetch/internal/sources"
	"github.com/0xrinful/InkFetch/internal/sources/azora"
	"github.com/0xrinful/InkFetch/internal/sources/mangalek"
	"github.com/0xrinful/InkFetch/internal/sources/teamx"
)

type Registry struct {
	sources      map[string]sources.Source
	downloaders  map[string]*downloader.Downloader
	flareEnabled bool
}

func NewRegistry(solver *flare.Solver, store requester.SessionStore) *Registry {
	r := &Registry{
		sources:      make(map[string]sources.Source),
		downloaders:  make(map[string]*downloader.Downloader),
		flareEnabled: solver != nil,
	}

	r.register(solver, store, mangalek.Config(), mangalek.New)
	r.register(solver, store, azora.Config(), azora.New)
	r.register(solver, store, teamx.Config(), teamx.New)

	return r
}

func (r *Registry) register(
	solver *flare.Solver,
	store requester.SessionStore,
	cfg sources.Config,
	newSource func(*requester.Requester) sources.Source,
) {
	req := requester.NewWithStore(solver, store, cfg)
	source := newSource(req)
	dl := downloader.New(req)

	id := source.Info().ID
	r.sources[id] = source
	r.downloaders[id] = dl
}

func (r *Registry) Source(id string) (sources.Source, bool) {
	s, ok := r.sources[id]
	return s, ok
}

func (r *Registry) Downloader(id string) (*downloader.Downloader, bool) {
	d, ok := r.downloaders[id]
	return d, ok
}

func (r *Registry) AllSources() []sources.Source {
	list := make([]sources.Source, 0, len(r.sources))
	for _, s := range r.sources {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Info().Name < list[j].Info().Name
	})
	return list
}

func (r *Registry) Sources() []sources.SourceInfo {
	infos := make([]sources.SourceInfo, 0, len(r.sources))
	for _, s := range r.sources {
		infos = append(infos, s.Info())
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos
}

func (r *Registry) FlareEnabled() bool {
	return r.flareEnabled
}
