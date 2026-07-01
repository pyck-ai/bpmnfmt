package model

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

// kindByTag maps supported flow node element names to kinds.
var kindByTag = map[string]NodeKind{
	"startEvent":             KindStartEvent,
	"endEvent":               KindEndEvent,
	"intermediateCatchEvent": KindIntermediateCatchEvent,
	"intermediateThrowEvent": KindIntermediateThrowEvent,
	"task":                   KindTask,
	"serviceTask":            KindTask,
	"userTask":               KindTask,
	"scriptTask":             KindTask,
	"sendTask":               KindTask,
	"receiveTask":            KindTask,
	"manualTask":             KindTask,
	"businessRuleTask":       KindTask,
	"exclusiveGateway":       KindExclusiveGateway,
}

// unsupportedTags are BPMN constructs the layouter refuses (lint rule E7).
// subProcess is intentionally absent: an expanded embedded sub-process is
// laid out (see rSubProcess); a collapsed one is demoted to Unsupported in
// finalizeSubProcesses.
var unsupportedTags = map[string]bool{
	"adHocSubProcess":     true,
	"transaction":         true,
	"callActivity":        true,
	"boundaryEvent":       true,
	"parallelGateway":     true,
	"inclusiveGateway":    true,
	"complexGateway":      true,
	"eventBasedGateway":   true,
	"laneSet":             true,
	"dataObject":          true,
	"dataObjectReference": true,
	"dataStoreReference":  true,
	"group":               true,
}

// unsupportedDefsTags are definitions-level unsupported constructs.
var unsupportedDefsTags = map[string]bool{
	"collaboration": true,
}

// frame roles for the parser stack.
type role int

const (
	rOther role = iota
	rDefinitions
	rProcess
	rNode
	rSubProcess
	rFlow
	rAnnotation
	rIncoming
	rOutgoing
	rText
	rDocumentation
	rDiagram
)

type frame struct {
	name xml.Name
	role role
}

// ParseFile reads and parses a .bpmn file.
func ParseFile(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw, path)
}

// Parse parses raw BPMN XML. path is used in error messages only.
func Parse(raw []byte, path string) (*File, error) {
	f := &File{
		Path:          path,
		Raw:           raw,
		Prefixes:      map[string]string{},
		DefsEndOffset: -1,
	}

	dec := xml.NewDecoder(bytes.NewReader(raw))
	var (
		stack        []frame
		curProc      *Process
		curNode      *FlowNode
		curAnn       *TextAnnotation
		curSub       *Process        // interior scope while inside an expanded sub-process
		curContainer *FlowNode       // the sub-process node owning curSub
		text         strings.Builder // accumulates chardata for incoming/outgoing/text/documentation
		docIdx       int
		diStart      int64 = -1
		seenDefs     bool
	)

	push := func(n xml.Name, r role) { stack = append(stack, frame{n, r}) }
	top := func() role {
		if len(stack) == 0 {
			return rOther
		}
		return stack[len(stack)-1].role
	}

	for {
		prev := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: parse: %w", path, err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case len(stack) == 0:
				if t.Name.Space != NSBPMN || t.Name.Local != "definitions" {
					return nil, fmt.Errorf("%s: root element is %s, want bpmn:definitions", path, t.Name.Local)
				}
				seenDefs = true
				for _, a := range t.Attr {
					switch {
					case a.Name.Space == "xmlns":
						f.Prefixes[a.Value] = a.Name.Local
					case a.Name.Space == "" && a.Name.Local == "xmlns":
						f.Prefixes[a.Value] = ""
					}
				}
				push(t.Name, rDefinitions)

			case top() == rDefinitions && t.Name.Space == NSBPMN && t.Name.Local == "process":
				curProc = &Process{
					ID:       attr(t, "id"),
					Name:     attr(t, "name"),
					NodeByID: map[string]*FlowNode{},
					FlowByID: map[string]*SequenceFlow{},
					AnnByID:  map[string]*TextAnnotation{},
				}
				f.Processes = append(f.Processes, curProc)
				f.recordID(t)
				push(t.Name, rProcess)

			case top() == rDefinitions && t.Name.Space == NSBPMNDI && t.Name.Local == "BPMNDiagram":
				diStart = prev
				if f.DI == nil {
					f.DI = &DIInfo{RefSet: map[string]bool{}, ShapeColors: map[string][]Attr{}, Expanded: map[string]bool{}}
				}
				push(t.Name, rDiagram)

			case top() == rDefinitions && t.Name.Space == NSBPMN && unsupportedDefsTags[t.Name.Local]:
				f.Unsupported = append(f.Unsupported, Unsupported{Tag: t.Name.Local, ID: attr(t, "id")})
				f.recordID(t)
				push(t.Name, rOther)

			case top() == rProcess && t.Name.Space == NSBPMN:
				f.recordID(t)
				switch {
				case kindByTag[t.Name.Local] != KindUnknown:
					curNode = &FlowNode{
						Kind:     kindByTag[t.Name.Local],
						Tag:      t.Name.Local,
						ID:       attr(t, "id"),
						Name:     attr(t, "name"),
						DocIndex: docIdx,
					}
					docIdx++
					curProc.Nodes = append(curProc.Nodes, curNode)
					if curNode.ID != "" {
						curProc.NodeByID[curNode.ID] = curNode
					}
					push(t.Name, rNode)
				case t.Name.Local == "sequenceFlow":
					fl := &SequenceFlow{
						ID:        attr(t, "id"),
						Name:      attr(t, "name"),
						SourceRef: attr(t, "sourceRef"),
						TargetRef: attr(t, "targetRef"),
						DocIndex:  docIdx,
					}
					docIdx++
					curProc.Flows = append(curProc.Flows, fl)
					if fl.ID != "" {
						curProc.FlowByID[fl.ID] = fl
					}
					push(t.Name, rFlow)
				case t.Name.Local == "textAnnotation":
					curAnn = &TextAnnotation{ID: attr(t, "id"), DocIndex: docIdx}
					docIdx++
					curProc.Annotations = append(curProc.Annotations, curAnn)
					if curAnn.ID != "" {
						curProc.AnnByID[curAnn.ID] = curAnn
					}
					push(t.Name, rAnnotation)
				case t.Name.Local == "association":
					curProc.Associations = append(curProc.Associations, &Association{
						ID:        attr(t, "id"),
						SourceRef: attr(t, "sourceRef"),
						TargetRef: attr(t, "targetRef"),
						DocIndex:  docIdx,
					})
					docIdx++
					push(t.Name, rOther)
				case t.Name.Local == "documentation":
					text.Reset()
					push(t.Name, rDocumentation)
				case t.Name.Local == "subProcess":
					// Expanded/collapsed is decided in finalizeSubProcesses
					// once the DI section has been read. Parse the interior
					// unconditionally into a Sub scope; the container node also
					// carries its own incoming/outgoing (curNode == container
					// until the first inner node is opened).
					curContainer = &FlowNode{
						Kind:     KindSubProcess,
						Tag:      t.Name.Local,
						ID:       attr(t, "id"),
						Name:     attr(t, "name"),
						DocIndex: docIdx,
					}
					docIdx++
					curProc.Nodes = append(curProc.Nodes, curContainer)
					if curContainer.ID != "" {
						curProc.NodeByID[curContainer.ID] = curContainer
					}
					curSub = &Process{
						NodeByID: map[string]*FlowNode{},
						FlowByID: map[string]*SequenceFlow{},
						AnnByID:  map[string]*TextAnnotation{},
					}
					curContainer.Sub = curSub
					curNode = curContainer
					push(t.Name, rSubProcess)
				case unsupportedTags[t.Name.Local]:
					curProc.Unsupported = append(curProc.Unsupported, Unsupported{Tag: t.Name.Local, ID: attr(t, "id")})
					push(t.Name, rOther)
				default:
					push(t.Name, rOther)
				}

			case top() == rNode && t.Name.Space == NSBPMN:
				f.recordID(t)
				switch {
				case t.Name.Local == "incoming":
					text.Reset()
					push(t.Name, rIncoming)
				case t.Name.Local == "outgoing":
					text.Reset()
					push(t.Name, rOutgoing)
				case strings.HasSuffix(t.Name.Local, "EventDefinition"):
					curNode.EventDef = strings.TrimSuffix(t.Name.Local, "EventDefinition")
					push(t.Name, rOther)
				default:
					push(t.Name, rOther)
				}

			case top() == rSubProcess && t.Name.Space == NSBPMN:
				f.recordID(t)
				switch {
				case t.Name.Local == "incoming":
					// Belongs to the container node (curNode == curContainer).
					text.Reset()
					push(t.Name, rIncoming)
				case t.Name.Local == "outgoing":
					text.Reset()
					push(t.Name, rOutgoing)
				case kindByTag[t.Name.Local] != KindUnknown:
					curNode = &FlowNode{
						Kind:     kindByTag[t.Name.Local],
						Tag:      t.Name.Local,
						ID:       attr(t, "id"),
						Name:     attr(t, "name"),
						DocIndex: docIdx,
					}
					docIdx++
					curSub.Nodes = append(curSub.Nodes, curNode)
					if curNode.ID != "" {
						curSub.NodeByID[curNode.ID] = curNode
					}
					push(t.Name, rNode)
				case t.Name.Local == "sequenceFlow":
					fl := &SequenceFlow{
						ID:        attr(t, "id"),
						Name:      attr(t, "name"),
						SourceRef: attr(t, "sourceRef"),
						TargetRef: attr(t, "targetRef"),
						DocIndex:  docIdx,
					}
					docIdx++
					curSub.Flows = append(curSub.Flows, fl)
					if fl.ID != "" {
						curSub.FlowByID[fl.ID] = fl
					}
					push(t.Name, rFlow)
				case t.Name.Local == "textAnnotation":
					curAnn = &TextAnnotation{ID: attr(t, "id"), DocIndex: docIdx}
					docIdx++
					curSub.Annotations = append(curSub.Annotations, curAnn)
					if curAnn.ID != "" {
						curSub.AnnByID[curAnn.ID] = curAnn
					}
					push(t.Name, rAnnotation)
				case t.Name.Local == "association":
					curSub.Associations = append(curSub.Associations, &Association{
						ID:        attr(t, "id"),
						SourceRef: attr(t, "sourceRef"),
						TargetRef: attr(t, "targetRef"),
						DocIndex:  docIdx,
					})
					docIdx++
					push(t.Name, rOther)
				case unsupportedTags[t.Name.Local] || t.Name.Local == "subProcess":
					// A nested sub-process (depth 2) is not laid out; recording
					// it here makes lint reject the whole file with E7.
					curSub.Unsupported = append(curSub.Unsupported, Unsupported{Tag: t.Name.Local, ID: attr(t, "id")})
					push(t.Name, rOther)
				default:
					push(t.Name, rOther)
				}

			case top() == rAnnotation && t.Name.Space == NSBPMN && t.Name.Local == "text":
				text.Reset()
				push(t.Name, rText)

			case diStart >= 0: // inside BPMNDiagram
				if t.Name.Space == NSBPMNDI && (t.Name.Local == "BPMNShape" || t.Name.Local == "BPMNEdge" || t.Name.Local == "BPMNPlane") {
					ref := attr(t, "bpmnElement")
					if ref != "" && t.Name.Local != "BPMNPlane" {
						f.DI.Refs = append(f.DI.Refs, ref)
						f.DI.RefSet[ref] = true
					}
					if t.Name.Local == "BPMNShape" {
						for _, a := range t.Attr {
							if a.Name.Space == NSBIOC || a.Name.Space == NSColor {
								f.DI.ShapeColors[ref] = append(f.DI.ShapeColors[ref], Attr{
									Space: a.Name.Space, Local: a.Name.Local, Value: a.Value,
								})
							}
						}
						if v := attr(t, "isExpanded"); v != "" && ref != "" {
							f.DI.Expanded[ref] = v == "true"
						}
					}
				}
				push(t.Name, rOther)

			default:
				if t.Name.Space == NSBPMN {
					f.recordID(t)
				}
				push(t.Name, rOther)
			}

		case xml.CharData:
			switch top() {
			case rIncoming, rOutgoing, rText, rDocumentation:
				text.Write(t)
			}

		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("%s: unbalanced XML", path)
			}
			fr := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			switch fr.role {
			case rIncoming:
				curNode.Incoming = append(curNode.Incoming, strings.TrimSpace(text.String()))
			case rOutgoing:
				curNode.Outgoing = append(curNode.Outgoing, strings.TrimSpace(text.String()))
			case rText:
				curAnn.Text = text.String()
			case rDocumentation:
				if curProc != nil && len(stack) > 0 && stack[len(stack)-1].role == rProcess {
					curProc.Documentation = text.String()
				}
			case rNode:
				curNode = nil
				if curContainer != nil {
					// Back inside a sub-process: further container-level
					// incoming/outgoing (rare) target the container again.
					curNode = curContainer
				}
			case rSubProcess:
				curSub = nil
				curContainer = nil
				curNode = nil
			case rAnnotation:
				curAnn = nil
			case rProcess:
				curProc = nil
			case rDiagram:
				f.DiagramSpans = append(f.DiagramSpans, Span{Start: diStart, End: dec.InputOffset()})
				diStart = -1
			case rDefinitions:
				f.DefsEndOffset = prev
			}
		}
	}

	if !seenDefs {
		return nil, fmt.Errorf("%s: no bpmn:definitions root element found", path)
	}
	f.finalizeSubProcesses()
	return f, nil
}

// finalizeSubProcesses decides, now that the DI section has been read,
// whether each parsed sub-process is expanded (laid out) or collapsed
// (rejected with E7), and moves process-level annotations that point into an
// expanded sub-process down into its interior scope so they are laid out and
// emitted inside the container.
func (f *File) finalizeSubProcesses() {
	for _, p := range f.Processes {
		for _, n := range p.Nodes {
			if n.Kind != KindSubProcess || n.Sub == nil {
				continue
			}
			if f.expandedSubProcess(n) {
				relocateInteriorAnnotations(p, n)
				continue
			}
			// Collapsed (or DI marks it collapsed): not laid out. Record it
			// as unsupported so lint reports E7 and formatting is blocked.
			p.Unsupported = append(p.Unsupported, Unsupported{Tag: n.Tag, ID: n.ID})
			n.Sub = nil
		}
	}
}

// expandedSubProcess reports whether the sub-process node should be laid out
// as an expanded container. An explicit isExpanded on the DI shape wins; a
// shape without the attribute is treated as collapsed; with no diagram at all
// a sub-process that has interior flow nodes is laid out expanded.
func (f *File) expandedSubProcess(n *FlowNode) bool {
	hasInner := len(n.Sub.Nodes) > 0
	if f.DI != nil {
		if v, ok := f.DI.Expanded[n.ID]; ok {
			return v
		}
		if f.DI.RefSet[n.ID] {
			return false // shape exists but no isExpanded => collapsed
		}
	}
	return hasInner
}

// relocateInteriorAnnotations moves a process-level textAnnotation (and its
// association) into the sub-process interior when the association's other end
// is an element that lives inside that sub-process.
func relocateInteriorAnnotations(p *Process, n *FlowNode) {
	sub := n.Sub
	inner := map[string]bool{}
	for _, in := range sub.Nodes {
		inner[in.ID] = true
	}
	for _, fl := range sub.Flows {
		inner[fl.ID] = true
	}

	moveAnn := map[string]bool{}    // annotation IDs to move
	keptAssoc := p.Associations[:0] // rebuilt in place
	var movedAssoc []*Association
	for _, as := range p.Associations {
		annID, other := "", ""
		switch {
		case p.AnnByID[as.SourceRef] != nil:
			annID, other = as.SourceRef, as.TargetRef
		case p.AnnByID[as.TargetRef] != nil:
			annID, other = as.TargetRef, as.SourceRef
		}
		if annID != "" && inner[other] {
			moveAnn[annID] = true
			movedAssoc = append(movedAssoc, as)
			continue
		}
		keptAssoc = append(keptAssoc, as)
	}
	if len(movedAssoc) == 0 {
		return
	}
	p.Associations = keptAssoc

	keptAnn := p.Annotations[:0]
	for _, ann := range p.Annotations {
		if moveAnn[ann.ID] {
			sub.Annotations = append(sub.Annotations, ann)
			if ann.ID != "" {
				sub.AnnByID[ann.ID] = ann
			}
			delete(p.AnnByID, ann.ID)
			continue
		}
		keptAnn = append(keptAnn, ann)
	}
	p.Annotations = keptAnn
	sub.Associations = append(sub.Associations, movedAssoc...)
}

func (f *File) recordID(t xml.StartElement) {
	if id := attr(t, "id"); id != "" {
		f.IDs = append(f.IDs, IDDecl{ID: id, Tag: t.Name.Local})
	}
}

func attr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}
