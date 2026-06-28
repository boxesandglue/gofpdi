package gofpdi

import (
	"bytes"
	"os"
	"testing"

	src "github.com/speedata/pdfdisassembler"
)

// importToBuffer runs a full import of one page and assembles a standalone PDF
// from the writer output so the result can be parsed back. It is a deliberate
// round-trip: write, then read with pdfdisassembler and assert structure.
func importToBuffer(t *testing.T, filename string, page int, box string) []byte {
	t.Helper()
	r, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	imp := NewImporter()
	// Number objects sequentially starting at 1; the host normally supplies an
	// allocator, but the internal counter is exercised here.
	imp.SetNextObjectID(1)
	if err := imp.SetSourceStream(r); err != nil {
		t.Fatal(err)
	}
	if _, err := imp.ImportPage(page, box); err != nil {
		t.Fatal(err)
	}
	names, err := imp.PutFormXobjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 template name, got %d", len(names))
	}

	objs := imp.GetImportedObjects()
	if len(objs) == 0 {
		t.Fatal("no imported objects")
	}

	// Assemble a minimal but valid PDF: the imported objects plus a catalog,
	// a page tree, and a single page that draws the imported XObject.
	xobjN := names["/GOFPDITPL0"]
	maxN := 0
	for n := range objs {
		if n > maxN {
			maxN = n
		}
	}
	catalogN, pagesN, pageN, contentN := maxN+1, maxN+2, maxN+3, maxN+4

	var buf bytes.Buffer
	offsets := make(map[int]int)
	buf.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")

	writeObj := func(n int, body []byte) {
		offsets[n] = buf.Len()
		buf.WriteString(itoa(n) + " 0 obj\n")
		buf.Write(body)
		buf.WriteString("\nendobj\n")
	}
	for n := 1; n <= maxN; n++ {
		if body, ok := objs[n]; ok {
			writeObj(n, body)
		}
	}
	writeObj(catalogN, []byte("<</Type /Catalog /Pages "+itoa(pagesN)+" 0 R>>"))
	writeObj(pagesN, []byte("<</Type /Pages /Kids ["+itoa(pageN)+" 0 R] /Count 1>>"))
	writeObj(pageN, []byte("<</Type /Page /Parent "+itoa(pagesN)+" 0 R /MediaBox [0 0 595 842]"+
		" /Resources <</XObject <</Imp "+itoa(xobjN)+" 0 R>>>> /Contents "+itoa(contentN)+" 0 R>>"))
	stream := "q 1 0 0 1 0 0 cm /Imp Do Q"
	writeObj(contentN, []byte("<</Length "+itoa(len(stream))+">>\nstream\n"+stream+"\nendstream"))

	xrefPos := buf.Len()
	total := contentN + 1
	buf.WriteString("xref\n0 " + itoa(total) + "\n")
	buf.WriteString("0000000000 65535 f \n")
	for n := 1; n < total; n++ {
		buf.WriteString(pad10(offsets[n]) + " 00000 n \n")
	}
	buf.WriteString("trailer\n<</Size " + itoa(total) + " /Root " + itoa(catalogN) + " 0 R>>\n")
	buf.WriteString("startxref\n" + itoa(xrefPos) + "\n%%EOF\n")
	return buf.Bytes()
}

func TestImportSimple(t *testing.T) {
	pdf := importToBuffer(t, "testdata/cow.pdf", 1, "/MediaBox")

	// Round-trip: the assembled PDF must parse, expose exactly one page, and
	// that page must carry a Form XObject resource named /Imp.
	rd, err := src.Open(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("re-parse imported PDF: %v", err)
	}
	defer rd.Close()

	n, err := rd.PageCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("PageCount = %d, want 1", n)
	}
	page, err := rd.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := page.Resources()
	if !ok {
		t.Fatal("page has no resources")
	}
	xobj, ok := res.Dict("XObject")
	if !ok {
		t.Fatal("resources have no /XObject")
	}
	form, ok := xobj.Stream("Imp")
	if !ok {
		t.Fatal("/Imp is not a stream")
	}
	if sub, _ := form.Dict.Name("Subtype"); sub != "Form" {
		t.Errorf("XObject /Subtype = %q, want Form", sub)
	}
	if _, ok := form.Dict.Array("BBox"); !ok {
		t.Error("Form XObject has no /BBox")
	}
}

func TestImportPageNumbersAreOneBased(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	imp := NewImporter()
	imp.SetNextObjectID(1)
	if err := imp.SetSourceStream(r); err != nil {
		t.Fatal(err)
	}
	// Page 0 is out of range (pages start at 1).
	if _, err := imp.ImportPage(0, "/MediaBox"); err == nil {
		t.Error("ImportPage(0) should fail: page numbering is 1-based")
	}
}

func TestGetPageSizes(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	imp := NewImporter()
	if err := imp.SetSourceStream(r); err != nil {
		t.Fatal(err)
	}
	sizes, err := imp.GetPageSizes()
	if err != nil {
		t.Fatal(err)
	}
	pg, ok := sizes[1] // 1-based
	if !ok {
		t.Fatal("no size for page 1")
	}
	mb, ok := pg["/MediaBox"]
	if !ok {
		t.Fatal("page 1 has no /MediaBox")
	}
	if mb["w"] <= 0 || mb["h"] <= 0 {
		t.Errorf("degenerate MediaBox: w=%v h=%v", mb["w"], mb["h"])
	}
}

// itoa and pad10 keep the assembled-PDF writer free of fmt/strconv churn.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func pad10(n int) string {
	s := itoa(n)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}
