// Package comicinfo models the ComicInfo.xml metadata file (v2.1 schema) and its
// XML (de)serialization. It has no dependency on archive handling so it can be
// reused by readers, writers, and providers alike.
package comicinfo

import "encoding/xml"

// ComicInfo models ComicInfo.xml following the v2.1 schema. Fields are declared
// in XSD document order so marshalled output validates against the sequence.
// Every field uses omitempty: an unset value is written as an absent element
// rather than an empty tag. `tag -m` populates a small subset by hand; `tag`
// fills as many fields as the provider exposes.
//
// Schema: https://github.com/anansi-project/comicinfo (drafts/v2.1)
type ComicInfo struct {
	XMLName             xml.Name `xml:"ComicInfo"`
	Title               string   `xml:"Title,omitempty"`
	Series              string   `xml:"Series,omitempty"`
	Number              string   `xml:"Number,omitempty"` // xs:string: preserves "1.5"
	Count               int      `xml:"Count,omitempty"`
	Volume              int      `xml:"Volume,omitempty"`
	AlternateSeries     string   `xml:"AlternateSeries,omitempty"`
	AlternateNumber     string   `xml:"AlternateNumber,omitempty"`
	AlternateCount      int      `xml:"AlternateCount,omitempty"`
	Summary             string   `xml:"Summary,omitempty"`
	Notes               string   `xml:"Notes,omitempty"`
	Year                int      `xml:"Year,omitempty"`
	Month               int      `xml:"Month,omitempty"`
	Day                 int      `xml:"Day,omitempty"`
	Writer              string   `xml:"Writer,omitempty"`
	Penciller           string   `xml:"Penciller,omitempty"`
	Inker               string   `xml:"Inker,omitempty"`
	Colorist            string   `xml:"Colorist,omitempty"`
	Letterer            string   `xml:"Letterer,omitempty"`
	CoverArtist         string   `xml:"CoverArtist,omitempty"`
	Editor              string   `xml:"Editor,omitempty"`
	Translator          string   `xml:"Translator,omitempty"`
	Publisher           string   `xml:"Publisher,omitempty"`
	Imprint             string   `xml:"Imprint,omitempty"`
	Genre               string   `xml:"Genre,omitempty"`
	Tags                string   `xml:"Tags,omitempty"`
	Web                 string   `xml:"Web,omitempty"`
	PageCount           int      `xml:"PageCount,omitempty"` // computed from images
	LanguageISO         string   `xml:"LanguageISO,omitempty"`
	Format              string   `xml:"Format,omitempty"`
	BlackAndWhite       string   `xml:"BlackAndWhite,omitempty"` // YesNo enum
	Manga               string   `xml:"Manga,omitempty"`         // Manga enum
	Characters          string   `xml:"Characters,omitempty"`
	Teams               string   `xml:"Teams,omitempty"`
	Locations           string   `xml:"Locations,omitempty"`
	ScanInformation     string   `xml:"ScanInformation,omitempty"`
	StoryArc            string   `xml:"StoryArc,omitempty"`
	StoryArcNumber      string   `xml:"StoryArcNumber,omitempty"`
	SeriesGroup         string   `xml:"SeriesGroup,omitempty"`
	AgeRating           string   `xml:"AgeRating,omitempty"` // AgeRating enum
	Pages               *Pages   `xml:"Pages,omitempty"`
	CommunityRating     float64  `xml:"CommunityRating,omitempty"` // 0-5
	MainCharacterOrTeam string   `xml:"MainCharacterOrTeam,omitempty"`
	Review              string   `xml:"Review,omitempty"`
	GTIN                string   `xml:"GTIN,omitempty"`
}

// Pages wraps the <Page> list so an unset <Pages> is omitted entirely
// (a nil pointer) rather than emitted as an empty element.
type Pages struct {
	Page []PageInfo `xml:"Page"`
}

// PageInfo is one <Page> entry in <Pages> (v2.1 ComicPageType attributes).
// Not populated by tag; reserved for future per-page metadata.
type PageInfo struct {
	Image       int    `xml:"Image,attr"`
	Type        string `xml:"Type,attr,omitempty"`
	DoublePage  bool   `xml:"DoublePage,attr,omitempty"`
	ImageSize   int64  `xml:"ImageSize,attr,omitempty"`
	Key         string `xml:"Key,attr,omitempty"`
	Bookmark    string `xml:"Bookmark,attr,omitempty"`
	ImageWidth  int    `xml:"ImageWidth,attr,omitempty"`
	ImageHeight int    `xml:"ImageHeight,attr,omitempty"`
}

// AgeRatings is the ComicInfo AgeRating enumeration (schema v2.0), in the order
// cb presents them as a numbered menu. Index 0 ("Unknown") is the default.
var AgeRatings = []string{
	"Unknown",
	"Adults Only 18+",
	"Early Childhood",
	"Everyone",
	"Everyone 10+",
	"G",
	"Kids to Adults",
	"M",
	"MA15+",
	"Mature 17+",
	"PG",
	"R18+",
	"Rating Pending",
	"Teen",
	"X18+",
}

// Parse unmarshals ComicInfo.xml bytes.
func Parse(data []byte) (*ComicInfo, error) {
	var ci ComicInfo
	if err := xml.Unmarshal(data, &ci); err != nil {
		return nil, err
	}
	return &ci, nil
}

// Marshal renders the ComicInfo as indented XML with a declaration header.
func (ci *ComicInfo) Marshal() ([]byte, error) {
	body, err := xml.MarshalIndent(ci, "", "  ")
	if err != nil {
		return nil, err
	}
	out := append([]byte(xml.Header), body...)
	return append(out, '\n'), nil
}
