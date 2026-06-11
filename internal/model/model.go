// Package model parses BPMN 2.0 XML into a read-only semantic model and
// records the byte span of the BPMNDI diagram section so it can be replaced
// without touching any other byte of the file (comments, attribute order and
// formatting outside the diagram block are preserved exactly).
package model

// Namespace URIs used by BPMN 2.0 files.
const (
	NSBPMN   = "http://www.omg.org/spec/BPMN/20100524/MODEL"
	NSBPMNDI = "http://www.omg.org/spec/BPMN/20100524/DI"
	NSDC     = "http://www.omg.org/spec/DD/20100524/DC"
	NSDI     = "http://www.omg.org/spec/DD/20100524/DI"
	NSBIOC   = "http://bpmn.io/schema/bpmn/biocolor/1.0"
	NSColor  = "http://www.omg.org/spec/BPMN/non-normative/color/1.0"
)

// NodeKind classifies the flow nodes the layouter understands.
type NodeKind int

const (
	KindUnknown NodeKind = iota
	KindStartEvent
	KindEndEvent
	KindIntermediateCatchEvent
	KindIntermediateThrowEvent
	KindTask // any *task variant: service, user, script, send, receive, manual, business rule, plain
	KindExclusiveGateway
)

// IsEvent reports whether the kind is rendered as a 36x36 circle.
func (k NodeKind) IsEvent() bool {
	switch k {
	case KindStartEvent, KindEndEvent, KindIntermediateCatchEvent, KindIntermediateThrowEvent:
		return true
	}
	return false
}

// IsGateway reports whether the kind is rendered as a 50x50 diamond.
func (k NodeKind) IsGateway() bool { return k == KindExclusiveGateway }

// FlowNode is a vertex of the process graph.
type FlowNode struct {
	Kind     NodeKind
	Tag      string // local element name, e.g. "serviceTask"
	ID       string
	Name     string
	Incoming []string // sequence flow IDs in declared order
	Outgoing []string // sequence flow IDs in declared order
	EventDef string   // "timer", "signal", "message", ... or ""
	DocIndex int
}

// SequenceFlow is a directed edge of the process graph.
type SequenceFlow struct {
	ID        string
	Name      string
	SourceRef string
	TargetRef string
	DocIndex  int
}

// TextAnnotation is a note artifact attached via Association.
type TextAnnotation struct {
	ID       string
	Text     string
	DocIndex int
}

// Association links an artifact (annotation) to a model element.
type Association struct {
	ID        string
	SourceRef string
	TargetRef string
	DocIndex  int
}

// IDDecl records an id attribute for duplicate detection.
type IDDecl struct {
	ID  string
	Tag string
}

// Unsupported records a construct the layouter cannot handle.
type Unsupported struct {
	Tag string
	ID  string
}

// Process is one bpmn:process.
type Process struct {
	ID            string
	Name          string
	Documentation string
	Nodes         []*FlowNode
	Flows         []*SequenceFlow
	Annotations   []*TextAnnotation
	Associations  []*Association
	Unsupported   []Unsupported

	NodeByID map[string]*FlowNode
	FlowByID map[string]*SequenceFlow
	AnnByID  map[string]*TextAnnotation
}

// Attr is a namespaced attribute carried over verbatim (shape colors).
type Attr struct {
	Space string // namespace URI
	Local string
	Value string
}

// DIInfo summarizes the existing diagram interchange section.
type DIInfo struct {
	Refs        []string // bpmnElement references in document order
	RefSet      map[string]bool
	ShapeColors map[string][]Attr // bpmnElement -> bioc/color attrs
}

// Span is a half-open byte range [Start, End) in the raw file.
type Span struct {
	Start int64
	End   int64
}

// File is one parsed .bpmn file.
type File struct {
	Path string
	Raw  []byte

	// Prefixes maps namespace URI -> prefix as declared on the root element.
	// The default namespace is represented by prefix "".
	Prefixes map[string]string

	Processes   []*Process
	Unsupported []Unsupported // definitions-level unsupported constructs
	IDs         []IDDecl      // every id attribute outside the DI section

	DI            *DIInfo // nil when the file has no BPMNDiagram
	DiagramSpans  []Span  // byte spans of bpmndi:BPMNDiagram elements
	DefsEndOffset int64   // byte offset of "</bpmn:definitions>" start tag
}

// Prefix returns the declared prefix for a namespace URI and whether it is declared.
func (f *File) Prefix(ns string) (string, bool) {
	p, ok := f.Prefixes[ns]
	return p, ok
}
