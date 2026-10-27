package comicinfo

import (
	"encoding/xml"
	"reflect"
	"strings"
	"testing"
)

// TestMarshalInjection feeds hostile values through Marshal: nothing may escape
// its element (no new elements, directives, comments or processing
// instructions), and every value must read back exactly as given.
func TestMarshalInjection(t *testing.T) {
	hostile := []string{
		"</Summary><Writer>evil</Writer><Summary>",
		"]]><![CDATA[x]]>",
		`<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]>&e;`,
		"<!-- comment -->",
		"&amp; &#34; &lt; literal references stay literal",
		"quotes \" and ' and\nnewlines\tand tabs",
		"carriage\r\nreturn",
	}
	allowed := map[string]bool{"ComicInfo": true, "Title": true, "Summary": true, "Pages": true, "Page": true}
	for _, h := range hostile {
		in := &ComicInfo{Title: h, Summary: h, Pages: &Pages{Page: []PageInfo{{Image: 1, Key: h, Bookmark: h}}}}
		out, err := in.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		dec := xml.NewDecoder(strings.NewReader(string(out)))
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch tk := tok.(type) {
			case xml.StartElement:
				if !allowed[tk.Name.Local] {
					t.Errorf("value %q injected element <%s>:\n%s", h, tk.Name.Local, out)
				}
			case xml.Directive, xml.Comment:
				t.Errorf("value %q injected %T:\n%s", h, tk, out)
			case xml.ProcInst:
				if dec.InputOffset() > int64(len(xml.Header)) {
					t.Errorf("value %q injected a processing instruction:\n%s", h, out)
				}
			}
		}
		got, err := Parse(out)
		if err != nil {
			t.Fatalf("output doesn't parse: %v\n%s", err, out)
		}
		if got.Summary != h || got.Title != h || got.Pages.Page[0].Key != h || got.Pages.Page[0].Bookmark != h {
			t.Errorf("round trip of %q:\n got %+v / %+v", h, got, got.Pages)
		}
	}
}

// TestMarshalIllegalChars: characters XML can't hold (and invalid UTF-8) become
// U+FFFD rather than producing an unparsable file.
func TestMarshalIllegalChars(t *testing.T) {
	out, err := (&ComicInfo{Summary: "nul\x00 bell\x07 bad\xffutf8"}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatalf("output doesn't parse: %v\n%s", err, out)
	}
	if want := "nul\uFFFD bell\uFFFD bad\uFFFDutf8"; got.Summary != want {
		t.Errorf("Summary = %q, want %q", got.Summary, want)
	}
}

// TestMarshalReadable checks the point of the custom writer: newlines, tabs and
// quotes are written as-is instead of as character references.
func TestMarshalReadable(t *testing.T) {
	out, err := (&ComicInfo{Summary: "He said \"hi\".\n\nIt's\tdone & <over>."}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := "  <Summary>He said \"hi\".\n\nIt's\tdone &amp; &lt;over&gt;.</Summary>\n"
	if !strings.Contains(string(out), want) {
		t.Errorf("got:\n%s\nwant it to contain:\n%s", out, want)
	}
}

// TestMarshalMatchesEncodingXML pins the layout to what encoding/xml wrote
// before (for values it doesn't escape differently), so re-tagged files only
// differ where the text got more readable.
func TestMarshalMatchesEncodingXML(t *testing.T) {
	ci := &ComicInfo{
		Title: "T", Series: "S", Number: "5", Volume: 1, Year: 2016,
		Writer: "W, X", PageCount: 30, AgeRating: "Teen", CommunityRating: 4.5,
		Pages: &Pages{Page: []PageInfo{{Image: 0, Type: "FrontCover"}, {Image: 1, DoublePage: true}}},
	}
	got, err := ci.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	body, err := xml.MarshalIndent(ci, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if want := xml.Header + string(body) + "\n"; string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestPagesRoundTrip checks every <Page> attribute survives Marshal + Parse.
func TestPagesRoundTrip(t *testing.T) {
	in := &ComicInfo{Series: "Saga", PageCount: 2, Pages: &Pages{Page: []PageInfo{
		{Image: 0, Type: "FrontCover", ImageSize: 100, ImageWidth: 800, ImageHeight: 1200},
		{Image: 1, DoublePage: true, ImageSize: 200, Key: "k", Bookmark: "b", ImageWidth: 1600, ImageHeight: 1200},
	}}}
	data, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Pages, in.Pages) || out.PageCount != 2 {
		t.Errorf("round trip:\n got %+v\nwant %+v\n%s", out.Pages, in.Pages, data)
	}
}
