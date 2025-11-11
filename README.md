# gofpdi — Go Free PDF Document Importer

[![MIT licensed](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Reference](https://img.shields.io/badge/go-reference-00ADD8.svg)](https://pkg.go.dev/github.com/boxesandglue/gofpdi)
[![Go Report Card](https://goreportcard.com/badge/github.com/boxesandglue/gofpdi)](https://goreportcard.com/report/github.com/boxesandglue/gofpdi)

**gofpdi** lets you import pages from existing PDF files and re-use them as [Form XObjects](https://en.wikipedia.org/wiki/PDF#Content_streams) in your own PDFs.
This fork is maintained by [boxes and Glue](https://github.com/boxesandglue) and used internally by [baseline-pdf](https://github.com/boxesandglue/baseline-pdf).

---

## Overview

This package provides the low-level logic to:

- Read and parse existing PDFs (`reader.PdfReader`)
- Import a page and normalize its boxes and rotation
- Emit that page as a **Form XObject** through `PdfWriter`
- Retrieve the resulting serialized objects to embed in a higher-level PDF writer

The code was originally written by **phpdave11** and contributors, based on the excellent PHP library [Setasign/FPDI (legacy 1.6.x)](https://github.com/Setasign/FPDI/tree/1.6.x-legacy).
This fork adds better documentation, deterministic output, and a cleaner writer interface while staying close to the upstream logic.
Huge thanks to the original authors — this fork would not exist without their work.

---

## Example

```go
package main

import (
    "fmt"
    "os"

    "github.com/boxesandglue/gofpdi"
    "github.com/boxesandglue/gofpdi/reader"
)

func main() {
    rd, err := reader.NewPdfReaderFromFile("input.pdf")
    if err != nil {
        panic(err)
    }

    pw := gofpdi.NewPdfWriter()
    _, err = pw.ImportPage(rd, 1, "/MediaBox")
    if err != nil {
        panic(err)
    }

    xobjects, err := pw.PutFormXobjects(rd)
    if err != nil {
        panic(err)
    }
    fmt.Println("Form names -> object IDs:", xobjects)

    objs := pw.GetImportedObjects()
    for key, body := range objs {
        _ = os.WriteFile(fmt.Sprintf("obj_%d_%s.bin", key.ID, key.Hash), body, 0o644)
    }
}
```

This snippet shows how to import a page and turn it into a reusable Form XObject.
`baseline-pdf` integrates this logic to embed external PDFs seamlessly in its own documents.

---

## License & Credits

Licensed under the **MIT License**.
Original code © [phpdave11/gofpdi](https://github.com/phpdave11/gofpdi) and contributors, based on [FPDI](https://github.com/Setasign/FPDI).
Fork maintained by **boxes and Glue** for use in **baseline-pdf** and related tools.
