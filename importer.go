package gofpdi

import (
	"io"

	"github.com/speedata/gofpdi/reader"
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
	SourceFile string
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

// SetSourceStream sets the importer source by providing a io.ReadSeeker
func (imp *Importer) SetSourceStream(rs *io.ReadSeeker) error {
	var err error
	if imp.reader, err = reader.NewPdfReaderFromStream(*rs); err != nil {
		return err
	}

	// Make the next writer start template numbers at this.tplN
	imp.writer.SetTplIDOffset(imp.tplN)
	return nil
}

// GetNumPages returns the number of pages in the PDF document
func (imp *Importer) GetNumPages() (int, error) {
	return imp.reader.GetNumPages()
}

// GetPageSizes returns the page sizes for all pages
func (imp *Importer) GetPageSizes() (map[int]map[string]map[string]float64, error) {
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
	imp.tplMap[tplN] = &TplInfo{SourceFile: "", TemplateID: res, Writer: imp.writer}

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

// PutFormXobjectsUnordered puts form xobjects and get back a map of template
// names (e.g. /GOFPDITPL1) and their object ids (sha1 hash)
func (imp *Importer) PutFormXobjectsUnordered() (map[string]string, error) {
	imp.writer.SetUseHash(true)
	res := make(map[string]string, 0)
	tplNamesIds, err := imp.writer.PutFormXobjects(imp.reader)
	if err != nil {
		return nil, err
	}
	for tplName, pdfObjID := range tplNamesIds {
		res[tplName] = pdfObjID.hash
	}
	return res, nil
}

// GetImportedObjects gets object ids (int) and their contents ([]byte)
func (imp *Importer) GetImportedObjects() map[int][]byte {
	res := make(map[int][]byte, 0)
	pdfObjIDBytes := imp.writer.GetImportedObjects()
	for pdfObjID, bytes := range pdfObjIDBytes {
		res[pdfObjID.id] = bytes
	}
	return res
}

// GetImportedObjectsUnordered gets object ids (sha1 hash) and their contents
// ([]byte) The contents may have references to other object hashes which will
// need to be replaced by the pdf generator library The positions of the hashes
// (sha1 - 40 characters) can be obtained by calling GetImportedObjHashPos()
func (imp *Importer) GetImportedObjectsUnordered() map[string][]byte {
	res := make(map[string][]byte, 0)
	pdfObjIDBytes := imp.writer.GetImportedObjects()
	for pdfObjID, bytes := range pdfObjIDBytes {
		res[pdfObjID.hash] = bytes
	}
	return res
}

// GetImportedObjHashPos gets the positions of the hashes (sha1 - 40 characters)
// within each object, to be replaced with actual objects ids by the pdf
// generator library
func (imp *Importer) GetImportedObjHashPos() map[string]map[int]string {
	res := make(map[string]map[int]string, 0)
	pdfObjIDPosHash := imp.writer.GetImportedObjHashPos()
	for pdfObjID, posHashMap := range pdfObjIDPosHash {
		res[pdfObjID.hash] = posHashMap
	}
	return res
}

// UseTemplate gets the template name (e.g. /GOFPDITPL1) and the 4 float64
// values necessary to draw the template a x,y for a given width and height For
// a given template id (returned from ImportPage),
func (imp *Importer) UseTemplate(tplid int, _x float64, _y float64, _w float64, _h float64) (string, float64, float64, float64, float64) {
	// Look up template id in importer tpl map
	tplInfo := imp.tplMap[tplid]
	return tplInfo.Writer.UseTemplate(tplInfo.TemplateID, _x, _y, _w, _h)
}
