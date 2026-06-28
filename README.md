# gofpdi: Go Free PDF Document Importer

[![Explore in Constellation](https://img.shields.io/badge/Explore%20in-Constellation-blue)](https://constellation.speedata.de)
[![MIT licensed](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Reference](https://img.shields.io/badge/go-reference-00ADD8.svg)](https://pkg.go.dev/github.com/boxesandglue/gofpdi)
[![Go Report Card](https://goreportcard.com/badge/github.com/boxesandglue/gofpdi)](https://goreportcard.com/report/github.com/boxesandglue/gofpdi)

**gofpdi** lets you import pages from existing PDF files and re-use them as [Form XObjects](https://en.wikipedia.org/wiki/PDF#Content_streams) in your own PDFs.
This fork is maintained by [boxes and Glue](https://github.com/boxesandglue) and used internally by [baseline-pdf](https://github.com/boxesandglue/baseline-pdf).

---

## Overview

This package provides the low-level logic to:

- Parse an existing PDF through the [speedata/pdfdisassembler](https://github.com/speedata/pdfdisassembler) reader (classical cross-reference tables and cross-reference streams, object streams, and the full set of standard stream filters)
- Import a page, choosing a box (`/MediaBox`, `/CropBox`, …) and normalizing the page rotation
- Emit that page as a **Form XObject**, copying every referenced object (fonts, images, graphics states, …) verbatim so stream data is never re-encoded
- Retrieve the resulting serialized object bodies to embed in a higher-level PDF writer

The code was originally written by **phpdave11** and contributors, based on the PHP library [Setasign/FPDI (legacy 1.6.x)](https://github.com/Setasign/FPDI/tree/1.6.x-legacy).
This fork replaces the original hand-rolled PDF parser with pdfdisassembler, adds deterministic output, and offers a cleaner importer interface.
Thanks to the original authors: this fork would not exist without their work.

---

## Usage

The public entry point is the `Importer`. The typical lifecycle is:

1. `NewImporter`, then install your host writer's object-number allocator with `SetObjIDGetter` (or seed the internal counter with `SetNextObjectID`).
2. `SetSourceStream` with the source PDF.
3. `ImportPage` for each page you want to embed (page numbers are **1-based**).
4. `PutFormXobjects` to serialize the XObjects and every object they reference, then `GetImportedObjects` to retrieve the bytes.

```go
package main

import (
    "fmt"
    "os"

    "github.com/boxesandglue/gofpdi"
)

func main() {
    f, err := os.Open("input.pdf")
    if err != nil {
        panic(err)
    }
    defer f.Close()

    imp := gofpdi.NewImporter()
    // Number the produced objects sequentially starting at 1. A host writer
    // would instead call imp.SetObjIDGetter with its own object allocator.
    imp.SetNextObjectID(1)

    if err := imp.SetSourceStream(f); err != nil {
        panic(err)
    }

    // Import the first page (1-based) using its /MediaBox.
    if _, err := imp.ImportPage(1, "/MediaBox"); err != nil {
        panic(err)
    }

    // Serialize each imported page as a Form XObject, plus the objects it
    // references. The map is template name -> assigned output object number.
    names, err := imp.PutFormXobjects()
    if err != nil {
        panic(err)
    }
    fmt.Println("template name -> object number:", names)

    // The serialized object bodies, keyed by output object number. They carry
    // no "N 0 obj"/"endobj" wrapper; the host writer adds that when assembling
    // the file.
    for objNum, body := range imp.GetImportedObjects() {
        fmt.Printf("object %d: %d bytes\n", objNum, len(body))
    }
}
```

`baseline-pdf` integrates this logic to embed external PDFs seamlessly in its own documents.

---

## Notes and limitations

- **Page numbers are 1-based** in the public API (page 1 is the first page).
- **Stream data is copied verbatim** in its original, filter-encoded form; image and font streams are never decoded and re-encoded.
- **Encrypted source PDFs are not supported.** Page import is rejected for them, because copying still-encrypted stream bytes into an unencrypted output would produce garbage.
- Extra Form XObject dictionary entries (for example `/StructParent` for PDF/UA structure attachment) can be injected with `SetTemplateDictEntry`.

---

## Ecosystem

gofpdi is part of a broader ecosystem of PDF, typesetting and publishing technologies.
**[Explore the constellation →](https://constellation.speedata.de)**


## License & Credits

Licensed under the **MIT License**.
Original code © [phpdave11/gofpdi](https://github.com/phpdave11/gofpdi) and contributors, based on [FPDI](https://github.com/Setasign/FPDI).
Fork maintained by **boxes and Glue** for use in **baseline-pdf** and related tools.
