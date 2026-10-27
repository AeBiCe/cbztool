package comicinfo

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Field access by element name, shared by `cbz tag -m -s Field=Value` and the
// JSON metadata file so both accept exactly the same names and values.

// notSettable are struct fields that are never set by name: XMLName is
// structural, PageCount is always computed from the archive, and Pages is
// per-page data rather than a single value.
var notSettable = map[string]bool{"XMLName": true, "PageCount": true, "Pages": true}

// fieldIndex maps a lowercased element name to its struct field index.
var fieldIndex = func() map[string]int {
	t := reflect.TypeFor[ComicInfo]()
	idx := make(map[string]int, t.NumField())
	for i := range t.NumField() {
		if name := t.Field(i).Name; !notSettable[name] {
			idx[strings.ToLower(name)] = i
		}
	}
	return idx
}()

// Fields lists every settable element name in XSD document order.
func Fields() []string {
	t := reflect.TypeFor[ComicInfo]()
	var out []string
	for i := range t.NumField() {
		if name := t.Field(i).Name; !notSettable[name] {
			out = append(out, name)
		}
	}
	return out
}

// CanonicalField resolves a case-insensitive element name ("publisher") to its
// exact spelling ("Publisher"). The second return is false for unknown or
// non-settable names.
func CanonicalField(name string) (string, bool) {
	i, ok := fieldIndex[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return "", false
	}
	return reflect.TypeFor[ComicInfo]().Field(i).Name, true
}

// enums are the closed-value string fields, validated and canonicalized by Set.
var enums = map[string][]string{
	"AgeRating":     AgeRatings,
	"BlackAndWhite": {"Unknown", "No", "Yes"},
	"Manga":         {"Unknown", "No", "Yes", "YesAndRightToLeft"},
}

// Set parses value into the named field. An empty (or all-space) value is a
// no-op, so it never clears a field. Numbers are parsed from their string form,
// enum fields are matched case-insensitively, and comma-separated list fields
// are tidied (tokens trimmed, empties dropped).
func (ci *ComicInfo) Set(name, value string) error {
	canon, ok := CanonicalField(name)
	if !ok {
		return fmt.Errorf("unknown field %q", name)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	f := reflect.ValueOf(ci).Elem().FieldByName(canon)
	switch f.Kind() {
	case reflect.String:
		if allowed, ok := enums[canon]; ok {
			v, ok := matchFold(allowed, value)
			if !ok {
				return fmt.Errorf("%s: %q is not valid (valid: %s)", canon, value, strings.Join(allowed, ", "))
			}
			value = v
		}
		f.SetString(value)
		for _, p := range ci.ListFields() {
			if p == f.Addr().Interface().(*string) {
				*p = CleanCSV(value)
			}
		}
	case reflect.Int:
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("%s: %q is not a non-negative whole number", canon, value)
		}
		f.SetInt(int64(n))
	case reflect.Float64:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || n < 0 || n > 5 {
			return fmt.Errorf("%s: %q is not a number from 0 to 5", canon, value)
		}
		f.SetFloat(n)
	default:
		return fmt.Errorf("%s cannot be set by name", canon)
	}
	return nil
}

// Get renders the named field as a string: "" when unset (including numeric
// zero) or when name is unknown. It is the inverse of Set.
func (ci *ComicInfo) Get(name string) string {
	canon, ok := CanonicalField(name)
	if !ok {
		return ""
	}
	f := reflect.ValueOf(ci).Elem().FieldByName(canon)
	if f.IsZero() {
		return ""
	}
	switch f.Kind() {
	case reflect.String:
		return f.String()
	case reflect.Int:
		return strconv.FormatInt(f.Int(), 10)
	case reflect.Float64:
		return strconv.FormatFloat(f.Float(), 'f', -1, 64)
	}
	return ""
}

func matchFold(allowed []string, v string) (string, bool) {
	for _, a := range allowed {
		if strings.EqualFold(a, v) {
			return a, true
		}
	}
	return "", false
}

// ListFields returns pointers to the comma-separated ComicInfo fields (creators,
// Genre, Tags, and the character/team/location/arc lists), so callers can
// normalize or combine them without enumerating the schema themselves.
func (ci *ComicInfo) ListFields() []*string {
	return []*string{
		&ci.Writer, &ci.Penciller, &ci.Inker, &ci.Colorist, &ci.Letterer,
		&ci.CoverArtist, &ci.Editor, &ci.Translator, &ci.Genre, &ci.Tags,
		&ci.Characters, &ci.Teams, &ci.Locations, &ci.StoryArc,
	}
}

// CleanCSV trims each comma-separated token and drops empties, rejoining with
// ", " so the comma-delimited ComicInfo fields are stored consistently.
func CleanCSV(s string) string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, ", ")
}

// Overlay copies every set (non-zero) field of top onto ci, leaving ci's other
// fields untouched. An empty value in top never clears a field in ci, matching
// the "empty ⇒ omitted" convention.
func (ci *ComicInfo) Overlay(top *ComicInfo) {
	if top == nil {
		return
	}
	dst := reflect.ValueOf(ci).Elem()
	src := reflect.ValueOf(top).Elem()
	for i := range src.NumField() {
		if src.Type().Field(i).Name == "XMLName" {
			continue
		}
		if f := src.Field(i); !f.IsZero() {
			dst.Field(i).Set(f)
		}
	}
}
