package gofpdi

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"math"
	"strconv"
	"strings"

	src "github.com/speedata/pdfdisassembler"
)

// PdfWriter serializes the objects reachable from imported pages into PDF
// object bodies. It assigns fresh output object numbers and rewrites every
// indirect reference in the copied graph to the newly assigned numbers.
// Streams are copied verbatim — their parameter dictionary plus their raw,
// still filter-encoded bytes — so image and font data are never re-encoded.
//
// PdfWriter is not safe for concurrent use.
type PdfWriter struct {
	reader *src.Reader

	tpls []*pdfTemplate

	// refMap maps a source indirect reference to the output object number
	// assigned to it on first sight; queue holds references discovered but not
	// yet copied. Keying on the source Reference deduplicates shared objects
	// (a font referenced by several pages is copied once).
	refMap map[src.Reference]int
	queue  []refJob

	// Object numbering: NextObjectID, when set, is the host's allocator;
	// otherwise nextObjID is incremented internally. nextObjID always tracks
	// the last number handed out.
	nextObjID    int
	NextObjectID func() int

	// currentObj is the buffer the serializer writes into; writtenObjs
	// collects each finished object body keyed by its output number.
	currentObj  *bytes.Buffer
	writtenObjs map[int][]byte

	// err is a sticky error: the recursive serializer cannot return errors, so
	// the first stream-read failure is recorded here and surfaced by the
	// caller (PutFormXobjects).
	err error

	// ExtraTemplateDict carries additional Form XObject dictionary entries
	// keyed by template index (see Importer.SetTemplateDictEntry).
	ExtraTemplateDict map[int]map[string]string
}

// refJob is a pending object copy: the source reference and the output number
// already reserved for it.
type refJob struct {
	ref   src.Reference
	objID int
}

// pdfTemplate is a staged page awaiting serialization as a Form XObject.
type pdfTemplate struct {
	resources *src.Dict          // resolved page /Resources, inlined into the XObject
	content   []byte             // decoded page content stream
	box       map[string]float64 // chosen box (llx/lly/urx/ury/x/y/w/h)
	rotation  int                // counter-rotation in degrees (0, -90, -180, -270)
}

// NewPdfWriter returns a fully initialized PdfWriter.
func NewPdfWriter() *PdfWriter {
	return &PdfWriter{
		refMap:      make(map[src.Reference]int),
		writtenObjs: make(map[int][]byte),
	}
}

// SetNextObjectID sets the internal counter so the next reserved number
// becomes id. Ignored when an external allocator is installed.
func (pw *PdfWriter) SetNextObjectID(id int) {
	pw.nextObjID = id - 1
}

// reserveObjectID returns a fresh output object number from the external
// allocator if present, otherwise from the internal counter.
func (pw *PdfWriter) reserveObjectID() int {
	if pw.NextObjectID != nil {
		pw.nextObjID = pw.NextObjectID()
	} else {
		pw.nextObjID++
	}
	return pw.nextObjID
}

// setErr records the first error seen during serialization.
func (pw *PdfWriter) setErr(err error) {
	if pw.err == nil {
		pw.err = err
	}
}

// stageTemplate captures everything needed to emit page as a Form XObject and
// returns its template index. It reserves no object numbers; numbering happens
// in PutFormXobjects.
func (pw *PdfWriter) stageTemplate(page *src.Page, boxName string) (int, error) {
	box, err := pageBoxDimensions(page, boxName)
	if err != nil {
		return 0, err
	}
	content, err := page.Content()
	if err != nil {
		return 0, fmt.Errorf("gofpdi: read page content: %w", err)
	}
	resources, _ := page.Resources() // ok=false -> nil, emitted as <<>>

	tpl := &pdfTemplate{
		resources: resources,
		content:   content,
		box:       box,
	}
	// PDF /Rotate is clockwise; counter-rotate the content into form space by
	// the negated angle. Rotation() is already normalized to 0/90/180/270.
	if angle := page.Rotation(); angle != 0 {
		tpl.rotation = -angle
	}

	pw.tpls = append(pw.tpls, tpl)
	return len(pw.tpls) - 1, nil
}

// PutFormXobjects emits each staged template as a Form XObject and copies the
// objects reachable from its resources.
func (pw *PdfWriter) PutFormXobjects() (map[string]int, error) {
	if pw.reader == nil {
		return nil, fmt.Errorf("gofpdi: no source reader")
	}
	result := make(map[string]int, len(pw.tpls))

	for i, tpl := range pw.tpls {
		// Flate-compress the page content for the XObject body.
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		if _, err := zw.Write(tpl.content); err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("gofpdi: compress content: %w", err)
		}
		if err := zw.Close(); err != nil {
			return nil, fmt.Errorf("gofpdi: compress content: %w", err)
		}
		body := buf.Bytes()

		// Reserve the XObject's own number FIRST: the host allocator (e.g.
		// baseline-pdf) returns the pre-allocated image object on its first
		// call, so this must be the first number drawn for the page.
		xobjID := pw.reserveObjectID()
		result[fmt.Sprintf("/GOFPDITPL%d", i)] = xobjID

		pw.currentObj = new(bytes.Buffer)
		pw.writeFormXObject(tpl, body, i)
		if pw.err != nil {
			return nil, pw.err
		}
		pw.writtenObjs[xobjID] = pw.currentObj.Bytes()

		// Copy every object reachable from the resources, in turn discovering
		// their dependencies, until the queue drains.
		if err := pw.drain(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// writeFormXObject serializes one Form XObject body (no obj/endobj wrapper).
// References inside /Resources are assigned output numbers and queued for
// copying as a side effect of writeDict.
func (pw *PdfWriter) writeFormXObject(tpl *pdfTemplate, body []byte, tplIndex int) {
	b := pw.currentObj
	b.WriteString("<</Type /XObject /Subtype /Form /FormType 1 /Filter /FlateDecode\n")
	fmt.Fprintf(b, "/BBox [%.2F %.2F %.2F %.2F]\n",
		tpl.box["llx"], tpl.box["lly"], tpl.box["urx"], tpl.box["ury"])

	for k, v := range pw.ExtraTemplateDict[tplIndex] {
		fmt.Fprintf(b, "/%s %s\n", k, v)
	}

	if c, s, tx, ty := formMatrix(tpl); c != 1 || s != 0 || tx != 0 || ty != 0 {
		fmt.Fprintf(b, "/Matrix [%.5F %.5F %.5F %.5F %.5F %.5F]\n", c, s, -s, c, tx, ty)
	}

	b.WriteString("/Resources ")
	if tpl.resources == nil {
		b.WriteString("<<>>")
	} else {
		pw.writeDict(tpl.resources)
	}
	b.WriteByte('\n')

	fmt.Fprintf(b, "/Length %d >>\n", len(body))
	b.WriteString("stream\n")
	b.Write(body)
	b.WriteString("\nendstream")
}

// drain copies each queued source object exactly once, picking up newly
// discovered references as it goes, until nothing remains.
func (pw *PdfWriter) drain() error {
	for len(pw.queue) > 0 {
		job := pw.queue[0]
		pw.queue = pw.queue[1:]

		obj, err := pw.reader.Resolve(job.ref)
		if err != nil {
			return fmt.Errorf("gofpdi: resolve %d %d R: %w", job.ref.Number, job.ref.Generation, err)
		}
		pw.currentObj = new(bytes.Buffer)
		pw.writeObject(obj)
		if pw.err != nil {
			return pw.err
		}
		pw.writtenObjs[job.objID] = pw.currentObj.Bytes()
	}
	return nil
}

// assignRef returns the output number for a source reference, assigning and
// queueing it on first sight.
func (pw *PdfWriter) assignRef(ref src.Reference) int {
	if id, ok := pw.refMap[ref]; ok {
		return id
	}
	id := pw.reserveObjectID()
	pw.refMap[ref] = id
	pw.queue = append(pw.queue, refJob{ref: ref, objID: id})
	return id
}

// writeObject serializes a single PDF object into the current buffer. Indirect
// references are rewritten to their assigned output numbers (and queued);
// every output object is generation 0.
func (pw *PdfWriter) writeObject(obj src.Object) {
	if pw.err != nil {
		return
	}
	b := pw.currentObj
	switch o := obj.(type) {
	case src.Reference:
		fmt.Fprintf(b, "%d 0 R ", pw.assignRef(o))
	case src.Name:
		b.WriteString("/" + escapeName(string(o)) + " ")
	case src.Integer:
		b.WriteString(strconv.FormatInt(int64(o), 10) + " ")
	case src.Real:
		pw.writeReal(float64(o))
	case src.Bool:
		if o {
			b.WriteString("true ")
		} else {
			b.WriteString("false ")
		}
	case src.String:
		pw.writePDFString([]byte(o))
	case src.Array:
		b.WriteByte('[')
		for _, e := range o {
			pw.writeObject(e)
		}
		b.WriteByte(']')
	case *src.Dict:
		pw.writeDict(o)
	case *src.Stream:
		pw.writeStream(o)
	case src.Null:
		b.WriteString("null ")
	default:
		pw.setErr(fmt.Errorf("gofpdi: cannot serialize %T", obj))
	}
}

// writeDict serializes a dictionary in source insertion order.
func (pw *PdfWriter) writeDict(d *src.Dict) {
	b := pw.currentObj
	b.WriteString("<<")
	for k, v := range d.Iter() {
		b.WriteString("/" + escapeName(k) + " ")
		pw.writeObject(v)
	}
	b.WriteString(">>")
}

// writeStream serializes a stream verbatim: its parameter dictionary (with
// /Length replaced by the true raw byte count, so an indirect-length source
// stream stays valid) followed by the raw, undecoded bytes.
func (pw *PdfWriter) writeStream(s *src.Stream) {
	raw, err := s.RawBytes()
	if err != nil {
		pw.setErr(fmt.Errorf("gofpdi: read stream bytes: %w", err))
		return
	}
	b := pw.currentObj
	b.WriteString("<<")
	for k, v := range s.Dict.Iter() {
		if k == "Length" {
			continue // re-emitted from the actual byte count below
		}
		b.WriteString("/" + escapeName(k) + " ")
		pw.writeObject(v)
	}
	fmt.Fprintf(b, "/Length %d>>\n", len(raw))
	b.WriteString("stream\n")
	b.Write(raw)
	b.WriteString("\nendstream")
}

// writeReal serializes a real number with the shortest exact decimal (PDF
// reals have no exponent form, so 'f' formatting is mandatory).
func (pw *PdfWriter) writeReal(x float64) {
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if s == "-0" {
		s = "0"
	}
	pw.currentObj.WriteString(s + " ")
}

// writePDFString serializes a literal string with binary-safe escaping. Bytes
// outside the printable ASCII range are written as octal escapes.
func (pw *PdfWriter) writePDFString(data []byte) {
	b := pw.currentObj
	b.WriteByte('(')
	for _, c := range data {
		switch c {
		case '\\', '(', ')':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if c < 0x20 || c > 0x7e {
				fmt.Fprintf(b, `\%03o`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte(')')
}

// escapeName re-encodes a PDF name body, #-escaping the delimiter and
// whitespace characters that are not legal in a raw name token (PDF 32000-1
// §7.3.5). The parser stored the decoded name, so this restores a writable
// form.
func escapeName(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < '!' || c > '~' || strings.IndexByte("#/()<>[]{}%", c) >= 0 {
			fmt.Fprintf(&b, "#%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// formMatrix computes the Form XObject /Matrix entries that translate the
// chosen box's lower-left corner to the form origin and apply the page
// rotation. The math mirrors the historical gofpdi behaviour.
func formMatrix(tpl *pdfTemplate) (c, s, tx, ty float64) {
	c = 1
	box := tpl.box
	if box["llx"] != 0 && box["lly"] != 0 && box["urx"] != 0 && box["ury"] != 0 {
		tx = -box["llx"]
		ty = -box["lly"]
		if tpl.rotation != 0 {
			angle := float64(tpl.rotation) * math.Pi / 180
			c = math.Cos(angle)
			s = math.Sin(angle)
			switch tpl.rotation {
			case -90:
				tx = -box["lly"]
				ty = box["urx"]
			case -180:
				tx = box["urx"]
				ty = box["ury"]
			case -270:
				tx = box["ury"]
				ty = -box["llx"]
			}
		}
	} else {
		tx = -box["x"] * 2
		ty = box["y"] * 2
	}
	return c, s, tx, ty
}

// pageBoxDimensions returns the requested box for page, clamped to the
// MediaBox for non-media boxes and with the usual spec fallbacks (a missing
// CropBox defaults to the MediaBox; art/bleed/trim default to the CropBox).
func pageBoxDimensions(page *src.Page, boxName string) (map[string]float64, error) {
	name := src.BoxName(strings.TrimPrefix(boxName, "/"))
	if name == "" {
		name = src.MediaBox
	}

	media, ok := page.Box(src.MediaBox)
	if !ok {
		return nil, fmt.Errorf("gofpdi: page %d has no /MediaBox", page.Index()+1)
	}

	rect, ok := page.Box(name)
	if !ok {
		switch name {
		case src.MediaBox, src.CropBox:
			rect = media
		default:
			if cb, ok := page.Box(src.CropBox); ok {
				rect = cb
			} else {
				rect = media
			}
		}
	}
	if name != src.MediaBox {
		rect = intersectRect(rect, media)
	}
	return rectToMap(rect), nil
}

// intersectRect clamps b so it does not extend beyond media.
func intersectRect(b, media src.Rect) src.Rect {
	if b.LLX < media.LLX {
		b.LLX = media.LLX
	}
	if b.LLY < media.LLY {
		b.LLY = media.LLY
	}
	if b.URX > media.URX {
		b.URX = media.URX
	}
	if b.URY > media.URY {
		b.URY = media.URY
	}
	return b
}

// rectToMap converts a Rect to the llx/lly/urx/ury plus x/y/w/h map shape the
// box consumers expect.
func rectToMap(r src.Rect) map[string]float64 {
	return map[string]float64{
		"llx": r.LLX,
		"lly": r.LLY,
		"urx": r.URX,
		"ury": r.URY,
		"x":   r.LLX,
		"y":   r.LLY,
		"w":   r.Width(),
		"h":   r.Height(),
	}
}
