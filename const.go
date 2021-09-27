package gofpdi

const (
	// PDFTypeNull means “no type”
	PDFTypeNull = iota
	// PDFTypeNumeric is a numeric type
	PDFTypeNumeric
	// PDFTypeToken is a name or something similar
	PDFTypeToken
	// PDFTypeHex is a hexadecimal encoded string such as <0012>
	PDFTypeHex
	// PDFTypeString is a string in parenthesis
	PDFTypeString
	// PDFTypeDictionary represents a Dictionary in double angle brackets << ... >>. The keys are strings and the values are PDF values
	PDFTypeDictionary
	// PDFTypeArray is an array [ ... ]
	PDFTypeArray
	// PDFTypeObjDec is a decimal (integer) object
	PDFTypeObjDec
	// PDFTypeObjRef is an indirect reference to an object such as 1 0 R
	PDFTypeObjRef
	// PDFTypeObject ...
	PDFTypeObject
	// PDFTypeStream is a slice of bytes representing a PDF stream
	PDFTypeStream
	// PDFTypeBoolean is either true or false
	PDFTypeBoolean
	// PDFTypeReal is a numeric with decimal places
	PDFTypeReal
)
