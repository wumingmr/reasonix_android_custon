package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

type pageDimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}
type exportPDFWriter struct {
	dst    io.Writer
	offset int64
	err    error
}

func (w *exportPDFWriter) write(value string) {
	if w.err != nil {
		return
	}
	var n int
	n, w.err = io.WriteString(w.dst, value)
	w.offset += int64(n)
}
func (w *exportPDFWriter) object(id int, body string, offsets []int64) {
	offsets[id] = w.offset
	w.write(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", id, body))
}
func writeExportPDF(ctx context.Context, dst io.Writer, dir string, count int, title string) error {
	if count == 0 {
		return errors.New("no export pages")
	}
	w := &exportPDFWriter{dst: dst}
	offsets := make([]int64, 4+count*3)
	w.write("%PDF-1.4\n%\xff\xff\xff\xff\n")
	w.object(1, "<< /Type /Catalog /Pages 2 0 R >>", offsets)
	var kids strings.Builder
	for i := range count {
		if err := ctx.Err(); err != nil {
			return err
		}
		pageID := 3 + i*3
		fmt.Fprintf(&kids, "%d 0 R ", pageID)
		path := filepath.Join(dir, fmt.Sprintf("page-%06d", i))
		meta, err := os.ReadFile(path + ".json")
		if err != nil {
			return err
		}
		var dimensions pageDimensions
		if err = json.Unmarshal(meta, &dimensions); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		height := min(769.89, float64(dimensions.Height)*523.28/float64(dimensions.Width))
		y := 841.89 - 36 - height
		w.object(pageID, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595.28 841.89] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>", pageID+2, pageID+1), offsets)
		commands := fmt.Sprintf("q\n523.28 0 0 %.3f 36 %.3f cm\n/Im0 Do\nQ\n", height, y)
		w.object(pageID+1, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(commands), commands), offsets)
		offsets[pageID+2] = w.offset
		w.write(fmt.Sprintf("%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n", pageID+2, dimensions.Width, dimensions.Height, info.Size()))
		if w.err != nil {
			file.Close()
			return w.err
		}
		n, err := copyExportContext(ctx, dst, file)
		file.Close()
		if err != nil {
			return err
		}
		w.offset += n
		w.write("\nendstream\nendobj\n")
	}
	w.object(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), count), offsets)
	// UTF-16BE PDF strings preserve non-ASCII titles.
	encoded := utf16.Encode([]rune(title))
	var hexTitle strings.Builder
	hexTitle.WriteString("FEFF")
	for _, unit := range encoded {
		fmt.Fprintf(&hexTitle, "%04X", unit)
	}
	w.object(3+count*3, "<< /Title <"+hexTitle.String()+"> /Producer (Reasonix) >>", offsets)
	xref := w.offset
	w.write(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(offsets)))
	for _, offset := range offsets[1:] {
		w.write(fmt.Sprintf("%010d 00000 n \n", offset))
	}
	w.write(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), 3+count*3, xref))
	return w.err
}

func publishExportImages(ctx context.Context, dir, path string, count int) ([]string, error) {
	if count == 0 {
		return nil, errors.New("no export pages")
	}
	targets := make([]string, count)
	for i := range targets {
		targets[i] = numberedExportPath(path, i, count)
	}
	if count == 1 {
		err := writeStreamingExport(path, func(dst io.Writer) error {
			src, err := os.Open(filepath.Join(dir, "page-000000"))
			if err != nil {
				return err
			}
			defer src.Close()
			_, err = copyExportContext(ctx, dst, src)
			return err
		})
		return targets, err
	}
	err := publishJournaledImages(ctx, dir, targets)
	return targets, err
}

func numberedExportPath(path string, partIndex, partCount int) string {
	if partCount <= 1 {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	return fmt.Sprintf("%s-%d-of-%d%s", stem, partIndex+1, partCount, ext)
}
