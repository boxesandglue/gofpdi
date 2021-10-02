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

	"github.com/speedata/gofpdi/reader"
)

type PdfWriter struct {
	f       *os.File
	w       *bufio.Writer
	r       *reader.PdfReader
	k       float64
	tpls    []*PdfTemplate
	m       int
	n       int
	offsets map[int]int
	offset  int
	result  map[int]string
	// Keep track of which objects have already been written
	objStack      map[int]*reader.PdfValue
	doOobjStack   map[int]*reader.PdfValue
	writtenObjs   map[*PdfObjectID][]byte
	writtenObjPos map[*PdfObjectID]map[int]string
	currentObj    *PdfObject
	currentObjID  int
	tplIDOffset   int
	useHash       bool
	NextObjectID  func() int
}

type PdfObjectID struct {
	id   int
	hash string
}

type PdfObject struct {
	id     *PdfObjectID
	buffer *bytes.Buffer
}

func (pw *PdfWriter) SetTplIDOffset(n int) {
	pw.tplIDOffset = n
}

func (pw *PdfWriter) SetUseHash(b bool) {
	pw.useHash = b
}

func (pw *PdfWriter) SetNextObjectID(id int) {
	pw.n = id - 1
}

func NewPdfWriter() *PdfWriter {
	pw := &PdfWriter{}
	pw.k = 1
	pw.objStack = make(map[int]*reader.PdfValue, 0)
	pw.doOobjStack = make(map[int]*reader.PdfValue, 0)
	pw.tpls = make([]*PdfTemplate, 0)
	pw.writtenObjs = make(map[*PdfObjectID][]byte, 0)
	pw.writtenObjPos = make(map[*PdfObjectID]map[int]string, 0)
	pw.currentObj = new(PdfObject)

	return pw
}

// Done with parsing.  Now, create templates.
type PdfTemplate struct {
	ID        int
	Reader    *reader.PdfReader
	Resources *reader.PdfValue
	Buffer    string
	Box       map[string]float64
	Boxes     map[string]map[string]float64
	X         float64
	Y         float64
	W         float64
	H         float64
	Rotation  int
	N         int
}

// GetImportedObjects returns all byte slices for the imported objects
func (pw *PdfWriter) GetImportedObjects() map[*PdfObjectID][]byte {
	return pw.writtenObjs
}

// GetImportedObjHashPos returns the position of each hash within the object,
// for each object (uniquely identified by a sha1 hash), to be replaced with
// pdf object ids (integers)
func (pw *PdfWriter) GetImportedObjHashPos() map[*PdfObjectID]map[int]string {
	return pw.writtenObjPos
}

// ClearImportedObjects deletes all imported objects
func (pw *PdfWriter) ClearImportedObjects() {
	pw.writtenObjs = make(map[*PdfObjectID][]byte, 0)
}

// ImportPage creates a PdfTemplate object from a page number (e.g. 1) and a boxName (e.g. /MediaBox)
func (pw *PdfWriter) ImportPage(rd *reader.PdfReader, pageno int, boxName string) (int, error) {
	var err error

	// Set default scale to 1
	pw.k = 1

	// Get all page boxes
	pageBoxes, err := rd.GetPageBoxes(1, pw.k)
	if err != nil {
		return -1, fmt.Errorf("%w: Failed to get page boxes", err)
	}

	// If requested box name does not exist for this page, use an alternate box
	if _, ok := pageBoxes[boxName]; !ok {
		if boxName == "/BleedBox" || boxName == "/TrimBox" || boxName == "ArtBox" {
			boxName = "/CropBox"
		} else if boxName == "/CropBox" {
			boxName = "/MediaBox"
		}
	}

	// If the requested box name or an alternate box name cannot be found, trigger an error
	// TODO: Improve error handling
	if _, ok := pageBoxes[boxName]; !ok {
		return -1, fmt.Errorf("Box not found: %s", boxName)
	}

	pageResources, err := rd.GetPageResources(pageno)
	if err != nil {
		return -1, fmt.Errorf("%w: Failed to get page resources", err)
	}

	content, err := rd.GetContent(pageno)
	if err != nil {
		return -1, fmt.Errorf("%w: Failed to get content", err)
	}

	// Set template values
	tpl := &PdfTemplate{}
	tpl.Reader = rd
	tpl.Resources = pageResources
	tpl.Buffer = content
	tpl.Box = pageBoxes[boxName]
	tpl.Boxes = pageBoxes
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

		tpl.Rotation = angle * -1
	}

	pw.tpls = append(pw.tpls, tpl)

	// Return last template id
	return len(pw.tpls) - 1, nil
}

// Create a new object and keep track of the offset for the xref table
func (pw *PdfWriter) newObj(objID int, onlyNewObj bool) {
	if objID < 0 {
		if pw.NextObjectID != nil {
			pw.n = pw.NextObjectID()
		} else {
			pw.n++
		}
		objID = pw.n
	}

	if !onlyNewObj {
		// set current object id integer
		pw.currentObjID = objID

		// Create new PdfObject and PdfObjectId
		pw.currentObj = new(PdfObject)
		pw.currentObj.buffer = new(bytes.Buffer)
		pw.currentObj.id = new(PdfObjectID)
		pw.currentObj.id.id = objID
		pw.currentObj.id.hash = pw.shaOfInt(objID)

		pw.writtenObjPos[pw.currentObj.id] = make(map[int]string, 0)
	}
}

func (pw *PdfWriter) endObj() {
	pw.writtenObjs[pw.currentObj.id] = pw.currentObj.buffer.Bytes()
	pw.currentObjID = -1
}

func (pw *PdfWriter) shaOfInt(i int) string {
	hasher := sha1.New()
	hasher.Write([]byte(fmt.Sprintf("%d-%s", i, pw.r.SourceFile)))
	sha := hex.EncodeToString(hasher.Sum(nil))
	return sha
}

func (pw *PdfWriter) outObjRef(objID int) {
	sha := pw.shaOfInt(objID)

	// Keep track of object hash and position - to be replaced with actual object id (integer)
	pw.writtenObjPos[pw.currentObj.id][pw.currentObj.buffer.Len()] = sha

	if pw.useHash {
		pw.currentObj.buffer.WriteString(sha)
	} else {
		pw.currentObj.buffer.WriteString(fmt.Sprintf("%d", objID))
	}
	pw.currentObj.buffer.WriteString(" 0 R ")
}

// Output PDF data with a newline
func (pw *PdfWriter) out(s string) {
	pw.currentObj.buffer.WriteString(s)
	pw.currentObj.buffer.WriteString("\n")
}

// Output PDF data
func (pw *PdfWriter) straightOut(s string) {
	pw.currentObj.buffer.WriteString(s)
}

// Output a PdfValue
func (pw *PdfWriter) writeValue(value *reader.PdfValue) {
	switch value.Type {
	case reader.PDFTypeToken:
		pw.straightOut(value.Token + " ")
		break

	case reader.PDFTypeNumeric:
		pw.straightOut(fmt.Sprintf("%d", value.Int) + " ")
		break

	case reader.PDFTypeReal:
		pw.straightOut(fmt.Sprintf("%F", value.Real) + " ")
		break

	case reader.PDFTypeArray:
		pw.straightOut("[")
		for i := 0; i < len(value.Array); i++ {
			pw.writeValue(value.Array[i])
		}
		pw.out("]")
		break

	case reader.PDFTypeDictionary:
		pw.straightOut("<<")
		for k, v := range value.Dictionary {
			pw.straightOut(k + " ")
			pw.writeValue(v)
		}
		pw.straightOut(">>")
		break

	case reader.PDFTypeObjRef:
		// An indirect object reference.  Fill the object stack if needed.
		// Check to see if object already exists on the don_obj_stack.
		if _, ok := pw.doOobjStack[value.ID]; !ok {
			pw.newObj(-1, true)
			pw.objStack[value.ID] = &reader.PdfValue{Type: reader.PDFTypeObjRef, Gen: value.Gen, ID: value.ID, NewID: pw.n}
			pw.doOobjStack[value.ID] = &reader.PdfValue{Type: reader.PDFTypeObjRef, Gen: value.Gen, ID: value.ID, NewID: pw.n}
		}

		// Get object ID from don_obj_stack
		objID := pw.doOobjStack[value.ID].NewID
		pw.outObjRef(objID)
		//this.out(fmt.Sprintf("%d 0 R", objId))
		break

	case reader.PDFTypeString:
		// A string
		pw.straightOut("(" + value.String + ")")
		break

	case reader.PDFTypeStream:
		// A stream.  First, output the stream dictionary, then the stream data itself.
		pw.writeValue(value.Value)
		pw.out("stream")
		pw.out(string(value.Stream.Bytes))
		pw.out("endstream")
		break

	case reader.PDFTypeHex:
		pw.straightOut("<" + value.String + ">")
		break

	case reader.PDFTypeBoolean:
		if value.Bool {
			pw.straightOut("true")
		} else {
			pw.straightOut("false")
		}
		break

	case reader.PDFTypeNull:
		// The null object
		pw.straightOut("null ")
		break
	}
}

// PutFormXobjects puts form xobjects and get back a map of template names (e.g.
// /GOFPDITPL1) and their object ids (int)
func (pw *PdfWriter) PutFormXobjects(reader *reader.PdfReader) (map[string]*PdfObjectID, error) {
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
		var p string
		if compress {
			var b bytes.Buffer
			w := zlib.NewWriter(&b)
			w.Write([]byte(tpl.Buffer))
			w.Close()

			p = b.String()
		} else {
			p = tpl.Buffer
		}

		// Create new PDF object
		pw.newObj(-1, false)

		cN := pw.n // remember current "n"

		tpl.N = pw.n

		// Return xobject form name and object position
		pdfObjID := new(PdfObjectID)
		pdfObjID.id = cN
		pdfObjID.hash = pw.shaOfInt(cN)
		result[fmt.Sprintf("/GOFPDITPL%d", i+pw.tplIDOffset)] = pdfObjID

		pw.out("<<" + filter + "/Type /XObject")
		pw.out("/Subtype /Form")
		pw.out("/FormType 1")

		pw.out(fmt.Sprintf("/BBox [%.2F %.2F %.2F %.2F]", tpl.Box["llx"]*pw.k, tpl.Box["lly"]*pw.k, (tpl.Box["urx"]+tpl.X)*pw.k, (tpl.Box["ury"]-tpl.Y)*pw.k))

		var c, s, tx, ty float64
		c = 1

		// Handle rotated pages
		if tpl.Box != nil {
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
					break

				case -180:
					tx = tpl.Box["urx"]
					ty = tpl.Box["ury"]
					break

				case -270:
					tx = tpl.Box["ury"]
					ty = -tpl.Box["llx"]
				}
			}
		} else {
			tx = -tpl.Box["x"] * 2
			ty = tpl.Box["y"] * 2
		}

		tx *= pw.k
		ty *= pw.k

		if c != 1 || s != 0 || tx != 0 || ty != 0 {
			pw.out(fmt.Sprintf("/Matrix [%.5F %.5F %.5F %.5F %.5F %.5F]", c, s, -s, c, tx, ty))
		}

		// Now write resources
		pw.out("/Resources ")

		if tpl.Resources != nil {
			pw.writeValue(tpl.Resources) // "n" will be changed
		} else {
			return nil, fmt.Errorf("Template resources are empty")
		}

		nN := pw.n // remember new "n"
		pw.n = cN  // reset to current "n"

		pw.out("/Length " + fmt.Sprintf("%d", len(p)) + " >>")

		pw.out("stream")
		pw.out(p)
		pw.out("endstream")

		pw.endObj()

		pw.n = nN // reset to new "n"

		// Put imported objects, starting with the ones from the XObject's Resources,
		// then from dependencies of those resources).
		err = pw.putImportedObjects(reader)
		if err != nil {
			return nil, fmt.Errorf("%w: Failed to put imported objects", err)
		}
	}

	return result, nil
}

func (pw *PdfWriter) putImportedObjects(rd *reader.PdfReader) error {
	var err error
	var nObj *reader.PdfValue

	// obj_stack will have new items added to it in the inner loop, so do another loop to check for extras
	// TODO make the order of this the same every time
	for {
		atLeastOne := false

		// FIXME:  How to determine number of objects before this loop?
		for i := 0; i < 9999; i++ {
			k := i
			v := pw.objStack[i]

			if v == nil {
				continue
			}

			atLeastOne = true

			nObj, err = rd.ResolveObject(v)
			if err != nil {
				return fmt.Errorf("%w: Unable to resolve object", err)
			}

			// New object with "NewId" field
			pw.newObj(v.NewID, false)

			if nObj.Type == reader.PDFTypeStream {
				pw.writeValue(nObj)
			} else {
				pw.writeValue(nObj.Value)
			}

			pw.endObj()

			// Remove from stack
			pw.objStack[k] = nil
		}

		if !atLeastOne {
			break
		}
	}

	return nil
}

// Get the calculated size of a template
// If one size is given, this method calculates the other one
func (pw *PdfWriter) getTemplateSize(tplid int, _w float64, _h float64) map[string]float64 {
	result := make(map[string]float64, 2)

	tpl := pw.tpls[tplid]

	w := tpl.W
	h := tpl.H

	if _w == 0 && _h == 0 {
		_w = w
		_h = h
	}

	if _w == 0 {
		_w = _h * w / h
	}

	if _h == 0 {
		_h = _w * h / w
	}

	result["w"] = _w
	result["h"] = _h

	return result
}

// UseTemplate ...
func (pw *PdfWriter) UseTemplate(tplid int, _x float64, _y float64, _w float64, _h float64) (string, float64, float64, float64, float64) {
	tpl := pw.tpls[tplid]

	w := tpl.W
	h := tpl.H

	_x += tpl.X
	_y += tpl.Y

	wh := pw.getTemplateSize(0, _w, _h)

	_w = wh["w"]
	_h = wh["h"]

	tData := make(map[string]float64, 9)
	tData["x"] = 0.0
	tData["y"] = 0.0
	tData["w"] = _w
	tData["h"] = _h
	tData["scaleX"] = (_w / w)
	tData["scaleY"] = (_h / h)
	tData["tx"] = _x
	tData["ty"] = (0 - _y - _h)
	tData["lty"] = (0 - _y - _h) - (0-h)*(_h/h)

	return fmt.Sprintf("/GOFPDITPL%d", tplid+pw.tplIDOffset), tData["scaleX"], tData["scaleY"], tData["tx"] * pw.k, tData["ty"] * pw.k
}
