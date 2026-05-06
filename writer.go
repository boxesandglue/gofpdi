// Package gofpdi provides minimal PDF import and form-XObject writing utilities
// that are focused on extracting pages from an existing PDF (via reader.PdfReader)
// and re‑emitting them as reusable Form XObjects. The PdfWriter coordinates object
// numbering, reference rewriting, and serialized output of imported resources.
package gofpdi

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/boxesandglue/gofpdi/reader"
)

// PdfWriter serializes PDF objects that originate from a PdfReader while keeping
// a stable mapping between the source document's object references and the newly
// assigned object numbers in the output. It also builds Form XObjects from pages
// (via ImportPage/PutFormXobjects) and collects the bytes for each written object
// for later consumption by a higher‑level PDF writer.
//
// Lifecycle (typical):
//  1. Create a writer with NewPdfWriter().
//  2. Call ImportPage for each source page you want to expose as a template.
//  3. Call PutFormXobjects(reader) to materialize those templates as Form XObjects;
//     this returns a map of template names (e.g. /GOFPDITPL1) to their object IDs.
//  4. Retrieve all serialized objects via GetImportedObjects() and integrate them
//     into your outer PDF file structure.
//
// The writer does not directly stream to a file; instead it accumulates each
// indirect object into writtenObjs for deterministic assembly by callers.
//
// Concurrency: PdfWriter is not safe for concurrent use.
// Object numbering: either supply a NextObjectID callback (allocator) or let the
// writer increment a local counter. You can set an initial counter with
// SetNextObjectID.
//
// Zero values: use NewPdfWriter() to get a fully initialized instance.
//
// Note: Only a subset of PDF types is emitted; this is intentionally small and
// tailored to the reader package used here.
type PdfWriter struct {
	f         *os.File          // Optional backing file handle (not used by default).
	w         *bufio.Writer     // Optional buffered writer (not used by default).
	r         *reader.PdfReader // The active source reader for imports and hashing.
	tpls      []*PdfTemplate    // Staged page templates to be turned into Form XObjects.
	nextObjID int               // Last assigned object number (monotonic).
	result    map[int]string    // Optional scratch map; not used in the core API.

	// Import bookkeeping. The keys are source object numbers from the reader.
	objStack       map[int]*reader.PdfValue // Pending objects to resolve and write.
	assignedObjIDs map[int]*reader.PdfValue // Mapping from source IDs to assigned NewID.

	// Output buffers and fixups.
	writtenObjs   map[PdfObjectKey][]byte         // Serialized bytes per written object.
	writtenObjPos map[PdfObjectKey]map[int]string // Positions of placeholder refs -> source hashes.
	currentObj    *PdfObject                      // Buffer under construction.

	// Template name offset; allows concatenating multiple imports without clashes.
	tplIDOffset int

	// NextObjectID, if non‑nil, is called to obtain the next object number. If it
	// is nil, PdfWriter increments nextObjID internally.
	NextObjectID func() int

	// ExtraTemplateDict carries additional Form XObject dictionary entries
	// that should be written into the XObject header. The outer key is the
	// template index (matching imp.tplN at ImportPage time); the inner map
	// is key/value pairs added verbatim to the dictionary, e.g.
	// {"StructParent": "7"} produces "/StructParent 7" inside the XObject.
	// PDF/UA-1 §7.1 Note 1 uses /StructParent to attach a Form XObject to
	// the structure tree without an enclosing marked-content sequence on
	// the parent page; this map is the hook that lets callers do that.
	ExtraTemplateDict map[int]map[string]string
}

// PdfObjectKey is a stable, comparable key for maps of written objects. It pairs
// the assigned object ID with a per‑document hash so that objects from different
// source files do not collide when merged.
type PdfObjectKey struct {
	ID   int
	Hash string
}

// PdfObjectID identifies an object inside this writer by its numeric ID and the
// hash derived from the source file. It is returned to callers to reference newly
// created objects without exposing internal maps.
type PdfObjectID struct {
	id   int
	hash string
}

// PdfObject is an in‑memory representation of a single indirect object being
// constructed. The buffer holds the complete serialized form of the object body.
// The object header/trailer (e.g., "n 0 obj" / "endobj") are not included here
// because the integration layer decides how to assemble the final file.
type PdfObject struct {
	id     *PdfObjectID
	buffer *bytes.Buffer
}

// SetTplIDOffset sets an offset used to build template names returned by
// PutFormXobjects (e.g., with an offset of 10, the first name is /GOFPDITPL11).
// This is useful when multiple imports are concatenated and template names must
// remain unique.
func (pw *PdfWriter) SetTplIDOffset(n int) {
	pw.tplIDOffset = n
}

// SetNextObjectID sets the internal object counter such that the next reserved
// object number will become id. Passing 1 means the next object will be 1. This
// only affects the internal allocator and does not touch NextObjectID if set.
func (pw *PdfWriter) SetNextObjectID(id int) {
	pw.nextObjID = id - 1
}

// NewPdfWriter returns a fully initialized PdfWriter ready to import objects.
// The returned writer keeps all internal bookkeeping in memory and does not
// perform any I/O by itself.
func NewPdfWriter() *PdfWriter {
	pw := &PdfWriter{}
	pw.objStack = make(map[int]*reader.PdfValue, 0)
	pw.assignedObjIDs = make(map[int]*reader.PdfValue, 0)
	pw.tpls = make([]*PdfTemplate, 0)
	pw.writtenObjs = make(map[PdfObjectKey][]byte, 0)
	pw.writtenObjPos = make(map[PdfObjectKey]map[int]string, 0)
	pw.currentObj = new(PdfObject)

	return pw
}

// PdfTemplate holds the minimal set of information needed to materialize a
// page from the source PDF as a Form XObject in the output. Templates are
// created with ImportPage and finalized by PutFormXobjects.
//
// Fields:
//
//	ID:        Assigned object number of the resulting Form XObject (set during write).
//	Reader:    Back‑reference to the source PdfReader used for this template.
//	Resources: The page's resource dictionary, to be copied into the XObject.
//	Buffer:    The page's content stream (uncompressed text of operators).
//	Box:       The effective page box (e.g., MediaBox), with llx/lly/urx/ury and derived x/y/w/h.
//	X, Y:      Additional offset applied when composing the XObject's matrix.
//	W, H:      Width and height of the template after rotation normalization.
//	Rotation:  Normalized clockwise rotation in degrees (negative values counter‑rotate content).
//	N:         Object number assigned to the XObject once written.
//
// Callers typically do not construct PdfTemplate directly.
type PdfTemplate struct {
	ID        int
	Reader    *reader.PdfReader
	Resources *reader.PdfValue
	Buffer    string
	Box       map[string]float64
	X         float64
	Y         float64
	W         float64
	H         float64
	Rotation  int
	N         int
}

// GetImportedObjects returns a copy of the internal map of all serialized
// objects produced so far. The map keys are PdfObjectKey (ID+Hash), and the
// values are the raw bytes representing each object's body. Callers can inject
// these into their own cross‑reference and file assembly logic.
func (pw *PdfWriter) GetImportedObjects() map[PdfObjectKey][]byte {
	return pw.writtenObjs
}

// ClearImportedObjects drops all accumulated object bytes and resets the output
// collection. This does not affect numbering state or templates.
func (pw *PdfWriter) ClearImportedObjects() {
	pw.writtenObjs = make(map[PdfObjectKey][]byte, 0)
}

// intersectBox clamps a non‑MediaBox (Crop/Trim/Bleed/Art) to the page's
// MediaBox, as required by the PDF spec. The returned map includes llx/lly/urx/ury
// and convenience fields x/y/w/h.
func intersectBox(bx map[string]float64, mediabox map[string]float64) map[string]float64 {
	newbox := make(map[string]float64)
	for k, v := range bx {
		newbox[k] = v
	}

	if bx["lly"] < mediabox["lly"] {
		newbox["lly"] = mediabox["lly"]
	}
	if bx["llx"] < mediabox["llx"] {
		newbox["llx"] = mediabox["llx"]
	}
	if bx["ury"] > mediabox["ury"] {
		newbox["ury"] = mediabox["ury"]
	}
	if bx["urx"] > mediabox["urx"] {
		newbox["urx"] = mediabox["urx"]
	}
	newbox["x"] = newbox["llx"]
	newbox["y"] = newbox["lly"]
	newbox["w"] = newbox["urx"] - newbox["llx"]
	newbox["h"] = newbox["ury"] - newbox["lly"]
	return newbox
}

// GetPDFBoxDimensions returns the normalized box dimensions of a specific page.
// boxname must be one of "/MediaBox", "/CropBox", "/BleedBox", "/TrimBox",
// or "/ArtBox". For non‑MediaBox names, the result is clamped to the MediaBox
// per PDF rules. The map includes llx/lly/urx/ury as well as x/y/w/h.
func (pw *PdfWriter) GetPDFBoxDimensions(p int, boxname string) (map[string]float64, error) {
	numPages, err := pw.r.GetNumPages()
	if err != nil {
		return nil, err
	}
	if p > numPages {
		return nil, fmt.Errorf("cannot get the page number %d of the PDF, the PDF has only %d page(s)", p, numPages)
	}
	pb, err := pw.r.GetAllPageBoxes(1.0)
	if err != nil {
		return nil, err
	}
	bx := pb[p][boxname]
	if len(bx) == 0 {
		if boxname == "/CropBox" {
			return pb[p]["/MediaBox"], nil
		}
		switch boxname {
		case "/ArtBox", "/BleedBox", "/TrimBox":
			return pb[p]["/CropBox"], nil
		default:
			// unknown box dimensions
			return nil, fmt.Errorf("could not find the box dimensions for the image (box %s)", boxname)
		}
	}
	if boxname == "/MediaBox" {
		return bx, nil
	}
	return intersectBox(bx, pb[p]["/MediaBox"]), nil
}

// ImportPage stages a page from rd as a PdfTemplate using the requested box
// (e.g., "/MediaBox"). The template is normalized for rotation so that its
// width and height (W/H) reflect the final orientation. The function returns the
// index of the created template in the internal list, or an error.
func (pw *PdfWriter) ImportPage(rd *reader.PdfReader, pageno int, boxName string) (int, error) {
	if rd == nil {
		return -1, fmt.Errorf("internal error: reader is nil")
	}
	pw.r = rd
	pageResources, err := rd.GetPageResources(pageno)
	if err != nil {
		return -1, fmt.Errorf("%w: Failed to get page resources", err)
	}

	content, err := rd.GetContent(pageno)
	if err != nil {
		return -1, fmt.Errorf("%w: Failed to get content", err)
	}
	bx, err := pw.GetPDFBoxDimensions(pageno, boxName)
	if err != nil {
		return 0, err
	}

	// Set template values
	tpl := &PdfTemplate{}
	tpl.Reader = rd
	tpl.Resources = pageResources
	tpl.Buffer = content
	tpl.Box = bx
	tpl.X = 0
	tpl.Y = 0
	tpl.W = tpl.Box["w"]
	tpl.H = tpl.Box["h"]

	// Set template rotation
	rotation, err := rd.GetPageRotation(pageno)
	if err != nil {
		return -1, fmt.Errorf("%w: Failed to get page rotation", err)
	}
	angle := rotation.Int % 360

	// Normalize angle
	if angle != 0 {
		steps := angle / 90
		w := tpl.W
		h := tpl.H

		if steps%2 == 0 {
			tpl.W = w
			tpl.H = h
		} else {
			tpl.W = h
			tpl.H = w
		}

		if angle < 0 {
			angle += 360
		}

		// Rotation in PDF is clockwise; we apply a negative angle to counter-rotate content into the form space.
		tpl.Rotation = angle * -1
	}

	pw.tpls = append(pw.tpls, tpl)

	// Return last template id
	return len(pw.tpls) - 1, nil
}

// reserveObjectID returns a fresh object number without creating buffers. If
// NextObjectID is set it is used as the allocator; otherwise an internal counter
// is incremented. The internal nextObjID always tracks the last assigned number.
func (pw *PdfWriter) reserveObjectID() int {
	if pw.NextObjectID != nil {
		pw.nextObjID = pw.NextObjectID()
	} else {
		pw.nextObjID++
	}
	return pw.nextObjID
}

// beginObject starts a new indirect object and initializes currentObj. If objID
// is negative, a fresh ID is reserved. The actual object number is returned. The
// object body is written to pw.currentObj.buffer until endObj() is called.
func (pw *PdfWriter) beginObject(objID int) int {
	// Decide the ID
	if objID < 0 {
		objID = pw.reserveObjectID()
	} else if objID > pw.nextObjID {
		// Keep nextObjID monotonic if an explicit higher id is chosen
		pw.nextObjID = objID
	}

	// Initialize the current object buffer and its identity
	pw.currentObj = &PdfObject{
		id: &PdfObjectID{
			id:   objID,
			hash: pw.shaOfInt(objID),
		},
		buffer: new(bytes.Buffer),
	}

	// Prepare position map for late object reference fixups
	key := PdfObjectKey{ID: pw.currentObj.id.id, Hash: pw.currentObj.id.hash}
	if _, ok := pw.writtenObjPos[key]; !ok {
		pw.writtenObjPos[key] = make(map[int]string, 0)
	}

	return objID
}

// endObj finalizes the current object by storing its serialized body in the
// writer's map under a stable PdfObjectKey. After this call, a new beginObject
// can start another object.
func (pw *PdfWriter) endObj() {
	key := PdfObjectKey{ID: pw.currentObj.id.id, Hash: pw.currentObj.id.hash}
	pw.writtenObjs[key] = pw.currentObj.buffer.Bytes()
}

// shaOfInt builds a per‑document hash for an object number using the reader's
// source file name as salt. If no reader is set, an empty string is returned.
func (pw *PdfWriter) shaOfInt(i int) string {
	if pw.r == nil {
		return ""
	}
	hasher := sha1.New()
	fmt.Fprintf(hasher, "%d-%s", i, pw.r.SourceFile)
	sha := hex.EncodeToString(hasher.Sum(nil))
	return sha
}

// outObjRef writes an indirect reference ("<obj> 0 R") to the current object
// buffer and records a position fixup keyed by the current object's identity so
// that callers can later patch numeric IDs if needed.
func (pw *PdfWriter) outObjRef(objID int) {
	sha := pw.shaOfInt(objID)

	// Keep track of object hash and position - to be replaced with actual object id (integer)
	key := PdfObjectKey{ID: pw.currentObj.id.id, Hash: pw.currentObj.id.hash}
	if _, ok := pw.writtenObjPos[key]; !ok {
		pw.writtenObjPos[key] = make(map[int]string, 0)
	}
	pw.writtenObjPos[key][pw.currentObj.buffer.Len()] = sha
	fmt.Fprintf(pw.currentObj.buffer, "%d", objID)
	pw.currentObj.buffer.WriteString(" 0 R ")
}

// outLine appends a string followed by a newline to the current object buffer.
func (pw *PdfWriter) outLine(s string) {
	pw.currentObj.buffer.WriteString(s)
	pw.currentObj.buffer.WriteString("\n")
}

// straightOut appends a raw string to the current object buffer without a newline.
func (pw *PdfWriter) straightOut(s string) {
	pw.currentObj.buffer.WriteString(s)
}

// writeValue serializes a reader.PdfValue into the current object buffer using a
// minimal subset of PDF syntax. Dictionaries are emitted with sorted keys for
// deterministic output.
func (pw *PdfWriter) writeValue(value *reader.PdfValue) {
	switch value.Type {
	case reader.PDFTypeToken:
		pw.straightOut(value.Token + " ")
	case reader.PDFTypeNumeric:
		pw.straightOut(fmt.Sprintf("%d", value.Int) + " ")
	case reader.PDFTypeReal:
		pw.writeReal(value.Real)
	case reader.PDFTypeArray:
		pw.straightOut("[")
		for i := 0; i < len(value.Array); i++ {
			pw.writeValue(value.Array[i])
		}
		pw.outLine("]")
	case reader.PDFTypeDictionary:
		// Emit dictionary entries in stable (sorted) key order for reproducible output.
		pw.straightOut("<<")
		keys := make([]string, 0, len(value.Dictionary))
		for k := range value.Dictionary {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			pw.straightOut(k + " ")
			pw.writeValue(value.Dictionary[k])
		}
		pw.straightOut(">>")
	case reader.PDFTypeObjRef:
		// An indirect object reference. Assign a new object number on first sight.
		if _, ok := pw.assignedObjIDs[value.ID]; !ok {
			pw.reserveObjectID()
			pw.objStack[value.ID] = &reader.PdfValue{Type: reader.PDFTypeObjRef, Gen: value.Gen, ID: value.ID, NewID: pw.nextObjID}
			pw.assignedObjIDs[value.ID] = &reader.PdfValue{Type: reader.PDFTypeObjRef, Gen: value.Gen, ID: value.ID, NewID: pw.nextObjID}
		}

		// Use the assigned object number.
		objID := pw.assignedObjIDs[value.ID].NewID
		pw.outObjRef(objID)
	case reader.PDFTypeString:
		// A properly escaped literal string
		pw.writePDFString(value.String)
	case reader.PDFTypeStream:
		// A stream. First, output the stream dictionary, then the stream data itself.
		pw.writeValue(value.Value)
		pw.outLine("stream")
		pw.outLine(string(value.Stream.Bytes))
		pw.outLine("endstream")
	case reader.PDFTypeHex:
		pw.straightOut("<" + value.String + ">")
	case reader.PDFTypeBoolean:
		if value.Bool {
			pw.straightOut("true ")
		} else {
			pw.straightOut("false ")
		}
	case reader.PDFTypeNull:
		pw.straightOut("null ")
	}
}

// PutFormXobjects emits one Form XObject per previously imported template. It
// returns a map from the XObject names (e.g., /GOFPDITPL1) to the corresponding
// PdfObjectID. The function also resolves and writes all resources referenced by
// those templates by walking object references in a deterministic order.
func (pw *PdfWriter) PutFormXobjects(reader *reader.PdfReader) (map[string]*PdfObjectID, error) {
	if reader == nil {
		return nil, fmt.Errorf("internal error: reader is nil")
	}
	// Set current reader
	pw.r = reader

	var err error
	var result = make(map[string]*PdfObjectID, 0)

	compress := true
	filter := ""
	if compress {
		filter = "/Filter /FlateDecode "
	}

	for i := 0; i < len(pw.tpls); i++ {
		tpl := pw.tpls[i]
		if tpl == nil {
			return nil, fmt.Errorf("Template is nil")
		}
		var p []byte
		if compress {
			var b bytes.Buffer
			zw := zlib.NewWriter(&b)
			if _, err := zw.Write([]byte(tpl.Buffer)); err != nil {
				_ = zw.Close()
				return nil, fmt.Errorf("flate write failed: %w", err)
			}
			if err := zw.Close(); err != nil {
				return nil, fmt.Errorf("flate close failed: %w", err)
			}
			p = b.Bytes()
			filter = "/Filter /FlateDecode "
		} else {
			p = []byte(tpl.Buffer)
		}
		// Create new PDF object
		pw.beginObject(-1)

		cN := pw.nextObjID // remember current "n"

		tpl.N = pw.nextObjID

		// Return xobject form name and object position
		pdfObjID := new(PdfObjectID)
		pdfObjID.id = cN
		pdfObjID.hash = pw.shaOfInt(cN)
		result[fmt.Sprintf("/GOFPDITPL%d", i+pw.tplIDOffset)] = pdfObjID

		pw.outLine("<<" + filter + "/Type /XObject")
		pw.outLine("/Subtype /Form")
		pw.outLine("/FormType 1")
		pw.outLine(fmt.Sprintf("/BBox [%.2F %.2F %.2F %.2F]", tpl.Box["llx"], tpl.Box["lly"], (tpl.Box["urx"] + tpl.X), (tpl.Box["ury"] - tpl.Y)))
		// Extra dict entries supplied by the host pipeline (e.g.
		// /StructParent for tagged PDFs). The Template index that the
		// caller used at ImportPage time is i + tplIDOffset here.
		if extras, ok := pw.ExtraTemplateDict[i+pw.tplIDOffset]; ok {
			for k, v := range extras {
				pw.outLine("/" + k + " " + v)
			}
		}

		var c, s, tx, ty float64
		c = 1

		// Handle rotated pages
		if tpl.Box != nil && tpl.Box["llx"] != 0 && tpl.Box["lly"] != 0 && tpl.Box["urx"] != 0 && tpl.Box["ury"] != 0 {
			tx = -tpl.Box["llx"]
			ty = -tpl.Box["lly"]

			if tpl.Rotation != 0 {
				angle := float64(tpl.Rotation) * math.Pi / 180.0
				c = math.Cos(float64(angle))
				s = math.Sin(float64(angle))

				switch tpl.Rotation {
				case -90:
					tx = -tpl.Box["lly"]
					ty = tpl.Box["urx"]
				case -180:
					tx = tpl.Box["urx"]
					ty = tpl.Box["ury"]
				case -270:
					tx = tpl.Box["ury"]
					ty = -tpl.Box["llx"]
				}
			}
		} else {
			tx = -tpl.Box["x"] * 2
			ty = tpl.Box["y"] * 2
		}

		if c != 1 || s != 0 || tx != 0 || ty != 0 {
			pw.outLine(fmt.Sprintf("/Matrix [%.5F %.5F %.5F %.5F %.5F %.5F]", c, s, -s, c, tx, ty))
		}

		// Now write resources
		pw.outLine("/Resources")

		if tpl.Resources == nil {
			return nil, fmt.Errorf("template resources are empty")
		}
		pw.writeValue(tpl.Resources)

		nN := pw.nextObjID // remember new "n"
		pw.nextObjID = cN  // reset to current "n"

		pw.outLine("/Length " + strconv.Itoa(len(p)) + " >>")
		pw.outLine("stream")
		pw.outLine(string(p))
		pw.outLine("endstream")

		pw.endObj()

		pw.nextObjID = nN // reset to new "n"

		// Put imported objects, starting with the ones from the XObject's Resources,
		// then from dependencies of those resources).
		err = pw.putImportedObjects(reader)
		if err != nil {
			return nil, fmt.Errorf("%w: Failed to put imported objects", err)
		}
	}

	return result, nil
}

// putImportedObjects drains objStack by resolving and writing each referenced
// object exactly once, in a deterministic (sorted) order. New references that
// appear while resolving objects are picked up in the next iteration.
func (pw *PdfWriter) putImportedObjects(rd *reader.PdfReader) error {
	for {
		// Build a sorted key list of all currently pending items.
		keys := make([]int, 0, len(pw.objStack))
		for k, v := range pw.objStack {
			if v != nil {
				keys = append(keys, k)
			}
		}
		sort.Ints(keys)

		if len(keys) == 0 {
			break
		}

		for _, k := range keys {
			v := pw.objStack[k]
			if v == nil {
				continue
			}

			// Resolve and write object
			nObj, err := rd.ResolveObject(v)
			if err != nil {
				return fmt.Errorf("%w: Unable to resolve object", err)
			}

			// Create new object with the pre-assigned NewID
			pw.beginObject(v.NewID)

			if nObj.Type == reader.PDFTypeStream {
				pw.writeValue(nObj)
			} else {
				pw.writeValue(nObj.Value)
			}
			pw.endObj()

			// Mark consumed
			pw.objStack[k] = nil
		}
	}
	return nil
}

// writeReal writes a compact decimal representation suitable for PDF content.
// Trailing zeros and a trailing decimal point are trimmed, and "-0" is avoided.
func (pw *PdfWriter) writeReal(x float64) {
	// 6 decimals is common in PDFs; adjust if you need more precision.
	s := strconv.FormatFloat(x, 'f', 6, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "-0" {
		s = "0"
	}
	pw.straightOut(s + " ")
}

// writePDFString writes a literal string with proper escaping as required by the
// PDF specification. Control characters are escaped using short sequences; for
// fully binary‑safe emission consider octal escapes.
func (pw *PdfWriter) writePDFString(s string) {
	var b strings.Builder
	b.WriteByte('(')
	for _, r := range s {
		switch r {
		case '\\', '(', ')':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\r':
			b.WriteString("\\r")
		case '\n':
			b.WriteString("\\n")
		case '\t':
			b.WriteString("\\t")
		case '\b':
			b.WriteString("\\b")
		case '\f':
			b.WriteString("\\f")
		default:
			// Emit as-is; if you need full binary safety, consider octal escapes.
			b.WriteRune(r)
		}
	}
	b.WriteByte(')')
	pw.straightOut(b.String())
}
