package canvas

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"fmt"
	"io"
	"strings"
)

type PDFWriter struct {
	w   io.Writer
	err error

	width, height float64
	pos           int
	objOffsets    []int

	resources      PDFDict
	graphicsStates map[float64]PDFName
	fonts          map[*Font]PDFName
}

func NewPDFWriter(writer io.Writer, width, height float64) *PDFWriter {
	w := &PDFWriter{
		w:              writer,
		width:          width,
		height:         height,
		resources:      PDFDict{"ExtGState": PDFDict{}, "Font": PDFDict{}},
		graphicsStates: map[float64]PDFName{},
		fonts:          map[*Font]PDFName{},
	}

	w.write("%%PDF-1.7\n")
	return w
}

func (w *PDFWriter) writeBytes(b []byte) {
	if w.err != nil {
		return
	}
	n, err := w.w.Write(b)
	w.pos += n
	w.err = err
}

func (w *PDFWriter) write(s string, v ...interface{}) {
	if w.err != nil {
		return
	}
	n, err := fmt.Fprintf(w.w, s, v...)
	w.pos += n
	w.err = err
}

type PDFRef int
type PDFName string
type PDFArray []interface{}
type PDFDict map[PDFName]interface{}
type PDFFilter string
type PDFStream struct {
	dict    PDFDict
	filters []PDFFilter
	b       []byte
}

const (
	PDFFilterASCII85 PDFFilter = "ASCII85Decode"
	PDFFilterFlate   PDFFilter = "FlateDecode"
)

func (w *PDFWriter) writeVal(i interface{}) {
	switch v := i.(type) {
	case int, float64:
		w.write("%v", v)
	case string:
		v = strings.Replace(v, `\`, `\\`, -1)
		v = strings.Replace(v, `(`, `\(`, -1)
		v = strings.Replace(v, `)`, `\)`, -1)
		w.write("(%v)", v)
	case PDFRef:
		w.write("%v 0 R", v)
	case PDFName:
		w.write("/%v", v)
	case PDFArray:
		w.write("[")
		for j, val := range v {
			if j != 0 {
				w.write(" ")
			}
			w.writeVal(val)
		}
		w.write("]")
	case PDFDict:
		w.write("<< ")
		for key, val := range v {
			w.writeVal(key)
			w.write(" ")
			w.writeVal(val)
			w.write(" ")
		}
		w.write(">>")
	case PDFStream:
		filters := PDFArray{}
		for j := len(v.filters) - 1; j >= 0; j-- {
			filters = append(filters, PDFName(v.filters[j]))
		}

		b := v.b
		for _, filter := range v.filters {
			var b2 bytes.Buffer
			switch filter {
			case PDFFilterASCII85:
				w := ascii85.NewEncoder(&b2)
				w.Write(b)
				w.Close()
			case PDFFilterFlate:
				w := zlib.NewWriter(&b2)
				w.Write(b)
				w.Close()
			}
			b = b2.Bytes()
		}

		dict := v.dict
		if dict == nil {
			dict = PDFDict{}
		}
		if len(filters) == 1 {
			dict["Filter"] = filters[0]
		} else if len(filters) > 1 {
			dict["Filter"] = filters
		}
		dict["Length"] = len(b)
		w.writeVal(dict)
		w.write("\nstream\n")
		w.writeBytes(b)
		w.write("\nendstream")
	}
}

func (w *PDFWriter) GetOpacityGS(a float64) PDFName {
	if name, ok := w.graphicsStates[a]; ok {
		return name
	}
	name := PDFName(fmt.Sprintf("GS%d", len(w.graphicsStates)))
	w.graphicsStates[a] = name
	w.resources["ExtGState"].(PDFDict)[name] = PDFDict{
		"ca": a,
	}
	return name
}

func (w *PDFWriter) GetFont(f *Font) PDFName {
	if name, ok := w.fonts[f]; ok {
		return name
	}

	mimetype, _ := f.Raw()
	if mimetype != "font/ttf" {
		panic("only TTF format support for embedding fonts in PDFs")
	}

	// TODO: implement
	baseFont := strings.ReplaceAll(f.name, " ", "_")
	ref := w.WriteObject(PDFStream{
		dict: PDFDict{
			"Type":     PDFName("Font"),
			"Subtype":  PDFName("TrueType"),
			"BaseFont": PDFName(baseFont),
		},
	})

	name := PDFName(fmt.Sprintf("F%d", len(w.fonts)))
	w.fonts[f] = name
	w.resources["Font"].(PDFDict)[name] = ref
	return name
}

func (w *PDFWriter) WriteObject(val interface{}) PDFRef {
	w.objOffsets = append(w.objOffsets, w.pos)
	w.write("%v 0 obj\n", len(w.objOffsets))
	w.writeVal(val)
	w.write("\nendobj\n")
	return PDFRef(len(w.objOffsets))
}

func (w *PDFWriter) Close() error {
	contents := PDFArray{}
	for j := 0; j < len(w.objOffsets); j++ {
		contents = append(contents, PDFRef(j+1))
	}

	refPage := w.WriteObject(PDFDict{
		"Type":      PDFName("Page"),
		"Parent":    PDFRef(len(w.objOffsets) + 2),
		"MediaBox":  PDFArray{0.0, 0.0, w.width, w.height},
		"Resources": w.resources,
		"Contents":  contents,
	})

	refPages := w.WriteObject(PDFDict{
		"Type":  PDFName("Pages"),
		"Kids":  PDFArray{refPage},
		"Count": 1,
	})

	refCatalog := w.WriteObject(PDFDict{
		"Type":  PDFName("Catalog"),
		"Pages": refPages,
	})

	xrefOffset := w.pos
	w.write("xref\n0 %d\n0000000000 65535 f\n", len(w.objOffsets)+1)
	for _, objOffset := range w.objOffsets {
		w.write("%010d 00000 n\n", objOffset)
	}
	w.write("trailer\n")
	w.writeVal(PDFDict{
		"Root": refCatalog,
		"Size": len(w.objOffsets),
	})
	w.write("\nstarxref\n%v\n%%%%EOF", xrefOffset)
	return w.err
}
