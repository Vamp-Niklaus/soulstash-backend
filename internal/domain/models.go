package domain

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// User represents a registered application user
type User struct {
	ID                   bson.ObjectID    `bson:"_id,omitempty" json:"id"`
	Username             string           `bson:"username" json:"username"`
	Email                string           `bson:"email,omitempty" json:"email,omitempty"`
	Password             string           `bson:"password,omitempty" json:"-"`
	FullName             string           `bson:"fullName" json:"fullName"`
	FirstName            string           `bson:"firstName" json:"firstName"`
	LastName             string           `bson:"lastName" json:"lastName"`
	DateOfBirth          string           `bson:"dateOfBirth,omitempty" json:"dateOfBirth,omitempty"`
	Bio                  string           `bson:"bio,omitempty" json:"bio,omitempty"`
	InstagramHandle      string           `bson:"instagramHandle,omitempty" json:"instagramHandle,omitempty"`
	XHandle              string           `bson:"xHandle,omitempty" json:"xHandle,omitempty"`
	YoutubeHandle        string           `bson:"youtubeHandle,omitempty" json:"youtubeHandle,omitempty"`
	Avatar               string           `bson:"avatar,omitempty" json:"avatar,omitempty"`
	Admin                bool             `bson:"admin" json:"admin"`
	AdminMode            int              `bson:"adminMode,omitempty" json:"adminMode,omitempty"`
	ShowAdult            bool             `bson:"showAdult" json:"showAdult"`
	CollectionVersion    int              `bson:"collectionVersion" json:"collectionVersion"`
	Collections          []UserCollection `bson:"collections" json:"collections"`
	FavoritePeople       []any            `bson:"favoritePeople" json:"favoritePeople"`
	FavoritePeoplePublic bool             `bson:"favoritePeoplePublic,omitempty" json:"favoritePeoplePublic,omitempty"`
	Followers            []string         `bson:"followers" json:"followers"`
	Following            []string         `bson:"following" json:"following"`
	CreatedAt            time.Time        `bson:"createdAt" json:"createdAt"`
	UpdatedAt            time.Time        `bson:"updatedAt" json:"updatedAt"`
}

// UserCollection represents a user's movie/series collection
type UserCollection struct {
	ID          any       `bson:"_id,omitempty" json:"_id"`
	Name        string    `bson:"name" json:"name"`
	Description string    `bson:"description,omitempty" json:"description"`
	IsDeletable bool      `bson:"isDeletable" json:"isDeletable"`
	IsPublic    bool      `bson:"isPublic" json:"isPublic"`
	IsPublished bool      `bson:"isPublished" json:"isPublished"`
	Visibility  string    `bson:"visibility,omitempty" json:"visibility,omitempty"`
	Banner      string    `bson:"banner" json:"banner"`
	MovieCount  int       `bson:"movieCount" json:"movieCount"`
	Movies      []any     `bson:"movies" json:"movies"`
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time `bson:"updatedAt" json:"updatedAt"`
}

// UserClaims represents the JWT token claims
type UserClaims struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
}

// TrafficLog represents a persisted HTTP request log
type TrafficLog struct {
	ID             bson.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	IP             string        `bson:"ip" json:"ip"`
	Domain         string        `bson:"domain" json:"domain"`
	Path           string        `bson:"path" json:"path"`
	Method         string        `bson:"method" json:"method"`
	StatusCode     int           `bson:"statusCode" json:"statusCode"`
	ResponseTimeMs int64         `bson:"responseTimeMs" json:"responseTimeMs"`
	Username       string        `bson:"username" json:"username"`
	UserAgent      string        `bson:"userAgent" json:"userAgent"`
	Device         string        `bson:"device" json:"device"`
	Country        string        `bson:"country" json:"country"`
	City           string        `bson:"city" json:"city"`
	LL             []float64     `bson:"ll,omitempty" json:"ll"`
	Referer        string        `bson:"referer" json:"referer"`
	CreatedAt      time.Time     `bson:"createdAt" json:"createdAt"`
}

// RatingRecord represents cached rating for TMDB / IMDb media
type RatingRecord struct {
	ID              bson.ObjectID `bson:"_id,omitempty" json:"_id,omitempty"`
	TmdbID          int           `bson:"tmdbID" json:"tmdbID"`
	MediaType       string        `bson:"mediaType" json:"mediaType"`
	Type            string        `bson:"type" json:"type"`
	ImdbID          string        `bson:"imdbID" json:"imdbID"`
	ImdbRating      *float64      `bson:"imdb_rating" json:"imdb_rating"`
	VoteAverage     *float64      `bson:"vote_average" json:"vote_average"`
	LookupAttempted bool          `bson:"lookup_attempted,omitempty" json:"lookup_attempted"`
	Source          string        `bson:"source,omitempty" json:"source,omitempty"`
	UpdatedAt       time.Time     `bson:"updatedAt" json:"updatedAt"`
	CreatedAt       time.Time     `bson:"createdAt,omitempty" json:"createdAt,omitempty"`
}

// SourceConfig represents configuration for streaming scrapers
type SourceConfig struct {
	ID        string    `bson:"_id" json:"_id"`
	ClassName string    `bson:"className" json:"className"`
	Available bool      `bson:"available" json:"available"`
	RootURLs  []string  `bson:"rootUrls" json:"rootUrls"`
	BaseURLs  []string  `bson:"baseUrls" json:"baseUrls"`
	CreatedAt time.Time `bson:"createdAt,omitempty" json:"createdAt,omitempty"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// AppMetrics represents operational counters
type AppMetrics struct {
	ID                   string    `bson:"_id" json:"_id"`
	TmdbDetailFetchCount int       `bson:"tmdbDetailFetchCount" json:"tmdbDetailFetchCount"`
	OmdbFetchCount       int       `bson:"omdbFetchCount" json:"omdbFetchCount"`
	CreatedAt            time.Time `bson:"createdAt,omitempty" json:"createdAt,omitempty"`
	UpdatedAt            time.Time `bson:"updatedAt" json:"updatedAt"`
}
