# Agent notes — bpmnfmt

## Fixtures must declare Camunda 8

Every `.bpmn` under `testdata/` must carry the Camunda 8 execution-platform
declaration on `bpmn:definitions`. Without it the Camunda Modeler prompts
"Camunda 7 or Camunda 8?" on every open, which makes reviewing generated
output tedious.

Canonical header (the form already used by `testdata/tour-execution.bpmn`):

```xml
<bpmn:definitions
  xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL"
  xmlns:bpmndi="http://www.omg.org/spec/BPMN/20100524/DI"
  xmlns:dc="http://www.omg.org/spec/DD/20100524/DC"
  xmlns:di="http://www.omg.org/spec/DD/20100524/DI"
  xmlns:zeebe="http://camunda.org/schema/zeebe/1.0"
  xmlns:modeler="http://camunda.org/schema/modeler/1.0"
  id="Definitions_..."
  targetNamespace="http://bpmn.io/schema/bpmn"
  modeler:executionPlatform="Camunda Cloud"
  modeler:executionPlatformVersion="8.9.0">
```

Both namespace declarations are required. `xmlns:modeler` is easy to lose —
the Modeler itself has been observed writing `modeler:executionPlatform`
back out without declaring the prefix, which is malformed XML that Go's
lenient parser accepts silently. Declare it explicitly.

New fixtures are authored with this header from the start. `bpmn:definitions`
sits outside the DI block, so the formatter splices it through verbatim and
the goldens inherit whatever the input declares — a fixture written without
the header produces a golden without it too.
