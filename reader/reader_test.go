package reader

import (
	"os"
	"strings"
	"testing"
)

func TestCow(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Error(err)
	}
	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Error(err)
	}

	np, err := pr.GetNumPages()
	if err != nil {
		t.Error(err)
	}
	if expected, got := 1, np; expected != got {
		t.Errorf("pr.GetNumPages() = %d, want %d", got, expected)
	}
}

func TestSample(t *testing.T) {
	r, err := os.Open("testdata/sample.pdf")
	if err != nil {
		t.Error(err)
	}
	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Error(err)
	}

	np, err := pr.GetNumPages()
	if err != nil {
		t.Error(err)
	}
	if expected, got := 2, np; expected != got {
		t.Errorf("pr.GetNumPages() = %d, want %d", got, expected)
	}
	pb, err := pr.GetAllPageBoxes(1)
	if err != nil {
		t.Error(err)
	}
	if expected, got := 2, len(pb); expected != got {
		t.Errorf("pr.GetAllPageBoxes() = %d, want %d", got, expected)
	}
}

func TestPrevXRef(t *testing.T) {
	r, err := os.Open("testdata/oceancrop.pdf")
	if err != nil {
		t.Error(err)
	}
	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Error(err)
	}

	pb, err := pr.GetAllPageBoxes(1)
	if err != nil {
		t.Error(err)
	}
	x := pb[1]["/CropBox"]["x"]

	if expected, got := 425.147, x; expected != got {
		t.Errorf("pr.GetAllPageBoxes(1)[1][CropBox] = %f, want %f", got, expected)
	}
}

func TestGetPageResources_Cow(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	res, err := pr.GetPageResources(1)
	if err != nil {
		t.Fatalf("GetPageResources(1) returned error: %v", err)
	}
	if res == nil {
		t.Fatalf("GetPageResources(1) = nil, want non-nil")
	}
	if res.Type != PDFTypeDictionary {
		t.Fatalf("GetPageResources(1).Type = %d, want PDFTypeDictionary", res.Type)
	}

	// Sanity: if common resource keys exist, they should be dictionaries or object refs.
	// These checks are optional-cautious: they only assert the type if the key is present.
	for _, k := range []string{"/Font", "/XObject", "/ExtGState", "/ColorSpace", "/Pattern", "/Shading", "/Properties"} {
		if v, ok := res.Dictionary[k]; ok {
			if v.Type != PDFTypeDictionary && v.Type != PDFTypeObjRef && v.Type != PDFTypeObject {
				t.Errorf("resources[%s].Type = %d, want dictionary or ref/object", k, v.Type)
			}
		}
	}
}

func TestGetPageResources_Sample_Pages(t *testing.T) {
	r, err := os.Open("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	// Check both pages; this also exercises resource inheritance if present.
	for _, pageno := range []int{1, 2} {
		res, err := pr.GetPageResources(pageno)
		if err != nil {
			t.Fatalf("GetPageResources(%d) returned error: %v", pageno, err)
		}
		if res == nil {
			t.Fatalf("GetPageResources(%d) = nil, want non-nil", pageno)
		}
		if res.Type != PDFTypeDictionary {
			t.Fatalf("GetPageResources(%d).Type = %d, want PDFTypeDictionary", pageno, res.Type)
		}
		// If there is a /ProcSet entry, it must be an array per spec.
		if ps, ok := res.Dictionary["/ProcSet"]; ok {
			if ps.Type != PDFTypeArray {
				t.Errorf("resources/ProcSet.Type = %d, want PDFTypeArray", ps.Type)
			}
		}
	}
}

func TestGetPageResources_OutOfRange(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	// Document has 1 page; asking for page 2 should be an error.
	if _, err := pr.GetPageResources(2); err == nil {
		t.Fatalf("GetPageResources(2) = nil error, want error for out-of-range page")
	}
}

func TestGetContent_Cow_NotEmpty(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	s, err := pr.GetContent(1)
	if err != nil {
		t.Fatalf("GetContent(1) returned error: %v", err)
	}
	if len(s) == 0 {
		t.Fatalf("GetContent(1) is empty, want non-empty content")
	}

	// Expect at least one common PDF operator in the content.
	ops := []string{"BT", "ET", "Do", "cm", "q", "Q", "Tj", "TJ"}
	hasOp := false
	for _, op := range ops {
		if strings.Contains(s, op) {
			hasOp = true
			break
		}
	}
	if !hasOp {
		// Print a prefix to help debugging without flooding logs
		if len(s) > 200 {
			t.Logf("content prefix: %q", s[:200])
		} else {
			t.Logf("content: %q", s)
		}
		t.Errorf("GetContent(1) did not contain any common PDF operators (%v)", ops)
	}
}

func TestGetContent_Sample_Pages(t *testing.T) {
	r, err := os.Open("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	for _, pageno := range []int{1, 2} {
		s, err := pr.GetContent(pageno)
		if err != nil {
			t.Fatalf("GetContent(%d) returned error: %v", pageno, err)
		}
		if len(s) == 0 {
			t.Errorf("GetContent(%d) returned empty string, want some operators", pageno)
		}
	}
}

func TestGetContent_OutOfRange(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pr.GetContent(2); err == nil {
		t.Fatalf("GetContent(2) = nil error, want error for out-of-range page")
	}
}

func TestGetPageRotation_Cow(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	rot, err := pr.GetPageRotation(1)
	if err != nil {
		t.Fatalf("GetPageRotation(1) returned error: %v", err)
	}
	// Expect a sensible rotation in standard multiples of 90.
	val := rot.Int
	if val != 0 && val != 90 && val != 180 && val != 270 && val != -90 && val != -180 && val != -270 {
		t.Errorf("GetPageRotation(1) = %d, want one of {0, 90, 180, 270} (allowing negatives)", val)
	}
}

func TestGetPageRotation_Sample_Pages(t *testing.T) {
	r, err := os.Open("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	for _, pageno := range []int{1, 2} {
		rot, err := pr.GetPageRotation(pageno)
		if err != nil {
			t.Fatalf("GetPageRotation(%d) returned error: %v", pageno, err)
		}
		val := rot.Int
		if val != 0 && val != 90 && val != 180 && val != 270 && val != -90 && val != -180 && val != -270 {
			t.Errorf("GetPageRotation(%d) = %d, want one of {0, 90, 180, 270} (allowing negatives)", pageno, val)
		}
	}
}

func TestResolveObject_Nil(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	// ResolveObject(nil) must return an error, not panic.
	_, err = pr.ResolveObject(nil)
	if err == nil {
		t.Fatal("ResolveObject(nil) = nil error, want error")
	}
}

func TestGetPageRotation_OutOfRange(t *testing.T) {
	r, err := os.Open("testdata/cow.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pr, err := NewPdfReaderFromStream(r)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pr.GetPageRotation(2); err == nil {
		t.Fatalf("GetPageRotation(2) = nil error, want error for out-of-range page")
	}
}
