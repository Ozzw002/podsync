//go:generate mockgen -source=feeds.go -destination=feeds_mock_test.go -package=feeds

package feeds

import (
	"testing"

	"github.com/go-pg/pg"
	"github.com/golang/mock/gomock"
	"github.com/mxpv/podsync/pkg/api"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/stretchr/testify/require"
	"github.com/ventu-io/go-shortid"
)

var feed = &model.Feed{
	HashID:   "123",
	ItemID:   "xyz",
	Provider: api.ProviderVimeo,
	LinkType: api.LinkTypeChannel,
	PageSize: 50,
	Quality:  api.QualityHigh,
	Format:   api.FormatVideo,
}

func TestService_CreateFeed(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	s := Service{
		sid:      shortid.GetDefault(),
		db:       createDatabase(t),
		builders: map[api.Provider]builder{api.ProviderYoutube: nil},
	}

	req := &api.CreateFeedRequest{
		URL:      "youtube.com/channel/123",
		PageSize: 50,
		Quality:  api.QualityHigh,
		Format:   api.FormatVideo,
	}

	hashId, err := s.CreateFeed(req, &api.Identity{})
	require.NoError(t, err)
	require.NotEmpty(t, hashId)
}

func TestService_GetFeed(t *testing.T) {
	s := Service{db: createDatabase(t)}

	_, err := s.BuildFeed(feed.HashID)
	require.NoError(t, err)
}

func TestService_WrongID(t *testing.T) {
	s := Service{db: createDatabase(t)}

	_, err := s.BuildFeed("invalid_feed_id")
	require.Error(t, err)
}

func TestService_UpdateLastAccess(t *testing.T) {
	s := Service{db: createDatabase(t)}

	feed1, err := s.QueryFeed(feed.HashID)
	require.NoError(t, err)

	feed2, err := s.QueryFeed(feed.HashID)
	require.NoError(t, err)

	require.True(t, feed2.LastAccess.After(feed1.LastAccess))
}

func TestService_GetMetadata(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	stats := NewMockstats(ctrl)
	stats.EXPECT().Inc(MetricDownloads, feed.HashID).Return(int64(10), nil)

	s := Service{
		db:    createDatabase(t),
		stats: stats,
	}

	m, err := s.GetMetadata(feed.HashID)
	require.NoError(t, err)
	require.Equal(t, int64(10), m.Downloads)
}

func TestService_DowngradeToAnonymous(t *testing.T) {
	s := Service{db: createDatabase(t)}

	feed := &model.Feed{
		HashID:       "123456",
		UserID:       "123456",
		ItemID:       "123456",
		Provider:     api.ProviderVimeo,
		LinkType:     api.LinkTypeGroup,
		PageSize:     150,
		Quality:      api.QualityLow,
		Format:       api.FormatAudio,
		FeatureLevel: api.ExtendedFeatures,
	}

	err := s.db.Insert(feed)
	require.NoError(t, err)

	err = s.Downgrade(feed.UserID, api.DefaultFeatures)
	require.NoError(t, err)

	downgraded := &model.Feed{FeedID: feed.FeedID}
	err = s.db.Select(downgraded)
	require.NoError(t, err)

	require.Equal(t, 50, downgraded.PageSize)
	require.Equal(t, api.QualityHigh, downgraded.Quality)
	require.Equal(t, api.FormatVideo, downgraded.Format)
	require.Equal(t, api.DefaultFeatures, downgraded.FeatureLevel)
}

func createDatabase(t *testing.T) *pg.DB {
	opts, err := pg.ParseURL("postgres://postgres:@localhost/podsync?sslmode=disable")
	if err != nil {
		require.NoError(t, err)
	}

	db := pg.Connect(opts)

	_, err = db.Model(&model.Feed{}).Where("1=1").Delete()
	require.NoError(t, err)

	err = db.Insert(feed)
	require.NoError(t, err)

	return db
}