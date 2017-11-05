package feeds

import (
	"fmt"
	"log"
	"time"

	"github.com/go-pg/pg"
	itunes "github.com/mxpv/podcast"
	"github.com/mxpv/podsync/pkg/api"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/pkg/errors"
	"github.com/ventu-io/go-shortid"
)

const (
	maxPageSize = 150
)

const (
	MetricQueries   = "queries"
	MetricDownloads = "downloads"
)

type stats interface {
	Inc(metric, hashID string) (int64, error)
	Get(metric, hashID string) (int64, error)
}

type builder interface {
	Build(feed *model.Feed) (podcast *itunes.Podcast, err error)
}

type Service struct {
	sid      *shortid.Shortid
	stats    stats
	db       *pg.DB
	builders map[api.Provider]builder
}

func (s Service) CreateFeed(req *api.CreateFeedRequest, identity *api.Identity) (string, error) {
	feed, err := parseURL(req.URL)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create feed for URL: %s", req.URL)
	}

	// Make sure builder exists for this provider
	_, ok := s.builders[feed.Provider]
	if !ok {
		return "", fmt.Errorf("failed to get builder for URL: %s", req.URL)
	}

	now := time.Now().UTC()

	// Set default fields
	feed.PageSize = api.DefaultPageSize
	feed.Format = api.FormatVideo
	feed.Quality = api.QualityHigh
	feed.FeatureLevel = api.DefaultFeatures
	feed.CreatedAt = now
	feed.LastAccess = now

	if identity.FeatureLevel > 0 {
		feed.UserID = identity.UserId
		feed.Quality = req.Quality
		feed.Format = req.Format
		feed.FeatureLevel = identity.FeatureLevel
		feed.PageSize = req.PageSize
		if feed.PageSize > maxPageSize {
			feed.PageSize = maxPageSize
		}
	}

	// Generate short id
	hashId, err := s.sid.Generate()
	if err != nil {
		return "", errors.Wrap(err, "failed to generate id for feed")
	}

	feed.HashID = hashId

	// Save to database
	_, err = s.db.Model(feed).Insert()
	if err != nil {
		return "", errors.Wrap(err, "failed to save feed to database")
	}

	return hashId, nil
}

func (s Service) QueryFeed(hashID string) (*model.Feed, error) {
	lastAccess := time.Now().UTC()

	feed := &model.Feed{}
	res, err := s.db.Model(feed).
		Set("last_access = ?", lastAccess).
		Where("hash_id = ?", hashID).
		Returning("*").
		Update()

	if err != nil {
		return nil, errors.Wrapf(err, "failed to query feed: %s", hashID)
	}

	if res.RowsAffected() != 1 {
		return nil, api.ErrNotFound
	}

	return feed, nil
}

func (s Service) BuildFeed(hashID string) (*itunes.Podcast, error) {
	feed, err := s.QueryFeed(hashID)
	if err != nil {
		return nil, err
	}

	builder, ok := s.builders[feed.Provider]
	if !ok {
		return nil, errors.Wrapf(err, "failed to get builder for feed: %s", hashID)
	}

	podcast, err := builder.Build(feed)
	if err != nil {
		return nil, err
	}

	_, err = s.stats.Inc(MetricQueries, feed.HashID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to update metrics for feed: %s", hashID)
	}

	return podcast, nil
}

func (s Service) GetMetadata(hashID string) (*api.Metadata, error) {
	feed := &model.Feed{}
	err := s.db.
		Model(feed).
		Where("hash_id = ?", hashID).
		Column("provider", "format", "quality", "user_id").
		Select()

	if err != nil {
		return nil, err
	}

	downloads, err := s.stats.Inc(MetricDownloads, hashID)
	if err != nil {
		return nil, err
	}

	return &api.Metadata{
		Provider:  feed.Provider,
		Format:    feed.Format,
		Quality:   feed.Quality,
		Downloads: downloads,
	}, nil
}

func (s Service) Downgrade(patronID string, featureLevel int) error {
	log.Printf("Downgrading patron '%s' to feature level %d", patronID, featureLevel)

	if featureLevel == api.DefaultFeatures {
		res, err := s.db.
			Model(&model.Feed{}).
			Set("page_size = ?", 50).
			Set("feature_level = ?", 0).
			Set("format = ?", api.FormatVideo).
			Set("quality = ?", api.QualityHigh).
			Where("user_id = ?", patronID).
			Update()

		if err != nil {
			log.Printf("failed to downgrade patron '%s' to feature level %d: %v", patronID, featureLevel, err)
			return err
		}

		log.Printf("Updated %d feed(s) of user '%s' to feature level %d", res.RowsAffected(), patronID, featureLevel)
		return nil
	}

	return errors.New("unsupported downgrade type")
}

type feedOption func(*Service)

//noinspection GoExportedFuncWithUnexportedType
func WithPostgres(db *pg.DB) feedOption {
	return func(service *Service) {
		service.db = db
	}
}

//noinspection GoExportedFuncWithUnexportedType
func WithBuilder(provider api.Provider, builder builder) feedOption {
	return func(service *Service) {
		service.builders[provider] = builder
	}
}

//noinspection GoExportedFuncWithUnexportedType
func WithStats(m stats) feedOption {
	return func(service *Service) {
		service.stats = m
	}
}

func NewFeedService(opts ...feedOption) (*Service, error) {
	sid, err := shortid.New(1, shortid.DefaultABC, uint64(time.Now().UnixNano()))
	if err != nil {
		return nil, err
	}

	svc := &Service{
		sid:      sid,
		builders: make(map[api.Provider]builder),
	}

	for _, fn := range opts {
		fn(svc)
	}

	return svc, nil
}