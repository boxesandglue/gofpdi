package gofpdi

import (
	"fmt"
	"io"
)

// The Importer class to be used by a pdf generation library
type Importer struct {
	sourceFile    string
	readers       map[string]*PdfReader
	writers       map[string]*PdfWriter
	tplMap        map[int]*TplInfo
	tplN          int
	writer        *PdfWriter
	importedPages map[string]int
}

// TplInfo has information about a template
type TplInfo struct {
	SourceFile string
	Writer     *PdfWriter
	TemplateID int
}

func (imp *Importer) getReader() *PdfReader {
	return imp.getReaderForFile(imp.sourceFile)
}

func (imp *Importer) getWriter() *PdfWriter {
	return imp.getWriterForFile(imp.sourceFile)
}

func (imp *Importer) getReaderForFile(file string) *PdfReader {
	if _, ok := imp.readers[file]; ok {
		return imp.readers[file]
	}
	return nil
}

func (imp *Importer) getWriterForFile(file string) *PdfWriter {
	if _, ok := imp.writers[file]; ok {
		return imp.writers[file]
	}

	return nil
}

// NewImporter returns a PDF importer
func NewImporter() *Importer {
	importer := &Importer{}
	importer.init()

	return importer
}

func (imp *Importer) init() {
	imp.readers = make(map[string]*PdfReader, 0)
	imp.writers = make(map[string]*PdfWriter, 0)
	imp.tplMap = make(map[int]*TplInfo, 0)
	imp.writer, _ = NewPdfWriter("")
	imp.importedPages = make(map[string]int, 0)
}

// SetSourceFile sets the importer source by providing the full path to a file.
func (imp *Importer) SetSourceFile(f string) {
	imp.sourceFile = f

	// If reader hasn't been instantiated, do that now
	if _, ok := imp.readers[imp.sourceFile]; !ok {
		reader, err := NewPdfReader(imp.sourceFile)
		if err != nil {
			panic(err)
		}
		imp.readers[imp.sourceFile] = reader
	}

	// If writer hasn't been instantiated, do that now
	if _, ok := imp.writers[imp.sourceFile]; !ok {
		writer, err := NewPdfWriter("")
		if err != nil {
			panic(err)
		}

		// Make the next writer start template numbers at this.tplN
		writer.SetTplIDOffset(imp.tplN)
		imp.writers[imp.sourceFile] = writer
	}
}

// SetSourceStream sets the importer source by providing a io.ReadSeeker
func (imp *Importer) SetSourceStream(rs *io.ReadSeeker) {
	imp.sourceFile = fmt.Sprintf("%v", rs)

	if _, ok := imp.readers[imp.sourceFile]; !ok {
		reader, err := NewPdfReaderFromStream(*rs)
		if err != nil {
			panic(err)
		}
		imp.readers[imp.sourceFile] = reader
	}

	// If writer hasn't been instantiated, do that now
	if _, ok := imp.writers[imp.sourceFile]; !ok {
		writer, err := NewPdfWriter("")
		if err != nil {
			panic(err)
		}

		// Make the next writer start template numbers at this.tplN
		writer.SetTplIDOffset(imp.tplN)
		imp.writers[imp.sourceFile] = writer
	}
}

// GetNumPages returns the number of pages in the PDF document
func (imp *Importer) GetNumPages() int {
	result, err := imp.getReader().getNumPages()

	if err != nil {
		panic(err)
	}

	return result
}

// GetPageSizes returns the page sizes for all pages
func (imp *Importer) GetPageSizes() map[int]map[string]map[string]float64 {
	result, err := imp.getReader().getAllPageBoxes(1.0)

	if err != nil {
		panic(err)
	}

	return result
}

// ImportPage imports a page and returns the template number
func (imp *Importer) ImportPage(pageno int, box string) int {
	// If page has already been imported, return existing tplN
	pageNameNumber := fmt.Sprintf("%s-%04d", imp.sourceFile, pageno)
	if _, ok := imp.importedPages[pageNameNumber]; ok {
		return imp.importedPages[pageNameNumber]
	}

	res, err := imp.getWriter().ImportPage(imp.getReader(), pageno, box)
	if err != nil {
		panic(err)
	}

	// Get current template id
	tplN := imp.tplN

	// Set tpl info
	imp.tplMap[tplN] = &TplInfo{SourceFile: imp.sourceFile, TemplateID: res, Writer: imp.getWriter()}

	// Increment template id
	imp.tplN++

	// Cache imported page tplN
	imp.importedPages[pageNameNumber] = tplN

	return tplN
}

// SetNextObjectID sets the start object number the generated PDF code has.
func (imp *Importer) SetNextObjectID(objID int) {
	imp.getWriter().SetNextObjectID(objID)
}

// PutFormXobjects puts form xobjects and get back a map of template names (e.g.
// /GOFPDITPL1) and their object ids (int)
func (imp *Importer) PutFormXobjects() map[string]int {
	res := make(map[string]int, 0)
	tplNamesIds, err := imp.getWriter().PutFormXobjects(imp.getReader())
	if err != nil {
		panic(err)
	}
	for tplName, pdfObjID := range tplNamesIds {
		res[tplName] = pdfObjID.id
	}
	return res
}

// PutFormXobjectsUnordered puts form xobjects and get back a map of template
// names (e.g. /GOFPDITPL1) and their object ids (sha1 hash)
func (imp *Importer) PutFormXobjectsUnordered() map[string]string {
	imp.getWriter().SetUseHash(true)
	res := make(map[string]string, 0)
	tplNamesIds, err := imp.getWriter().PutFormXobjects(imp.getReader())
	if err != nil {
		panic(err)
	}
	for tplName, pdfObjID := range tplNamesIds {
		res[tplName] = pdfObjID.hash
	}
	return res
}

// GetImportedObjects gets object ids (int) and their contents (string)
func (imp *Importer) GetImportedObjects() map[int]string {
	res := make(map[int]string, 0)
	pdfObjIDBytes := imp.getWriter().GetImportedObjects()
	for pdfObjID, bytes := range pdfObjIDBytes {
		res[pdfObjID.id] = string(bytes)
	}
	return res
}

// GetImportedObjectsUnordered gets object ids (sha1 hash) and their contents
// ([]byte) The contents may have references to other object hashes which will
// need to be replaced by the pdf generator library The positions of the hashes
// (sha1 - 40 characters) can be obtained by calling GetImportedObjHashPos()
func (imp *Importer) GetImportedObjectsUnordered() map[string][]byte {
	res := make(map[string][]byte, 0)
	pdfObjIDBytes := imp.getWriter().GetImportedObjects()
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
	pdfObjIDPosHash := imp.getWriter().GetImportedObjHashPos()
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
