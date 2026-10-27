// Package comicinfo models the ComicInfo.xml metadata file (v2.1 schema) and its
// XML (de)serialization. It has no dependency on archive handling so it can be
// reused by readers, writers, and providers alike.
package comicinfo

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

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
// cbz presents them as a numbered menu. Index 0 ("Unknown") is the default.
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
//
// Element text is escaped by escapeText rather than encoding/xml, which also
// writes newlines, tabs and quotes as character references (&#xA;, &#34;, …).
// Those are valid but make a Summary unreadable in the raw file, and XML text
// content only needs &, < and > escaped. Element names are the struct's own
// constants, so provider data can only ever appear as escaped text. <Pages>
// (attribute data) is still encoded by encoding/xml, where quotes must stay
// escaped.
func (ci *ComicInfo) Marshal() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString("<ComicInfo>\n")
	v := reflect.ValueOf(ci).Elem()
	t := v.Type()
	for i := range t.NumField() {
		f, fv := t.Field(i), v.Field(i)
		if f.Name == "XMLName" || fv.IsZero() {
			continue
		}
		if f.Name == "Pages" {
			pages, err := xml.MarshalIndent(ci.Pages, "  ", "  ")
			if err != nil {
				return nil, err
			}
			b.Write(pages) // MarshalIndent writes the "  " prefix itself
			b.WriteByte('\n')
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("xml"), ",")
		var text string
		switch fv.Kind() {
		case reflect.String:
			text = fv.String()
		case reflect.Int:
			text = strconv.FormatInt(fv.Int(), 10)
		case reflect.Float64:
			text = strconv.FormatFloat(fv.Float(), 'g', -1, 64)
		default:
			return nil, fmt.Errorf("comicinfo: cannot marshal field %s of kind %s", f.Name, fv.Kind())
		}
		b.WriteString("  <" + name + ">")
		escapeText(&b, text)
		b.WriteString("</" + name + ">\n")
	}
	b.WriteString("</ComicInfo>\n")
	return b.Bytes(), nil
}

// escapeText writes s as XML element text: &, < and > become entities (so no
// value can open or close an element, or form "]]>"), \r is kept as a
// reference (a parser would otherwise normalize it away), and anything that
// isn't a legal XML character — control characters, invalid UTF-8 — becomes
// U+FFFD, as encoding/xml does. Newlines, tabs and quotes are written as-is.
func escapeText(b *bytes.Buffer, s string) {
	for i := 0; i < len(s); {
		r, width := utf8.DecodeRuneInString(s[i:])
		i += width
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '\r':
			b.WriteString("&#xD;")
		case r == utf8.RuneError && width == 1, !isXMLChar(r):
			b.WriteRune('�')
		default:
			b.WriteRune(r)
		}
	}
}

// isXMLChar reports whether r is in the XML 1.0 Char production.
func isXMLChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}
