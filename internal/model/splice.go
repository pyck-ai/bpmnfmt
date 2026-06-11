package model

import (
	"bytes"
	"fmt"
)

// SpliceDI returns a copy of the file's raw bytes with the BPMNDiagram element
// replaced by block. Everything outside the diagram element is preserved
// byte-for-byte. When the file has no diagram, block is inserted directly
// before the closing definitions tag (followed by the indentation that
// preceded it).
//
// block must be the full element starting with "<bpmndi:..." and ending with
// the closing tag, without leading or trailing whitespace.
func (f *File) SpliceDI(block []byte) ([]byte, error) {
	switch len(f.DiagramSpans) {
	case 0:
		if f.DefsEndOffset < 0 {
			return nil, fmt.Errorf("%s: no </definitions> end tag found", f.Path)
		}
		at := f.DefsEndOffset
		var out bytes.Buffer
		out.Grow(len(f.Raw) + len(block) + 8)
		out.Write(f.Raw[:at])
		// The closing tag sits at column 0; indent the inserted block one level.
		out.WriteString("  ")
		out.Write(block)
		out.WriteString("\n")
		out.Write(f.Raw[at:])
		return out.Bytes(), nil
	case 1:
		sp := f.DiagramSpans[0]
		var out bytes.Buffer
		out.Grow(len(f.Raw) + len(block))
		out.Write(f.Raw[:sp.Start])
		out.Write(block)
		out.Write(f.Raw[sp.End:])
		return out.Bytes(), nil
	default:
		return nil, fmt.Errorf("%s: %d BPMNDiagram elements, want at most 1", f.Path, len(f.DiagramSpans))
	}
}
