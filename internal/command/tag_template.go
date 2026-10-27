package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/comicinfo"
	"codeberg.org/Nikode/cbztool/internal/filename"
)

// templateIssueFields are the per-issue fields a template lists under each
// issue; every other settable field (except Number, which comes from the key or
// filename) is listed once under "series". Year appears in both: a series-wide
// default that an issue can override.
var templateIssueFields = map[string]bool{
	"Title": true, "AlternateNumber": true, "Summary": true, "Notes": true,
	"Year": true, "Month": true, "Day": true, "Web": true,
	"Characters": true, "Teams": true, "Locations": true, "ScanInformation": true,
	"StoryArc": true, "StoryArcNumber": true, "CommunityRating": true,
	"MainCharacterOrTeam": true, "Review": true, "GTIN": true,
}

// templateFields splits the settable fields, in schema order, into the series
// and issue sections of a template.
func templateFields() (series, issue []string) {
	for _, f := range comicinfo.Fields() {
		if f == "Number" {
			continue
		}
		if !templateIssueFields[f] || f == "Year" {
			series = append(series, f)
		}
		if templateIssueFields[f] {
			issue = append(issue, f)
		}
	}
	return series, issue
}

// kv is one ordered JSON member; templates keep schema order, which a Go map
// would lose.
type kv struct{ k, v string }

// tmplIssue is one issue entry in a template.
type tmplIssue struct {
	key    string
	fields []kv
}

// blankTemplate is every series field plus a single issue, all empty.
func blankTemplate() ([]kv, []tmplIssue) {
	seriesF, issueF := templateFields()
	var series, issue []kv
	for _, f := range seriesF {
		series = append(series, kv{f, ""})
	}
	for _, f := range issueF {
		issue = append(issue, kv{f, ""})
	}
	return series, []tmplIssue{{"1", issue}}
}

// tmplSource is what a prefilled template knows about one archive.
type tmplSource struct {
	path   string
	parsed filename.ParsedName
	ci     *comicinfo.ComicInfo // existing ComicInfo (or empty), Series defaulted from the filename
}

// prefilledTemplate builds a template from existing archives: a series field
// whose value is the same in every archive goes under "series"; one that
// differs is left "" there and listed on each issue that has a value. Issue
// fields carry each archive's current value. Issues are keyed by number, or by
// filename when the number is missing or shared (variants).
func prefilledTemplate(srcs []tmplSource) ([]kv, []tmplIssue) {
	seriesF, issueF := templateFields()
	inIssue := map[string]bool{}
	for _, f := range issueF {
		inIssue[f] = true
	}

	var series []kv
	perIssue := map[string]bool{} // series fields that differ between archives
	for _, f := range seriesF {
		v := srcs[0].ci.Get(f)
		for _, s := range srcs[1:] {
			if s.ci.Get(f) != v {
				perIssue[f] = true
				v = ""
				break
			}
		}
		series = append(series, kv{f, v})
	}

	numCount := map[string]int{}
	for _, s := range srcs {
		numCount[canonNumber(s.parsed.Number)]++
	}

	var issues []tmplIssue
	for _, s := range srcs {
		key := canonNumber(s.parsed.Number)
		if key == "" || numCount[key] > 1 {
			key = filepath.Base(s.path)
		}
		var fields []kv
		for _, f := range comicinfo.Fields() {
			switch {
			case inIssue[f]:
				fields = append(fields, kv{f, s.ci.Get(f)})
			case perIssue[f] && s.ci.Get(f) != "":
				fields = append(fields, kv{f, s.ci.Get(f)})
			}
		}
		issues = append(issues, tmplIssue{key, fields})
	}
	return series, issues
}

// renderTemplate writes the template as indented JSON in field order.
func renderTemplate(series []kv, issues []tmplIssue) []byte {
	var b strings.Builder
	b.WriteString("{\n  \"series\": ")
	writeObject(&b, "  ", series)
	b.WriteString(",\n  \"issues\": {")
	for i, is := range issues {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    " + jsonString(is.key) + ": ")
		writeObject(&b, "    ", is.fields)
	}
	b.WriteString("\n  }\n}\n")
	return []byte(b.String())
}

func writeObject(b *strings.Builder, indent string, fields []kv) {
	b.WriteString("{")
	for i, f := range fields {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n" + indent + "  " + jsonString(f.k) + ": " + jsonString(f.v))
	}
	b.WriteString("\n" + indent + "}")
}

// jsonString quotes s as a JSON string without HTML-escaping (<, >, & stay
// readable in summaries).
func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(buf.String(), "\n")
}

// writeTemplate writes a -f template to out: blank when target is "", else
// prefilled from the .cbz file or directory target. An existing out is never
// overwritten without force, and a .cbz out is refused outright (a likely
// forgotten template path, e.g. `cbz tag -m -t comic.cbz`).
func writeTemplate(out, target string, force bool) error {
	if strings.EqualFold(filepath.Ext(out), ".cbz") {
		return fmt.Errorf("tag -m: -t %s: the template path must be a .json file, not a .cbz (usage: cbz tag -m -t meta.json [<path>])", out)
	}

	var series []kv
	var issues []tmplIssue
	if target == "" {
		series, issues = blankTemplate()
	} else {
		files, err := cbzFiles(target)
		if err != nil {
			return err
		}
		srcs := make([]tmplSource, 0, len(files))
		for _, f := range files {
			a, err := archive.Open(f)
			if err != nil {
				return err
			}
			ci, _, found, rerr := a.ReadComicInfo()
			a.Close()
			if rerr != nil {
				return fmt.Errorf("%s: %w", f, rerr)
			}
			if !found {
				ci = &comicinfo.ComicInfo{}
			}
			parsed := filename.Parse(f)
			if ci.Series == "" {
				ci.Series = parsed.Series
			}
			srcs = append(srcs, tmplSource{f, parsed, ci})
		}
		series, issues = prefilledTemplate(srcs)
	}

	data := renderTemplate(series, issues)
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		flags |= os.O_EXCL
	}
	fh, err := os.OpenFile(out, flags, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists (use --force to overwrite)", out)
		}
		return err
	}
	if _, err := fh.Write(data); err != nil {
		fh.Close()
		return err
	}
	if err := fh.Close(); err != nil {
		return err
	}
	fmt.Printf("wrote template %s (%d issue(s)); fill it in, then: cbz tag -m -f %s <path>\n", Sanitize(out), len(issues), Sanitize(out))
	return nil
}
