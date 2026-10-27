package comicinfo

import (
	"strings"
	"unicode"
)

// Merge combines metadata for the same issue from two sources, where primary
// takes precedence. The result is a new ComicInfo; neither input is modified.
//
//   - Single-value fields: primary's value wins; secondary only fills a field
//     primary left empty.
//   - Date (Year/Month/Day) is taken as a unit from one source, so a result never
//     mixes one source's year with another's month.
//   - Genre, Tags, Characters, Teams, Locations and StoryArc are unioned:
//     primary's entries first, then any secondary entry not already present
//     (see listKey for what counts as the same entry). Primary's spelling is
//     kept.
//   - Creator fields (Writer, Penciller, …) are single values per role:
//     primary's list is kept whole and secondary only fills an empty role.
//     ComicVine credits a person's roles once per issue, so its cover artists
//     also show up as penciller/inker/colorist; unioning would mix them into
//     the interior credits.
//   - Web keeps both URLs, space-separated (ComicInfo v2.1 allows several).
func Merge(primary, secondary *ComicInfo) *ComicInfo {
	switch {
	case primary == nil && secondary == nil:
		return &ComicInfo{}
	case secondary == nil:
		out := *primary
		return &out
	case primary == nil:
		out := *secondary
		return &out
	}

	out := *secondary
	out.Overlay(primary)

	if primary.Year != 0 || primary.Month != 0 || primary.Day != 0 {
		out.Year, out.Month, out.Day = primary.Year, primary.Month, primary.Day
	}

	p, s, o := primary.unionFields(), secondary.unionFields(), out.unionFields()
	for i := range o {
		*o[i] = UnionCSV(*p[i], *s[i])
	}

	if primary.Web != "" && secondary.Web != "" && primary.Web != secondary.Web {
		out.Web = primary.Web + " " + secondary.Web
	}
	return &out
}

// IsUnionField reports whether the named field (exact spelling, as from
// CanonicalField) is one Merge unions across sources rather than taking from
// one of them.
func IsUnionField(name string) bool {
	switch name {
	case "Genre", "Tags", "Characters", "Teams", "Locations", "StoryArc":
		return true
	}
	return false
}

// unionFields are the list fields Merge unions across sources (keep in sync
// with IsUnionField) (creator fields
// are deliberately excluded; see Merge).
func (ci *ComicInfo) unionFields() []*string {
	return []*string{&ci.Genre, &ci.Tags, &ci.Characters, &ci.Teams, &ci.Locations, &ci.StoryArc}
}

// UnionCSV merges two comma-separated lists: every entry of a, then each entry
// of b whose listKey isn't already present. Empty tokens are dropped.
func UnionCSV(a, b string) string {
	var out []string
	seen := map[string]bool{}
	for _, list := range []string{a, b} {
		for tok := range strings.SplitSeq(list, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			k := listKey(tok)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, tok)
		}
	}
	return strings.Join(out, ", ")
}

// listKey is the identity used to de-duplicate list entries across sources:
// case-folded, a trailing parenthetical disambiguator dropped, and punctuation
// and spacing ignored. So "Medusa (Marvel)" matches "Medusa", and "Spider-Man"
// matches "Spider Man". A name that is nothing but a parenthetical keeps it.
func listKey(s string) string {
	base := s
	if i := strings.LastIndex(s, "("); i > 0 && strings.HasSuffix(strings.TrimSpace(s), ")") {
		base = s[:i]
	}
	k := alnumFold(base)
	if k == "" {
		k = alnumFold(s)
	}
	return k
}

func alnumFold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
