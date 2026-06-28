// Package gofpdi extracts pages from an existing PDF and re-emits them as
// reusable Form XObjects for embedding into a generated PDF.
//
// It is built on the read-only pdfdisassembler parser, which understands
// classical cross-reference tables and cross-reference streams, object
// streams (compressed objects), and the full set of standard stream filters.
// Source PDFs that the previous hand-rolled reader rejected (xref streams,
// non-Flate filters) therefore import out of the box.
//
// Page numbers in the public API are 1-based: page 1 is the first page. This
// matches the historical gofpdi contract and the boxes-and-glue image loader.
// Internally they are converted to pdfdisassembler's 0-based page index.
package gofpdi

import (
	"fmt"
	"io"

	src "github.com/speedata/pdfdisassembler"
)

// Importer extracts pages from a single source PDF (set via SetSourceStream)
// and stages them as Form XObjects. Typical lifecycle:
//
//  1. NewImporter, then SetObjIDGetter to supply the host PDF's object-number
//     allocator.
//  2. SetSourceStream with the source PDF.
//  3. ImportPage for each page to embed.
//  4. PutFormXobjects to serialize the XObjects and every object they
//     reference, then GetImportedObjects to retrieve the bytes.
//
// An Importer is bound to one source PDF and is not safe for concurrent use.
type Importer struct {
	reader *src.Reader
	writer *PdfWriter
	// importedPages maps a 1-based page number to the template index returned
	// by ImportPage, so repeated imports of the same page are deduplicated.
	importedPages map[int]int
}

// NewImporter returns a ready-to-use Importer.
func NewImporter() *Importer {
	return &Importer{
		writer:        NewPdfWriter(),
		importedPages: make(map[int]int),
	}
}

// SetObjIDGetter installs the allocator the writer calls whenever it needs a
// fresh output object number. The first number it hands out is used for the
// Form XObject itself; subsequent numbers are used for the page's referenced
// objects (fonts, images, graphics states, …).
func (imp *Importer) SetObjIDGetter(f func() int) {
	imp.writer.NextObjectID = f
}

// SetNextObjectID seeds the writer's internal object counter such that the
// next reserved number becomes objID. It has no effect when SetObjIDGetter
// installed an external allocator.
func (imp *Importer) SetNextObjectID(objID int) {
	imp.writer.SetNextObjectID(objID)
}

// SetTemplateDictEntry stages an extra Form XObject dictionary entry written
// into the XObject header at PutFormXobjects time. tplN is the template index
// returned by ImportPage; key is the entry name without the leading slash;
// value is the raw PDF token (e.g. an integer "7" or a reference "12 0 R").
//
// PDF 1.7 §14.7.4.4 / PDF/UA-1 §7.1 Note 1 attach a Form XObject to a
// structure element through a single /StructParent entry; pass
// key="StructParent", value="<int>" for that. Other dictionary additions
// (/Metadata, /OC, /Group) ride on the same hook.
func (imp *Importer) SetTemplateDictEntry(tplN int, key, value string) {
	if imp.writer.ExtraTemplateDict == nil {
		imp.writer.ExtraTemplateDict = make(map[int]map[string]string)
	}
	if imp.writer.ExtraTemplateDict[tplN] == nil {
		imp.writer.ExtraTemplateDict[tplN] = make(map[string]string)
	}
	imp.writer.ExtraTemplateDict[tplN][key] = value
}

// SetSourceStream sets the source PDF. The reader keeps a reference to rs, so
// rs must stay readable until import is complete.
func (imp *Importer) SetSourceStream(rs io.ReadSeeker) error {
	r, err := src.Open(rs)
	if err != nil {
		return fmt.Errorf("gofpdi: open source PDF: %w", err)
	}
	// Importing copies the source's raw, still filter-encoded stream bytes
	// straight into the output. For an encrypted source those bytes are also
	// still encrypted, so copying them into an unencrypted output PDF would
	// produce garbage. Reading metadata from encrypted PDFs works, but page
	// import does not yet, so reject it loudly rather than emit a broken file.
	if t := r.Trailer(); t != nil && t.Has("Encrypt") {
		return fmt.Errorf("gofpdi: importing pages from encrypted PDFs is not supported")
	}
	imp.reader = r
	imp.writer.reader = r
	return nil
}

// GetNumPages returns the number of pages in the source PDF.
func (imp *Importer) GetNumPages() (int, error) {
	if imp.reader == nil {
		return 0, fmt.Errorf("gofpdi: no source stream set")
	}
	return imp.reader.PageCount()
}

// GetPageSizes returns the boundary boxes of every page, keyed by 1-based page
// number, then by box name ("/MediaBox", "/CropBox", …). Each box is a map
// with llx/lly/urx/ury plus the convenience fields x/y/w/h. Only boxes that
// are actually present (after /Parent inheritance) are included; callers apply
// their own spec defaults (e.g. a missing /CropBox falling back to /MediaBox).
func (imp *Importer) GetPageSizes() (map[int]map[string]map[string]float64, error) {
	if imp.reader == nil {
		return nil, fmt.Errorf("gofpdi: no source stream set")
	}
	pages, err := imp.reader.Pages()
	if err != nil {
		return nil, err
	}
	out := make(map[int]map[string]map[string]float64, len(pages))
	for _, pg := range pages {
		boxes := make(map[string]map[string]float64)
		for name, rect := range pg.Boxes() {
			boxes["/"+string(name)] = rectToMap(rect)
		}
		out[pg.Index()+1] = boxes // 0-based index -> 1-based page number
	}
	return out, nil
}

// ImportPage stages the 1-based page pageno using the requested box (e.g.
// "/MediaBox"; empty defaults to /MediaBox) and returns the template index to
// pass to SetTemplateDictEntry. Importing the same page twice returns the
// previously assigned index without re-staging.
func (imp *Importer) ImportPage(pageno int, box string) (int, error) {
	if imp.reader == nil {
		return 0, fmt.Errorf("gofpdi: no source stream set")
	}
	if tplN, ok := imp.importedPages[pageno]; ok {
		return tplN, nil
	}
	page, err := imp.reader.Page(pageno - 1) // 1-based -> 0-based
	if err != nil {
		return 0, err
	}
	tplN, err := imp.writer.stageTemplate(page, box)
	if err != nil {
		return 0, err
	}
	imp.importedPages[pageno] = tplN
	return tplN, nil
}

// PutFormXobjects serializes one Form XObject per imported page plus every
// object reachable from their resources. It returns a map from the XObject
// template name (e.g. "/GOFPDITPL0") to its assigned output object number.
// Retrieve the serialized object bodies with GetImportedObjects.
func (imp *Importer) PutFormXobjects() (map[string]int, error) {
	if imp.reader == nil {
		return nil, fmt.Errorf("gofpdi: no source stream set")
	}
	return imp.writer.PutFormXobjects()
}

// GetImportedObjects returns the serialized body of every object produced so
// far, keyed by output object number. The bodies carry no "N 0 obj"/"endobj"
// wrapper; the host writer adds that when assembling the file.
func (imp *Importer) GetImportedObjects() map[int][]byte {
	return imp.writer.writtenObjs
}
