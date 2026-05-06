package gofpdi

import (
	"fmt"
	"io"

	"github.com/boxesandglue/gofpdi/reader"
)

// The Importer class to be used by a pdf generation library
type Importer struct {
	reader        *reader.PdfReader
	writer        *PdfWriter
	tplMap        map[int]*TplInfo
	tplN          int
	importedPages map[int]int
}

// TplInfo has information about a template
type TplInfo struct {
	Writer     *PdfWriter
	TemplateID int
}

// NewImporter returns a PDF importer
func NewImporter() *Importer {
	importer := &Importer{}
	importer.tplMap = make(map[int]*TplInfo, 0)
	importer.writer = NewPdfWriter()
	importer.importedPages = make(map[int]int, 0)

	return importer
}

// SetObjIDGetter sets a function that is called each time the PDF writer should
// generate a new object number
func (imp *Importer) SetObjIDGetter(f func() int) {
	imp.writer.NextObjectID = f
}

// SetTemplateDictEntry stages an extra Form-XObject dictionary entry that
// will be written into the XObject header at PutFormXobjects time. tplN is
// the template index returned by ImportPage; key is the entry name without
// the leading slash; value is the raw PDF token (e.g. an integer like "7"
// or an indirect reference like "12 0 R").
//
// PDF 1.7 §14.7.4.4 / PDF/UA-1 §7.1 Note 1 use a single /StructParent entry
// to attach the entire XObject content to a structure element via the
// document's ParentTree. Pass key="StructParent" and value="<int>" for that
// case. Other PDF features that require dictionary additions (e.g.
// /Metadata, /OC, /Group) can ride on the same hook.
func (imp *Importer) SetTemplateDictEntry(tplN int, key, value string) {
	if imp.writer.ExtraTemplateDict == nil {
		imp.writer.ExtraTemplateDict = make(map[int]map[string]string)
	}
	if imp.writer.ExtraTemplateDict[tplN] == nil {
		imp.writer.ExtraTemplateDict[tplN] = make(map[string]string)
	}
	imp.writer.ExtraTemplateDict[tplN][key] = value
}

// SetSourceStream sets the importer source by providing a io.ReadSeeker
func (imp *Importer) SetSourceStream(rs io.ReadSeeker) error {
	var err error
	if imp.reader, err = reader.NewPdfReaderFromStream(rs); err != nil {
		return err
	}

	// Make the next writer start template numbers at this.tplN
	imp.writer.SetTplIDOffset(imp.tplN)
	return nil
}

// GetNumPages returns the number of pages in the PDF document
func (imp *Importer) GetNumPages() (int, error) {
	if imp.reader == nil {
		return 0, fmt.Errorf("internal error: reader is nil")
	}
	return imp.reader.GetNumPages()
}

// GetPageSizes returns the page sizes for all pages
func (imp *Importer) GetPageSizes() (map[int]map[string]map[string]float64, error) {
	if imp.reader == nil {
		return nil, fmt.Errorf("internal error: reader is nil")
	}
	return imp.reader.GetAllPageBoxes(1.0)
}

// ImportPage imports a page and returns the template number
func (imp *Importer) ImportPage(pageno int, box string) (int, error) {
	// If page has already been imported, return existing tplN
	if _, ok := imp.importedPages[pageno]; ok {
		return imp.importedPages[pageno], nil
	}

	res, err := imp.writer.ImportPage(imp.reader, pageno, box)
	if err != nil {
		return 0, err
	}
	// Get current template id
	tplN := imp.tplN

	// Set tpl info
	imp.tplMap[tplN] = &TplInfo{TemplateID: res, Writer: imp.writer}

	// Increment template id
	imp.tplN++

	// Cache imported page tplN
	imp.importedPages[pageno] = tplN

	return tplN, nil
}

// SetNextObjectID sets the start object number the generated PDF code has.
func (imp *Importer) SetNextObjectID(objID int) {
	imp.writer.SetNextObjectID(objID)
}

// PutFormXobjects puts form xobjects and get back a map of template names (e.g.
// /GOFPDITPL1) and their object ids (int)
func (imp *Importer) PutFormXobjects() (map[string]int, error) {
	res := make(map[string]int, 0)
	tplNamesIds, err := imp.writer.PutFormXobjects(imp.reader)
	if err != nil {
		return nil, err
	}
	for tplName, pdfObjID := range tplNamesIds {
		res[tplName] = pdfObjID.id
	}
	return res, nil
}

// GetImportedObjects gets object ids (int) and their contents ([]byte)
func (imp *Importer) GetImportedObjects() map[int][]byte {
	res := make(map[int][]byte, 0)
	pdfObjIDBytes := imp.writer.GetImportedObjects()
	for pdfObjID, bytes := range pdfObjIDBytes {
		res[pdfObjID.ID] = bytes
	}
	return res
}
