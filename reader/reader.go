package reader

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"os"
	"strconv"
)

// A PdfReader reads a PDF file for importing
type PdfReader struct {
	availableBoxes []string
	stack          []string
	trailer        *PdfValue
	catalog        *PdfValue
	pages          []*PdfValue
	xrefPos        int
	xref           map[int]map[int]int
	xrefStream     map[int][2]int
	f              io.ReadSeeker
	nBytes         int64
	SourceFile     string
	curPage        int
	alreadyRead    bool
	pageCount      int
}

// NewPdfReaderFromStream opens the io.ReadSeeker and returns a PdfReader object
func NewPdfReaderFromStream(rs io.ReadSeeker) (*PdfReader, error) {
	length, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("%w: Failed to determine stream length", err)
	}
	parser := &PdfReader{f: rs, nBytes: length}
	if err := parser.init(); err != nil {
		return nil, fmt.Errorf("%w: Failed to initialize parser", err)
	}
	if err := parser.read(); err != nil {
		return nil, fmt.Errorf("%w:Failed to read pdf from stream", err)
	}
	return parser, nil
}

// NewPdfReader opens a PDF file and returns a PdfReader
func NewPdfReader(filename string) (*PdfReader, error) {
	var err error
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to open file", err)
	}
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to obtain file information", err)
	}

	parser := &PdfReader{f: f, SourceFile: filename, nBytes: info.Size()}
	if err = parser.init(); err != nil {
		return nil, fmt.Errorf("%w:Failed to initialize parser", err)
	}
	if err = parser.read(); err != nil {
		return nil, fmt.Errorf("%w:Failed to read pdf", err)
	}

	return parser, nil
}

func (pr *PdfReader) init() error {
	pr.availableBoxes = []string{"/MediaBox", "/CropBox", "/BleedBox", "/TrimBox", "/ArtBox"}
	pr.xref = make(map[int]map[int]int, 0)
	pr.xrefStream = make(map[int][2]int, 0)
	err := pr.read()
	if err != nil {
		return fmt.Errorf("%w:Failed to read pdf", err)
	}
	return nil
}

// A PdfValue holds any data structure found within a PdF file. The used file is
// provided by the Type attribute
type PdfValue struct {
	Type       int
	String     string
	Token      string
	Int        int
	Real       float64
	Bool       bool
	Dictionary map[string]*PdfValue
	Array      []*PdfValue
	ID         int
	NewID      int
	Gen        int
	Value      *PdfValue
	Stream     *PdfValue
	Bytes      []byte
}

// Jump over comments
func (pr *PdfReader) skipComments(r *bufio.Reader) error {
	var err error
	var b byte

	for {
		b, err = r.ReadByte()
		if err != nil {
			return fmt.Errorf("%w:Failed to ReadByte while skipping comments", err)
		}

		if b == '\n' || b == '\r' {
			if b == '\r' {
				// Peek and see if next char is \n
				b2, err := r.ReadByte()
				if err != nil {
					return fmt.Errorf("%w:Failed to read byte", err)
				}
				if b2 != '\n' {
					r.UnreadByte()
				}
			}
			break
		}
	}

	return nil
}

// Advance reader so that whitespace is ignored
func (pr *PdfReader) skipWhitespace(r *bufio.Reader) error {
	var err error
	var b byte

	for {
		b, err = r.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("%w:Failed to read byte", err)
		}

		if b == ' ' || b == '\n' || b == '\r' || b == '\t' {
			continue
		} else {
			r.UnreadByte()
			break
		}
	}

	return nil
}

// Read a token
func (pr *PdfReader) readToken(r *bufio.Reader) (string, error) {
	var err error

	// If there is a token available on the stack, pop it out and return it.
	if len(pr.stack) > 0 {
		var popped string
		popped, pr.stack = pr.stack[len(pr.stack)-1], pr.stack[:len(pr.stack)-1]
		return popped, nil
	}

	err = pr.skipWhitespace(r)
	if err != nil {
		return "", fmt.Errorf("%w:Failed to skip whitespace", err)
	}

	b, err := r.ReadByte()
	if err != nil {
		if err == io.EOF {
			return "", nil
		}
		return "", fmt.Errorf("%w:Failed to read byte", err)
	}

	switch b {
	case '[', ']', '(', ')':
		// This is either an array or literal string delimeter, return it.
		return string(b), nil

	case '<', '>':
		// This could either be a hex string or a dictionary delimiter.
		// Determine the appropriate case and return the token.
		nb, err := r.ReadByte()
		if err != nil {
			return "", fmt.Errorf("%w:Failed to read byte", err)
		}
		if nb == b {
			return string(b) + string(nb), nil
		}
		r.UnreadByte()
		return string(b), nil

	case '%':
		err = pr.skipComments(r)
		if err != nil {
			return "", fmt.Errorf("%w:Failed to skip comments", err)
		}
		return pr.readToken(r)

	default:
		// FIXME this may not be performant to create new strings for each byte
		// Is it probably better to create a buffer and then convert to a string at the end.
		str := string(b)

	loop:
		for {
			b, err := r.ReadByte()
			if err != nil {
				return "", fmt.Errorf("%w:Failed to read byte", err)
			}
			switch b {
			case ' ', '%', '[', ']', '<', '>', '(', ')', '\r', '\n', '\t', '/':
				r.UnreadByte()
				break loop
			default:
				str += string(b)
			}
		}
		return str, nil
	}
}

// Read a value based on a token
func (pr *PdfReader) readValue(r *bufio.Reader, t string) (*PdfValue, error) {
	var err error
	var b byte

	result := &PdfValue{}
	result.Type = -1
	result.Token = t
	result.Dictionary = make(map[string]*PdfValue, 0)
	result.Array = make([]*PdfValue, 0)

	switch t {
	case "<":
		// This is a hex string

		// Read bytes until '>' is found
		var s string
		for {
			b, err = r.ReadByte()
			if err != nil {
				return nil, fmt.Errorf("%w:Failed to read byte", err)
			}
			if b != '>' {
				s += string(b)
			} else {
				break
			}
		}

		result.Type = PDFTypeHex
		result.String = s

	case "<<":
		// This is a dictionary

		// Recurse into this function until we reach the end of the dictionary.
		for {
			key, err := pr.readToken(r)
			if err != nil {
				return nil, fmt.Errorf("%w:Failed to read token", err)
			}
			if key == "" {
				return nil, fmt.Errorf("Token is empty")
			}

			if key == ">>" {
				break
			}

			// read next token
			newKey, err := pr.readToken(r)
			if err != nil {
				return nil, fmt.Errorf("%w:Failed to read token", err)
			}

			value, err := pr.readValue(r, newKey)
			if err != nil {
				return nil, fmt.Errorf("%w:Failed to read value for token: "+newKey, err)
			}

			if value.Type == -1 {
				return result, nil
			}

			// Catch missing value
			if value.Type == PDFTypeToken && value.String == ">>" {
				result.Type = PDFTypeNull
				result.Dictionary[key] = value
				break
			}

			// Set value in dictionary
			result.Dictionary[key] = value
		}

		result.Type = PDFTypeDictionary
		return result, nil

	case "[":
		// This is an array

		tmpResult := make([]*PdfValue, 0)

		// Recurse into this function until we reach the end of the array
		for {
			key, err := pr.readToken(r)
			if err != nil {
				return nil, fmt.Errorf("%w:Failed to read token", err)
			}
			if key == "" {
				return nil, fmt.Errorf("Token is empty")
			}

			if key == "]" {
				break
			}

			value, err := pr.readValue(r, key)
			if err != nil {
				return nil, fmt.Errorf("%w:Failed to read value for token: "+key, err)
			}

			if value.Type == -1 {
				return result, nil
			}

			tmpResult = append(tmpResult, value)
		}

		result.Type = PDFTypeArray
		result.Array = tmpResult

	case "(":
		// This is a string

		openBrackets := 1

		// Create new buffer
		var buf bytes.Buffer

		// Read bytes until brackets are balanced
		for openBrackets > 0 {
			b, err := r.ReadByte()

			if err != nil {
				return nil, fmt.Errorf("%w:Failed to read byte", err)
			}

			switch b {
			case '(':
				openBrackets++

			case ')':
				openBrackets--

			case '\\':
				nb, err := r.ReadByte()
				if err != nil {
					return nil, fmt.Errorf("%w:Failed to read byte", err)
				}

				buf.WriteByte(b)
				buf.WriteByte(nb)

				continue
			}

			if openBrackets > 0 {
				buf.WriteByte(b)
			}
		}

		result.Type = PDFTypeString
		result.String = buf.String()

	case "stream":
		return nil, fmt.Errorf("Stream not implemented")

	default:
		result.Type = PDFTypeToken
		result.Token = t

		if isNumeric(t) {
			// A numeric token.  Make sure that it is not part of something else
			t2, err := pr.readToken(r)
			if err != nil {
				return nil, fmt.Errorf("%w:Failed to read token", err)
			}
			if t2 != "" {
				if isNumeric(t2) {
					// Two numeric tokens in a row.
					// In this case, we're probably in front of either an object reference
					// or an object specification.
					// Determine the case and return the data.
					t3, err := pr.readToken(r)
					if err != nil {
						return nil, fmt.Errorf("%w:Failed to read token", err)
					}

					if t3 != "" {
						switch t3 {
						case "obj":
							result.Type = PDFTypeObjDec
							result.ID, _ = strconv.Atoi(t)
							result.Gen, _ = strconv.Atoi(t2)
							return result, nil

						case "R":
							result.Type = PDFTypeObjRef
							result.ID, _ = strconv.Atoi(t)
							result.Gen, _ = strconv.Atoi(t2)
							return result, nil
						}

						// If we get to this point, that numeric value up there was just a numeric value.
						// Push the extra tokens back into the stack and return the value.
						pr.stack = append(pr.stack, t3)
					}
				}

				pr.stack = append(pr.stack, t2)
			}

			if n, err := strconv.Atoi(t); err == nil {
				result.Type = PDFTypeNumeric
				result.Int = n
				result.Real = float64(n) // Also assign Real value here to fix page box parsing bugs
			} else {
				result.Type = PDFTypeReal
				result.Real, _ = strconv.ParseFloat(t, 64)
			}
		} else if t == "true" || t == "false" {
			result.Type = PDFTypeBoolean
			result.Bool = t == "true"
		} else if t == "null" {
			result.Type = PDFTypeNull
		} else {
			result.Type = PDFTypeToken
			result.Token = t
		}
	}

	return result, nil
}

// Resolve a compressed object (PDF 1.5)
func (pr *PdfReader) resolveCompressedObject(objSpec *PdfValue) (*PdfValue, error) {
	var err error

	// Make sure object reference exists in xrefStream
	if _, ok := pr.xrefStream[objSpec.ID]; !ok {
		return nil, fmt.Errorf("could not find object ID %d in xref stream or xref table", objSpec.ID)
	}

	// Get object id and index
	objectID := pr.xrefStream[objSpec.ID][0]
	objectIndex := pr.xrefStream[objSpec.ID][1]

	// Read compressed object
	compressedObjSpec := &PdfValue{Type: PDFTypeObjRef, ID: objectID, Gen: 0}

	// Resolve compressed object
	compressedObj, err := pr.ResolveObject(compressedObjSpec)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to resolve compressed object", err)
	}

	// Verify object type is /ObjStm
	if _, ok := compressedObj.Value.Dictionary["/Type"]; ok {
		if compressedObj.Value.Dictionary["/Type"].Token != "/ObjStm" {
			return nil, fmt.Errorf("Expected compressed object type to be /ObjStm")
		}
	} else {
		return nil, fmt.Errorf("could not determine compressed object type")
	}

	// Get number of sub-objects in compressed object
	n := compressedObj.Value.Dictionary["/N"].Int
	if n <= 0 {
		return nil, fmt.Errorf("No sub objects in compressed object")
	}

	// Get offset of first object
	first := compressedObj.Value.Dictionary["/First"].Int

	// Get length
	//length := compressedObj.Value.Dictionary["/Length"].Int

	// Check for filter
	filter := ""
	if _, ok := compressedObj.Value.Dictionary["/Filter"]; ok {
		filter = compressedObj.Value.Dictionary["/Filter"].Token
		if filter != "/FlateDecode" {
			return nil, fmt.Errorf("Unsupported filter - expected /FlateDecode, got: " + filter)
		}
	}

	if filter == "/FlateDecode" {
		// Decompress if filter is /FlateDecode
		// Uncompress zlib compressed data
		var out bytes.Buffer
		zlibReader, _ := zlib.NewReader(bytes.NewBuffer(compressedObj.Stream.Bytes))
		defer zlibReader.Close()
		io.Copy(&out, zlibReader)

		// Set stream to uncompressed data
		compressedObj.Stream.Bytes = out.Bytes()
	}

	// Get io.Reader for bytes
	r := bufio.NewReader(bytes.NewBuffer(compressedObj.Stream.Bytes))

	subObjID := 0
	subObjPos := 0

	// Read sub-object indeces and their positions within the (un)compressed object
	for i := 0; i < n; i++ {
		var token string
		var _objidx int
		var _objpos int

		// Read first token (object index)
		token, err = pr.readToken(r)
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to read token", err)
		}

		// Convert line (string) into int
		_objidx, err = strconv.Atoi(token)
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to convert token into integer: "+token, err)
		}

		// Read first token (object index)
		token, err = pr.readToken(r)
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to read token", err)
		}

		// Convert line (string) into int
		_objpos, err = strconv.Atoi(token)
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to convert token into integer: "+token, err)
		}

		if i == objectIndex {
			subObjID = _objidx
			subObjPos = _objpos
		}
	}

	// Now create an io.ReadSeeker
	rs := io.ReadSeeker(bytes.NewReader(compressedObj.Stream.Bytes))

	// Determine where to seek to (sub-object position + /First)
	seekTo := int64(subObjPos + first)

	// Fast forward to the object
	rs.Seek(seekTo, 0)

	// Create a new io.Reader
	r = bufio.NewReader(rs)

	// Read token
	token, err := pr.readToken(r)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to read token", err)
	}

	// Read object
	obj, err := pr.readValue(r, token)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to read value for token: "+token, err)
	}

	result := &PdfValue{}
	result.ID = subObjID
	result.Gen = 0
	result.Type = PDFTypeObject
	result.Value = obj

	return result, nil
}

// ResolveObject returns the direct object referenced from objSpec or objSpec if it is not a reference to an object.
func (pr *PdfReader) ResolveObject(objSpec *PdfValue) (*PdfValue, error) {
	var err error
	var oldPos int64

	// Create new bufio.Reader
	r := bufio.NewReader(pr.f)

	if objSpec.Type != PDFTypeObjRef {
		return objSpec, nil
	}

	// This is a reference, resolve it.
	offset := pr.xref[objSpec.ID][objSpec.Gen]

	if _, ok := pr.xref[objSpec.ID]; !ok {
		// This may be a compressed object
		return pr.resolveCompressedObject(objSpec)
	}

	// Save current file position
	// This is needed if you want to resolve reference while you're reading another object.
	// (e.g.: if you need to determine the length of a stream)
	oldPos, err = pr.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to get current position of file", err)
	}

	// Reposition the file pointer and load the object header
	_, err = pr.f.Seek(int64(offset), 0)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to set position of file", err)
	}

	token, err := pr.readToken(r)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to read token", err)
	}

	obj, err := pr.readValue(r, token)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to read value for token: "+token, err)
	}

	if obj.Type != PDFTypeObjDec {
		return nil, fmt.Errorf("expected type to be PDF_TYPE_OBJDEC, got: %d", obj.Type)
	}

	if obj.ID != objSpec.ID {
		return nil, fmt.Errorf("object ID (%d) does not match ObjSpec ID (%d)", obj.ID, objSpec.ID)
	}

	if obj.Gen != objSpec.Gen {
		return nil, fmt.Errorf("Object Gen does not match ObjSpec Gen")
	}

	// Read next token
	token, err = pr.readToken(r)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to read token", err)
	}

	// Read actual object value
	value, err := pr.readValue(r, token)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to read value for token: "+token, err)
	}

	// Read next token
	token, err = pr.readToken(r)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to read token", err)
	}

	result := &PdfValue{}
	result.ID = obj.ID
	result.Gen = obj.Gen
	result.Type = PDFTypeObject
	result.Value = value

	if token == "stream" {
		result.Type = PDFTypeStream

		err = pr.skipWhitespace(r)
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to skip whitespace", err)
		}

		// Get stream length dictionary
		lengthDict := value.Dictionary["/Length"]

		// Get number of bytes of stream
		length := lengthDict.Int

		// If lengthDict is an object reference, resolve the object and set length
		if lengthDict.Type == PDFTypeObjRef {
			lengthDict, err = pr.ResolveObject(lengthDict)

			if err != nil {
				return nil, fmt.Errorf("%w:Failed to resolve length object of stream", err)
			}

			// Set length to resolved object value
			length = lengthDict.Value.Int
		}

		// Read length bytes
		bytes := make([]byte, length)

		// Cannot use reader.Read() because that may not read all the bytes
		_, err := io.ReadFull(r, bytes)
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to read bytes from buffer", err)
		}

		token, err = pr.readToken(r)
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to read token", err)
		}
		if token != "endstream" {
			return nil, fmt.Errorf("Expected next token to be: endstream, got: " + token)
		}

		token, err = pr.readToken(r)
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to read token", err)
		}

		streamObj := &PdfValue{}
		streamObj.Type = PDFTypeStream
		streamObj.Bytes = bytes

		result.Stream = streamObj
	}

	if token != "endobj" {
		return nil, fmt.Errorf("Expected next token to be: endobj, got: " + token)
	}

	// Reposition the file pointer to previous position
	_, err = pr.f.Seek(oldPos, 0)
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to set position of file", err)
	}

	return result, nil

}

// Find the xref offset (should be at the end of the PDF)
func (pr *PdfReader) findXref() error {
	var result int
	var err error
	var toRead int64

	toRead = 1500

	// If PDF is smaller than 1500 bytes, be sure to only read the number of bytes that are in the file
	fileSize := pr.nBytes
	if fileSize < toRead {
		toRead = fileSize
	}

	// Perform seek operation
	_, err = pr.f.Seek(-toRead, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("%w:Failed to set position of file", err)
	}

	// Create new bufio.Reader
	r := bufio.NewReader(pr.f)
	for {
		// Read all tokens until "startxref" is found
		token, err := pr.readToken(r)
		if err != nil {
			return fmt.Errorf("%w:Failed to read token", err)
		}
		if token == "startxref" {
			// Probably EOF before finding startxref
			if token, err = pr.readToken(r); err != nil {
				return fmt.Errorf("%w:Failed to find startxref token", err)
			}

			// Convert line (string) into int
			if result, err = strconv.Atoi(token); err != nil {
				return fmt.Errorf("%w:Failed to convert xref position into integer: %s", err, token)
			}

			// Successfully read the xref position
			pr.xrefPos = result
			break
		}
	}

	// Rewind file pointer
	if _, err = pr.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w:Failed to set position of file", err)
	}

	pr.xrefPos = result

	return nil
}

// Read and parse the xref table
func (pr *PdfReader) readXref() error {
	var err error

	// Create new bufio.Reader
	r := bufio.NewReader(pr.f)

	// Set file pointer to xref start
	_, err = pr.f.Seek(int64(pr.xrefPos), 0)
	if err != nil {
		return fmt.Errorf("%w:Failed to set position of file", err)
	}

	// Xref should start with 'xref'
	t, err := pr.readToken(r)
	if err != nil {
		return fmt.Errorf("%w:Failed to read token", err)
	}
	if t != "xref" {
		// Maybe this is an XRef stream ...
		v, err := pr.readValue(r, t)
		if err != nil {
			return fmt.Errorf("%w:Failed to read XRef stream", err)
		}

		if v.Type == PDFTypeObjDec {
			// Read next token
			t, err = pr.readToken(r)
			if err != nil {
				return fmt.Errorf("%w:Failed to read token", err)
			}

			// Read actual object value
			v, err := pr.readValue(r, t)
			if err != nil {
				return fmt.Errorf("%w:Failed to read value for token: %s", err, t)
			}

			// If /Type is set, check to see if it is XRef
			if _, ok := v.Dictionary["/Type"]; ok {
				if v.Dictionary["/Type"].Token == "/XRef" {
					// Continue reading xref stream data now that it is confirmed that it is an xref stream

					// Check for /DecodeParms
					paethDecode := false
					if _, ok := v.Dictionary["/DecodeParms"]; ok {
						columns := 0
						predictor := 0

						if _, ok2 := v.Dictionary["/DecodeParms"].Dictionary["/Columns"]; ok2 {
							columns = v.Dictionary["/DecodeParms"].Dictionary["/Columns"].Int
						}
						if _, ok2 := v.Dictionary["/DecodeParms"].Dictionary["/Predictor"]; ok2 {
							predictor = v.Dictionary["/DecodeParms"].Dictionary["/Predictor"].Int
						}

						if columns > 4 || predictor > 12 {
							return fmt.Errorf("Unsupported /DecodeParms - only tested with /Columns <= 4 and /Predictor <= 12")
						}
						paethDecode = true
					}

					/*
						// Check to make sure field size is [1 2 1] - not yet tested with other field sizes
						if v.Dictionary["/W"].Array[0].Int != 1 || v.Dictionary["/W"].Array[1].Int > 4 || v.Dictionary["/W"].Array[2].Int != 1 {
							return fmt.Errorf(fmt.Sprintf("Unsupported field sizes in cross-reference stream dictionary: /W [%d %d %d]",
								v.Dictionary["/W"].Array[0].Int,
								v.Dictionary["/W"].Array[1].Int,
								v.Dictionary["/W"].Array[2].Int))
						}
					*/

					index := make([]int, 2)

					// If /Index is not set, this is an error
					if _, ok := v.Dictionary["/Index"]; ok {
						if len(v.Dictionary["/Index"].Array) < 2 {
							return fmt.Errorf("%w:Index array does not contain 2 elements", err)
						}

						index[0] = v.Dictionary["/Index"].Array[0].Int
						index[1] = v.Dictionary["/Index"].Array[1].Int
					} else {
						index[0] = 0
					}

					prevXref := 0

					// Check for previous xref stream
					if _, ok := v.Dictionary["/Prev"]; ok {
						prevXref = v.Dictionary["/Prev"].Int
					}

					// Set root object
					if _, ok := v.Dictionary["/Root"]; ok {
						// Just set the whole dictionary with /Root key to keep compatibiltiy with existing code
						pr.trailer = v
					}
					// Don't return an error here.  The trailer could be in another XRef stream.

					startObject := index[0]

					err = pr.skipWhitespace(r)
					if err != nil {
						return fmt.Errorf("%w:Failed to skip whitespace", err)
					}

					// Get stream length dictionary
					lengthDict := v.Dictionary["/Length"]

					// Get number of bytes of stream
					length := lengthDict.Int

					// If lengthDict is an object reference, resolve the object and set length
					if lengthDict.Type == PDFTypeObjRef {
						lengthDict, err = pr.ResolveObject(lengthDict)

						if err != nil {
							return fmt.Errorf("%w:Failed to resolve length object of stream", err)
						}

						// Set length to resolved object value
						length = lengthDict.Value.Int
					}

					t, err = pr.readToken(r)
					if err != nil {
						return fmt.Errorf("%w:Failed to read token", err)
					}
					if t != "stream" {
						return fmt.Errorf("Expected next token to be: stream, got: " + t)
					}

					err = pr.skipWhitespace(r)
					if err != nil {
						return fmt.Errorf("%w:Failed to skip whitespace", err)
					}

					// Read length bytes
					data := make([]byte, length)

					// Cannot use reader.Read() because that may not read all the bytes
					_, err := io.ReadFull(r, data)
					if err != nil {
						return fmt.Errorf("%w:Failed to read bytes from buffer", err)
					}

					// Look for endstream token
					t, err = pr.readToken(r)
					if err != nil {
						return fmt.Errorf("%w:Failed to read token", err)
					}
					if t != "endstream" {
						return fmt.Errorf("Expected next token to be: endstream, got: " + t)
					}

					// Look for endobj token
					t, err = pr.readToken(r)
					if err != nil {
						return fmt.Errorf("%w:Failed to read token", err)
					}
					if t != "endobj" {
						return fmt.Errorf("Expected next token to be: endobj, got: " + t)
					}

					// Now decode zlib data
					b := bytes.NewReader(data)

					z, err := zlib.NewReader(b)
					if err != nil {
						return fmt.Errorf("%w:zlib.NewReader error", err)
					}
					defer z.Close()

					p, err := ioutil.ReadAll(z)
					if err != nil {
						return fmt.Errorf("%w:ioutil.ReadAll error", err)
					}

					objPos := 0
					objGen := 0
					i := startObject

					// Decode result with paeth algorithm
					var result []byte
					b = bytes.NewReader(p)

					firstFieldSize := v.Dictionary["/W"].Array[0].Int
					middleFieldSize := v.Dictionary["/W"].Array[1].Int
					lastFieldSize := v.Dictionary["/W"].Array[2].Int

					fieldSize := firstFieldSize + middleFieldSize + lastFieldSize
					if paethDecode {
						fieldSize++
					}

					prevRow := make([]byte, fieldSize)
					for {
						result = make([]byte, fieldSize)
						_, err := io.ReadFull(b, result)
						if err != nil {
							if err == io.EOF {
								break
							} else {
								return fmt.Errorf("%w:io.ReadFull error", err)
							}
						}

						if paethDecode {
							filterPaeth(result, prevRow, fieldSize)
							copy(prevRow, result)
						}

						objectData := make([]byte, fieldSize)
						if paethDecode {
							copy(objectData, result[1:fieldSize])
						} else {
							copy(objectData, result[0:fieldSize])
						}

						if objectData[0] == 1 {
							// Regular objects
							b := make([]byte, 4)
							copy(b[4-middleFieldSize:], objectData[1:1+middleFieldSize])

							objPos = int(binary.BigEndian.Uint32(b))
							objGen = int(objectData[firstFieldSize+middleFieldSize])

							// Append map[int]int
							pr.xref[i] = make(map[int]int, 1)

							// Set object id, generation, and position
							pr.xref[i][objGen] = objPos
						} else if objectData[0] == 2 {
							// Compressed objects
							b := make([]byte, 4)
							copy(b[4-middleFieldSize:], objectData[1:1+middleFieldSize])

							objID := int(binary.BigEndian.Uint32(b))
							objIdx := int(objectData[firstFieldSize+middleFieldSize])

							// object id (i) is located in StmObj (objId) at index (objIdx)
							pr.xrefStream[i] = [2]int{objID, objIdx}
						}

						i++
					}

					// Check for previous xref stream
					if prevXref > 0 {
						// Set xrefPos to /Prev xref
						pr.xrefPos = prevXref

						// Read preivous xref
						xrefErr := pr.readXref()
						if xrefErr != nil {
							return fmt.Errorf("%w: Failed to read prev xref", xrefErr)
						}
					}
				}
			}

			return nil
		}

		return fmt.Errorf("Expected xref to start with 'xref'.  Got: " + t)
	}

	for {
		// Next value will be the starting object id (usually 0, but not always) or the trailer
		t, err = pr.readToken(r)
		if err != nil {
			return fmt.Errorf("%w:Failed to read token", err)
		}

		// Check for trailer
		if t == "trailer" {
			break
		}

		// Convert token to int
		startObject, err := strconv.Atoi(t)
		if err != nil {
			return fmt.Errorf("%w:Failed to convert start object to integer: "+t, err)
		}

		// Determine how many objects there are
		t, err = pr.readToken(r)
		if err != nil {
			return fmt.Errorf("%w:Failed to read token", err)
		}

		// Convert token to int
		numObject, err := strconv.Atoi(t)
		if err != nil {
			return fmt.Errorf("%w:Failed to convert num object to integer: "+t, err)
		}

		// For all objects in xref, read object position, object generation, and status (free or new)
		for i := startObject; i < startObject+numObject; i++ {
			t, err = pr.readToken(r)
			if err != nil {
				return fmt.Errorf("%w:Failed to read token", err)
			}

			// Get object position as int
			objPos, err := strconv.Atoi(t)
			if err != nil {
				return fmt.Errorf("%w:Failed to convert object position to integer: "+t, err)
			}

			t, err = pr.readToken(r)
			if err != nil {
				return fmt.Errorf("%w:Failed to read token", err)
			}

			// Get object generation as int
			objGen, err := strconv.Atoi(t)
			if err != nil {
				return fmt.Errorf("%w:Failed to convert object generation to integer: "+t, err)
			}

			// Get object status (free or new)
			objStatus, err := pr.readToken(r)
			if err != nil {
				return fmt.Errorf("%w:Failed to read token", err)
			}
			if objStatus != "f" && objStatus != "n" {
				return fmt.Errorf("Expected objStatus to be 'n' or 'f', got: " + objStatus)
			}

			// Append map[int]int
			pr.xref[i] = make(map[int]int, 1)

			// Set object id, generation, and position
			pr.xref[i][objGen] = objPos
		}
	}

	// Read trailer dictionary
	t, err = pr.readToken(r)
	if err != nil {
		return fmt.Errorf("%w:Failed to read token", err)
	}

	trailer, err := pr.readValue(r, t)
	if err != nil {
		return fmt.Errorf("%w:Failed to read value for token: "+t, err)
	}

	// If /Root is set, then set trailer object so that /Root can be read later
	if _, ok := trailer.Dictionary["/Root"]; ok {
		pr.trailer = trailer
	}

	// If a /Prev xref trailer is specified, parse that
	if tr, ok := trailer.Dictionary["/Prev"]; ok {
		// Resolve parent xref table
		pr.xrefPos = tr.Int
		return pr.readXref()
	}

	return nil
}

// Read root (catalog object)
func (pr *PdfReader) readRoot() error {
	var err error

	rootObjSpec := pr.trailer.Dictionary["/Root"]

	// Read root (catalog)
	pr.catalog, err = pr.ResolveObject(rootObjSpec)
	if err != nil {
		return fmt.Errorf("%w:Failed to resolve root object", err)
	}

	return nil
}

// Read kids (pages inside a page tree)
func (pr *PdfReader) readKids(kids *PdfValue, r int) error {
	// Loop through pages and add to result
	for i := 0; i < len(kids.Array); i++ {
		page, err := pr.ResolveObject(kids.Array[i])
		if err != nil {
			return fmt.Errorf("%w:Failed to resolve page/pages object", err)
		}

		objType := page.Value.Dictionary["/Type"].Token
		if objType == "/Page" {
			// Set page and increment curPage
			pr.pages[pr.curPage] = page
			pr.curPage++
		} else if objType == "/Pages" {
			// Resolve kids
			subKids, err := pr.ResolveObject(page.Value.Dictionary["/Kids"])
			if err != nil {
				return fmt.Errorf("%w:Failed to resolve kids", err)
			}

			// Recurse into page tree
			if err = pr.readKids(subKids, r+1); err != nil {
				return fmt.Errorf("%w:Failed to read kids", err)
			}
		} else {
			return fmt.Errorf("%w: Unknown object type '%s'.  Expected: /Pages or /Page", err, objType)
		}
	}

	return nil
}

// Read all pages in PDF
func (pr *PdfReader) readPages() error {
	var err error

	// resolve_pages_dict
	pagesDict, err := pr.ResolveObject(pr.catalog.Value.Dictionary["/Pages"])
	if err != nil {
		return fmt.Errorf("%w:Failed to resolve pages object", err)
	}

	// This will normally return itself
	kids, err := pr.ResolveObject(pagesDict.Value.Dictionary["/Kids"])
	if err != nil {
		return fmt.Errorf("%w:Failed to resolve kids object", err)
	}

	// Get number of pages
	pageCount, err := pr.ResolveObject(pagesDict.Value.Dictionary["/Count"])
	if err != nil {
		return fmt.Errorf("%w:Failed to get page count", err)
	}
	pr.pageCount = pageCount.Int

	// Allocate pages
	pr.pages = make([]*PdfValue, pageCount.Int)

	// Read kids
	err = pr.readKids(kids, 0)
	if err != nil {
		return fmt.Errorf("%w:Failed to read kids", err)
	}

	return nil
}

// GetPageResources gets references to page resources for a given page number
func (pr *PdfReader) GetPageResources(pageno int) (*PdfValue, error) {
	var err error

	// Check to make sure page exists in pages slice
	if len(pr.pages) < pageno {
		return nil, fmt.Errorf("page %d does not exist", pageno)
	}

	// Resolve page object
	page, err := pr.ResolveObject(pr.pages[pageno-1])
	if err != nil {
		return nil, fmt.Errorf("%w:Failed to resolve page object", err)
	}

	// Check to see if /Resources exists in Dictionary
	if _, ok := page.Value.Dictionary["/Resources"]; ok {
		// Resolve /Resources object
		res, err := pr.ResolveObject(page.Value.Dictionary["/Resources"])
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to resolve resources object", err)
		}

		// If type is PDF_TYPE_OBJECT, return its Value
		if res.Type == PDFTypeObject {
			return res.Value, nil
		}

		// Otherwise, returned the resolved object
		return res, nil
	}

	// If /Resources does not exist, check to see if /Parent exists and return that
	if _, ok := page.Value.Dictionary["/Parent"]; ok {
		// Resolve parent object
		res, err := pr.ResolveObject(page.Value.Dictionary["/Parent"])
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to resolve parent object", err)
		}

		// If /Parent object type is PDF_TYPE_OBJECT, return its Value
		if res.Type == PDFTypeObject {
			return res.Value, nil
		}

		// Otherwise, return the resolved parent object
		return res, nil
	}

	// Return an empty PdfValue if we got here
	// TODO:  Improve error handling
	return &PdfValue{}, nil
}

// Get page content and return a slice of PdfValue objects
func (pr *PdfReader) getPageContent(objSpec *PdfValue) ([]*PdfValue, error) {
	var err error
	var content *PdfValue

	// Allocate slice
	contents := make([]*PdfValue, 0)

	if objSpec.Type == PDFTypeObjRef {
		// If objSpec is an object reference, resolve the object and append it to contents
		content, err = pr.ResolveObject(objSpec)
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to resolve object", err)
		}
		contents = append(contents, content)
	} else if objSpec.Type == PDFTypeArray {
		// If objSpec is an array, loop through the array and recursively get page content and append to contents
		for i := 0; i < len(objSpec.Array); i++ {
			tmpContents, err := pr.getPageContent(objSpec.Array[i])
			if err != nil {
				return nil, fmt.Errorf("%w:Failed to get page content", err)
			}
			for j := 0; j < len(tmpContents); j++ {
				contents = append(contents, tmpContents[j])
			}
		}
	}

	return contents, nil
}

// GetContent reaturns the stream for the given page (i.e. PDF drawing instructions)
func (pr *PdfReader) GetContent(pageno int) (string, error) {
	var err error
	var contents []*PdfValue

	// Check to make sure page exists in pages slice
	if len(pr.pages) < pageno {
		return "", fmt.Errorf("page %d does not exist", pageno)
	}

	// Get page
	page := pr.pages[pageno-1]

	// FIXME: This could be slow, converting []byte to string and appending many times
	buffer := ""

	// Check to make sure /Contents exists in page dictionary
	if _, ok := page.Value.Dictionary["/Contents"]; ok {
		// Get an array of page content
		contents, err = pr.getPageContent(page.Value.Dictionary["/Contents"])
		if err != nil {
			return "", fmt.Errorf("%w:Failed to get page content", err)
		}

		for i := 0; i < len(contents); i++ {
			// Decode content if one or more /Filter is specified.
			// Most common filter is FlateDecode which can be uncompressed with zlib
			tmpBuffer, err := pr.rebuildContentStream(contents[i])
			if err != nil {
				return "", fmt.Errorf("%w:Failed to rebuild content stream", err)
			}

			// FIXME:  This is probably slow
			buffer += string(tmpBuffer)
		}
	}

	return buffer, nil
}

// Rebuild content stream
// This will decode content if one or more /Filter (such as FlateDecode) is specified.
// If there are multiple filters, they will be decoded in the order in which they were specified.
func (pr *PdfReader) rebuildContentStream(content *PdfValue) ([]byte, error) {
	var err error
	var tmpFilter *PdfValue

	// Allocate slice of PdfValue
	filters := make([]*PdfValue, 0)

	// If content has a /Filter, append it to filters slice
	if _, ok := content.Value.Dictionary["/Filter"]; ok {
		filter := content.Value.Dictionary["/Filter"]

		// If filter type is a reference, resolve it
		if filter.Type == PDFTypeObjRef {
			tmpFilter, err = pr.ResolveObject(filter)
			if err != nil {
				return nil, fmt.Errorf("%w:Failed to resolve object", err)
			}
			filter = tmpFilter.Value
		}

		if filter.Type == PDFTypeToken {
			// If filter type is a token (e.g. FlateDecode), appent it to filters slice
			filters = append(filters, filter)
		} else if filter.Type == PDFTypeArray {
			// If filter type is an array, then there are multiple filters.  Set filters variable to array value.
			filters = filter.Array
		}

	}

	// Set stream variable to content bytes
	stream := content.Stream.Bytes

	// Loop through filters and apply each filter to stream
	for i := 0; i < len(filters); i++ {
		switch filters[i].Token {
		case "/FlateDecode":
			// Uncompress zlib compressed data
			var out bytes.Buffer
			zlibReader, _ := zlib.NewReader(bytes.NewBuffer(stream))
			defer zlibReader.Close()
			io.Copy(&out, zlibReader)

			// Set stream to uncompressed data
			stream = out.Bytes()
		default:
			return nil, fmt.Errorf("Unspported filter: " + filters[i].Token)
		}
	}

	return stream, nil
}

// GetNumPages returns the number of pages in the PDF file
func (pr *PdfReader) GetNumPages() (int, error) {
	if pr.pageCount == 0 {
		return 0, fmt.Errorf("Page count is 0")
	}

	return pr.pageCount, nil
}

// GetAllPageBoxes returns all pages boxes
func (pr *PdfReader) GetAllPageBoxes(k float64) (map[int]map[string]map[string]float64, error) {
	var err error

	// Allocate result with the number of available boxes
	result := make(map[int]map[string]map[string]float64, len(pr.pages))

	for i := 1; i <= len(pr.pages); i++ {
		result[i], err = pr.GetPageBoxes(i, k)
		if result[i] == nil {
			return nil, fmt.Errorf("%w:Unable to get page box", err)
		}
	}

	return result, nil
}

// GetPageBoxes gets all page box data
func (pr *PdfReader) GetPageBoxes(pageno int, k float64) (map[string]map[string]float64, error) {
	var err error

	// Allocate result with the number of available boxes
	result := make(map[string]map[string]float64, len(pr.availableBoxes))

	// Check to make sure page exists in pages slice
	if len(pr.pages) < pageno {
		return nil, fmt.Errorf("page %d does not exist", pageno)
	}

	// Resolve page object
	page, err := pr.ResolveObject(pr.pages[pageno-1])
	if err != nil {
		return nil, fmt.Errorf("failed to resolve page object")
	}

	// Loop through available boxes and add to result
	for i := 0; i < len(pr.availableBoxes); i++ {
		box, err := pr.getPageBox(page, pr.availableBoxes[i], k)
		if err != nil {
			return nil, fmt.Errorf("failed to get page box")
		}
		result[pr.availableBoxes[i]] = box
	}

	return result, nil
}

// Get a specific page box value (e.g. MediaBox) and return its values
func (pr *PdfReader) getPageBox(page *PdfValue, boxIndex string, k float64) (map[string]float64, error) {
	var err error
	var tmpBox *PdfValue

	// Allocate 8 fields in result
	result := make(map[string]float64, 8)

	// Check to make sure box_index (e.g. MediaBox) exists in page dictionary
	if _, ok := page.Value.Dictionary[boxIndex]; ok {
		box := page.Value.Dictionary[boxIndex]

		// If the box type is a reference, resolve it
		if box.Type == PDFTypeObjRef {
			tmpBox, err = pr.ResolveObject(box)
			if err != nil {
				return nil, fmt.Errorf("Failed to resolve object")
			}
			box = tmpBox.Value
		}

		if box.Type == PDFTypeArray {
			// If the box type is an array, calculate scaled value based on k
			result["x"] = box.Array[0].Real / k
			result["y"] = box.Array[1].Real / k
			result["w"] = math.Abs(box.Array[0].Real-box.Array[2].Real) / k
			result["h"] = math.Abs(box.Array[1].Real-box.Array[3].Real) / k
			result["llx"] = math.Min(box.Array[0].Real, box.Array[2].Real)
			result["lly"] = math.Min(box.Array[1].Real, box.Array[3].Real)
			result["urx"] = math.Max(box.Array[0].Real, box.Array[2].Real)
			result["ury"] = math.Max(box.Array[1].Real, box.Array[3].Real)
		} else {
			// TODO: Improve error handling
			return nil, fmt.Errorf("Could not get page box")
		}
	} else if _, ok := page.Value.Dictionary["/Parent"]; ok {
		parentObj, err := pr.ResolveObject(page.Value.Dictionary["/Parent"])
		if err != nil {
			return nil, fmt.Errorf("%w:Could not resolve parent object", err)
		}

		// If the page box is inherited from /Parent, recursively return page box of parent
		return pr.getPageBox(parentObj, boxIndex, k)
	}

	return result, nil
}

// GetPageRotation returns the page rotation for a page number
func (pr *PdfReader) GetPageRotation(pageno int) (*PdfValue, error) {
	// Check to make sure page exists in pages slice
	if len(pr.pages) < pageno {
		return nil, fmt.Errorf("page %d does not exist", pageno)
	}

	return pr._getPageRotation(pr.pages[pageno-1])
}

// Get page rotation for a page object spec
func (pr *PdfReader) _getPageRotation(page *PdfValue) (*PdfValue, error) {
	var err error

	// Resolve page object
	page, err = pr.ResolveObject(page)
	if err != nil {
		return nil, fmt.Errorf("Failed to resolve page object")
	}

	// Check to make sure /Rotate exists in page dictionary
	if _, ok := page.Value.Dictionary["/Rotate"]; ok {
		res, err := pr.ResolveObject(page.Value.Dictionary["/Rotate"])
		if err != nil {
			return nil, fmt.Errorf("Failed to resolve rotate object")
		}

		// If the type is PDF_TYPE_OBJECT, return its value
		if res.Type == PDFTypeObject {
			return res.Value, nil
		}

		// Otherwise, return the object
		return res, nil
	}
	// Check to see if parent has a rotation
	if _, ok := page.Value.Dictionary["/Parent"]; ok {
		// Recursively return /Parent page rotation
		res, err := pr._getPageRotation(page.Value.Dictionary["/Parent"])
		if err != nil {
			return nil, fmt.Errorf("%w:Failed to get page rotation for parent", err)
		}

		// If the type is PDF_TYPE_OBJECT, return its value
		if res.Type == PDFTypeObject {
			return res.Value, nil
		}

		// Otherwise, return the object
		return res, nil
	}

	return &PdfValue{Int: 0}, nil
}

func (pr *PdfReader) read() error {
	// Only run once
	if !pr.alreadyRead {
		var err error

		// Find xref position
		err = pr.findXref()
		if err != nil {
			return fmt.Errorf("%w:Failed to find xref position", err)
		}

		// Parse xref table
		err = pr.readXref()
		if err != nil {
			return fmt.Errorf("%w:Failed to read xref table", err)
		}

		// Read catalog
		err = pr.readRoot()
		if err != nil {
			return fmt.Errorf("%w:Failed to read root", err)
		}

		// Read pages
		err = pr.readPages()
		if err != nil {
			return fmt.Errorf("%w:Failed to to read pages", err)
		}

		// Now that this has been read, do not read again
		pr.alreadyRead = true
	}

	return nil
}
