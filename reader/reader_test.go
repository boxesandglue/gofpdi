package reader

import (
	"os"
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
