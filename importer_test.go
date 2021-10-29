package gofpdi

import (
	"os"
	"testing"
)

func TestImportSimple(t *testing.T) {
	r, err := os.Open("reader/testdata/cow.pdf")
	if err != nil {
		t.Error(err)
	}
	imp := NewImporter()

	imp.SetSourceStream(r)

	_, err = imp.ImportPage(1, "/MediaBox")
	if err != nil {
		t.Error(err)
	}

	_, err = imp.PutFormXobjects()
	if err != nil {
		t.Error(err)
	}
	imp.GetImportedObjects()

}
