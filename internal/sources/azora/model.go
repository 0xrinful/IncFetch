package azora

import (
	"encoding/json"
	"strconv"
	"strings"
)

// flexFloat unmarshals chapter numbers that the Iken API may return as either
// a JSON number (12.5) or a JSON string ("12.5").
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*f = 0
		return nil
	}
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*f = 0
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*f = flexFloat(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*f = flexFloat(v)
	return nil
}

// searchResponse maps to GET /api/query
type searchResponse struct {
	Posts      []postItem `json:"posts"`
	TotalCount int        `json:"totalCount"`
}

type postItem struct {
	ID            int             `json:"id"`
	Slug          string          `json:"slug"`
	Title         string          `json:"postTitle"`
	FeaturedImage string          `json:"featuredImage"`
	IsNovel       bool            `json:"isNovel"`
	SeriesType    string          `json:"seriesType"`
	Genres        []genre         `json:"genres"`
	Chapters      []chapterInList `json:"chapters"`
}

func (p postItem) isNovelPost() bool {
	return p.IsNovel || strings.EqualFold(p.SeriesType, "novel")
}

// chapterInList maps to chapter entries embedded in /api/post and /api/chapters.
type chapterInList struct {
	ID               int       `json:"id"`
	Slug             string    `json:"slug"`
	Number           flexFloat `json:"number"`
	Title            string    `json:"title"`
	CreatedAt        string    `json:"createdAt"`
	IsLocked         bool      `json:"isLocked"`
	IsTimeLocked     bool      `json:"isTimeLocked"`
	Price            int       `json:"price"`
	ChapterPurchased bool      `json:"chapterPurchased"`
	IsAccessible     *bool     `json:"isAccessible"`
}

func (c chapterInList) locked() bool {
	if c.IsAccessible != nil && !*c.IsAccessible {
		return true
	}
	return c.IsLocked || c.IsTimeLocked || (!c.ChapterPurchased && c.Price != 0)
}

// postResponse maps to GET /api/post?postSlug=
type postResponse struct {
	TotalChapterCount *int       `json:"totalChapterCount"`
	Post              postDetail `json:"post"`
}

type postDetail struct {
	ID                int             `json:"id"`
	Slug              string          `json:"slug"`
	Title             string          `json:"postTitle"`
	Description       string          `json:"postContent"`
	AlternativeTitles string          `json:"alternativeTitles"`
	Author            string          `json:"author"`
	Artist            string          `json:"artist"`
	FeaturedImage     string          `json:"featuredImage"`
	IsNovel           bool            `json:"isNovel"`
	SeriesStatus      string          `json:"seriesStatus"`
	SeriesType        string          `json:"seriesType"`
	Genres            []genre         `json:"genres"`
	Chapters          []chapterInList `json:"chapters"`
}

func (p postDetail) isNovelPost() bool {
	return p.IsNovel || strings.EqualFold(p.SeriesType, "novel")
}

// chaptersResponse maps to GET /api/chapters?postId=
type chaptersResponse struct {
	Post struct {
		Chapters []chapterInList `json:"chapters"`
	} `json:"post"`
}

type genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// chapterResponse maps to GET /api/chapter?chapterId=
type chapterResponse struct {
	Chapter chapterDetail `json:"chapter"`
}

type chapterDetail struct {
	ID                  int         `json:"id"`
	IsShortLinkLocked   bool        `json:"isShortLinkLocked"`
	IsLockedByCoins     bool        `json:"isLockedByCoins"`
	IsPermanentlyLocked bool        `json:"isPermanentlyLocked"`
	Images              []pageImage `json:"images"`
}

type pageImage struct {
	Order *int   `json:"order"`
	URL   string `json:"url"`
}
